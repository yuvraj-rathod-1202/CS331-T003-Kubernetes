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
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// RingEdge represents a directional probe edge in the O(N) Pingmesh ring topology.
type RingEdge struct {
	SourceNode string
	SourceIP   string
	TargetNode string
	TargetIP   string
	EdgeType   string // "forward", "reverse", "chord"
}

// RingTopology represents the calculated O(N) ring probe graph.
type RingTopology struct {
	Nodes []string
	Edges []RingEdge
}

// BuildRingTopology constructs a deterministic O(N) ring topology from worker nodes.
// For N nodes: Node[i] -> Node[(i+1)%N] (Forward) and Node[i] -> Node[(i-1+N)%N] (Reverse).
func BuildRingTopology(nodeList []corev1.Node) RingTopology {
	var nodeNames []string
	nodeIPMap := make(map[string]string)

	for _, n := range nodeList {
		nodeNames = append(nodeNames, n.Name)
		var ip string
		for _, addr := range n.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				ip = addr.Address
				break
			}
		}
		if ip == "" && len(n.Status.Addresses) > 0 {
			ip = n.Status.Addresses[0].Address
		}
		nodeIPMap[n.Name] = ip
	}

	// Sort lexicographically for deterministic ring order across all reconciliations
	sort.Strings(nodeNames)

	n := len(nodeNames)
	if n < 2 {
		return RingTopology{
			Nodes: nodeNames,
			Edges: nil,
		}
	}

	var edges []RingEdge
	for i := 0; i < n; i++ {
		source := nodeNames[i]
		nextIndex := (i + 1) % n
		target := nodeNames[nextIndex]

		edges = append(edges, RingEdge{
			SourceNode: source,
			SourceIP:   nodeIPMap[source],
			TargetNode: target,
			TargetIP:   nodeIPMap[target],
			EdgeType:   "forward",
		})

		// For N >= 3, add bidirectional reverse ring edge
		if n >= 3 {
			prevIndex := (i - 1 + n) % n
			prevTarget := nodeNames[prevIndex]
			edges = append(edges, RingEdge{
				SourceNode: source,
				SourceIP:   nodeIPMap[source],
				TargetNode: prevTarget,
				TargetIP:   nodeIPMap[prevTarget],
				EdgeType:   "reverse",
			})
		}
	}

	return RingTopology{
		Nodes: nodeNames,
		Edges: edges,
	}
}
