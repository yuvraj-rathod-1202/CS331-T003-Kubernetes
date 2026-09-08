/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package podconnectivity

import (
	"fmt"
)

// FailureType identifies the classified root cause of a network failure.
type FailureType string

const (
	FailureTypeNone           FailureType = "None"
	FailureTypeLocalCNI       FailureType = "LocalCNI"
	FailureTypeAppVeth        FailureType = "AppVeth"
	FailureTypeNodeIngress    FailureType = "NodeIngressDead"
	FailureTypeNodeEgress     FailureType = "NodeEgressDead"
	FailureTypeAsymmetricPath FailureType = "AsymmetricPathFailure"
	FailureTypeTunnelCrash    FailureType = "TunnelCrash"
	FailureTypeTotalPartition FailureType = "TotalNetworkPartition"
)

// EdgeProbeResult holds the outcome of a single synthetic probe edge.
type EdgeProbeResult struct {
	SourceNode string `json:"sourceNode"`
	TargetNode string `json:"targetNode"`
	Success    bool   `json:"success"`
	LatencyMs  int64  `json:"latencyMs,omitempty"`
	ErrorMsg   string `json:"errorMsg,omitempty"`
}

// LocalProbeResult holds the outcome of a node's local CNI canary probe.
type LocalProbeResult struct {
	NodeName string `json:"nodeName"`
	Success  bool   `json:"success"`
	ErrorMsg string `json:"errorMsg,omitempty"`
}

// AnchorProbeResult holds the reachability status of system anchors.
type AnchorProbeResult struct {
	NodeName   string `json:"nodeName"`
	AnchorName string `json:"anchorName"`
	Success    bool   `json:"success"`
}

// TriangulationDecision represents the synthesized diagnosis.
type TriangulationDecision struct {
	IsHealthy         bool
	FailureType       FailureType
	FaultyNode        string
	FaultyComponent   string // "CNI", "App", "NIC"
	RecommendedAction string // "cni_restart", "node_isolation", "node_drain"
	Reason            string
	Severity          string // "info", "warning", "critical", "emergency"
}

// Triangulate executes the Three-Tier Pattern Triangulation algorithm:
// Tier 1: Local CNI & veth Validation
// Tier 2: Inter-Node Triangulation
// Tier 3: Anchor Corroboration
func Triangulate(
	localProbes map[string]LocalProbeResult,
	ringProbes []EdgeProbeResult,
	anchorProbes []AnchorProbeResult,
) TriangulationDecision {
	// Tier 1: Intra-Node / Local CNI Validation
	for node, local := range localProbes {
		if !local.Success {
			// Check if this node can reach external anchors or peer nodes
			canReachExternal := false
			for _, a := range anchorProbes {
				if a.NodeName == node && a.Success {
					canReachExternal = true
					break
				}
			}
			if !canReachExternal {
				for _, r := range ringProbes {
					if r.SourceNode == node && r.Success {
						canReachExternal = true
						break
					}
				}
			}

			if canReachExternal {
				return TriangulationDecision{
					IsHealthy:         false,
					FailureType:       FailureTypeLocalCNI,
					FaultyNode:        node,
					FaultyComponent:   componentCNI,
					RecommendedAction: actionCNIRestart,
					Reason:            fmt.Sprintf("Local CNI probe failed on node %s while external egress is operational", node),
					Severity:          severityCritical,
				}
			}
		}
	}

	// Tier 2: Inter-Node Triangulation
	if len(ringProbes) == 0 {
		return TriangulationDecision{
			IsHealthy:         true,
			FailureType:       FailureTypeNone,
			Reason:            "No inter-node probes executed or single node cluster",
			Severity:          severityInfo,
			RecommendedAction: "none",
		}
	}

	// Map incoming and outgoing probe successes
	incomingSuccess := make(map[string]int)
	incomingTotal := make(map[string]int)
	outgoingSuccess := make(map[string]int)
	outgoingTotal := make(map[string]int)

	failedEdges := []EdgeProbeResult{}
	for _, p := range ringProbes {
		incomingTotal[p.TargetNode]++
		outgoingTotal[p.SourceNode]++
		if p.Success {
			incomingSuccess[p.TargetNode]++
			outgoingSuccess[p.SourceNode]++
		} else {
			failedEdges = append(failedEdges, p)
		}
	}

	if len(failedEdges) == 0 {
		return TriangulationDecision{
			IsHealthy:         true,
			FailureType:       FailureTypeNone,
			Reason:            "All inter-node ring probes are healthy",
			Severity:          severityInfo,
			RecommendedAction: "none",
		}
	}

	// Check if a specific target node has ALL incoming probes failing
	for target, total := range incomingTotal {
		if total > 0 && incomingSuccess[target] == 0 {
			return TriangulationDecision{
				IsHealthy:         false,
				FailureType:       FailureTypeNodeIngress,
				FaultyNode:        target,
				FaultyComponent:   componentCNI,
				RecommendedAction: actionCNIRestart,
				Reason:            fmt.Sprintf("All peer nodes failed to reach node %s (Ingress/Tunnel failure)", target),
				Severity:          severityCritical,
			}
		}
	}

	// Check if a specific source node has ALL outgoing probes failing
	for source, total := range outgoingTotal {
		if total > 0 && outgoingSuccess[source] == 0 {
			// Tier 3 Corroboration: check anchor for this node
			canReachAnchor := false
			for _, a := range anchorProbes {
				if a.NodeName == source && a.Success {
					canReachAnchor = true
					break
				}
			}

			if canReachAnchor {
				// Egress to cluster overlay is broken, but physical gateway is reachable
				return TriangulationDecision{
					IsHealthy:         false,
					FailureType:       FailureTypeTunnelCrash,
					FaultyNode:        source,
					FaultyComponent:   componentCNI,
					RecommendedAction: actionCNIRestart,
					Reason:            fmt.Sprintf("Node %s can reach gateway but lost cross-node overlay connectivity", source),
					Severity:          severityCritical,
				}
			}

			return TriangulationDecision{
				IsHealthy:         false,
				FailureType:       FailureTypeNodeEgress,
				FaultyNode:        source,
				FaultyComponent:   "NIC",
				RecommendedAction: actionNodeIsolation,
				Reason:            fmt.Sprintf("Node %s cannot reach any peer nodes or external anchors (Total Egress failure)", source),
				Severity:          severityCritical,
			}
		}
	}

	// Asymmetric edge failure: e.g. Node A -> Node B fails, but Node C -> Node B succeeds
	firstFailure := failedEdges[0]
	return TriangulationDecision{
		IsHealthy:         false,
		FailureType:       FailureTypeAsymmetricPath,
		FaultyNode:        firstFailure.TargetNode,
		FaultyComponent:   componentCNI,
		RecommendedAction: actionCNIRestart,
		Reason:            fmt.Sprintf("Asymmetric route/tunnel failure detected between %s and %s", firstFailure.SourceNode, firstFailure.TargetNode),
		Severity:          severityWarning,
	}
}
