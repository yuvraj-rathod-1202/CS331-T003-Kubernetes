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

// Package monitor tracks per-node IP address usage and detects exhaustion.
//
// The Monitor watches Pods and Nodes to compute per-node IP utilization.
// When a node's usage crosses the ExpandThreshold, it signals the IPAM
// controller to allocate additional blocks. It also detects FailedCreatePodSandBox
// events that indicate IP exhaustion.
package monitor

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// NodeUsage holds the IP usage statistics for a single node.
type NodeUsage struct {
	// NodeName is the Kubernetes node name.
	NodeName string

	// AllocatedIPs is the number of pod IPs currently in use on this node.
	AllocatedIPs int

	// TotalCapacity is the total number of addresses available on this node.
	TotalCapacity int

	// UtilizationPercent is (AllocatedIPs / TotalCapacity) * 100.
	UtilizationPercent float64

	// PendingPods is the count of pods stuck in ContainerCreating on this node.
	PendingPods int

	// HasIPExhaustionEvents is true if FailedCreatePodSandBox events exist.
	HasIPExhaustionEvents bool
}

// Signal represents a signal from the monitor to the controller.
type Signal struct {
	// Type is the signal type.
	Type SignalType

	// NodeName is the affected node (empty for cluster-wide signals).
	NodeName string

	// Message is a human-readable description.
	Message string

	// Usage contains the current usage stats (if applicable).
	Usage *NodeUsage
}

// SignalType identifies the kind of signal.
type SignalType int

const (
	// SignalExpandNeeded means a node needs more IP space.
	SignalExpandNeeded SignalType = iota

	// SignalPoolExhausted means the entire cluster pool is nearly full.
	SignalPoolExhausted

	// SignalLeaksDetected means leaked IPs were found on a node.
	SignalLeaksDetected

	// SignalNodeIdle means a node has very low utilization and blocks could be reclaimed.
	SignalNodeIdle
)

// Monitor watches per-node IP usage and emits signals to the IPAM controller.
type Monitor struct {
	client    client.Client
	mu        sync.RWMutex
	nodeUsage map[string]*NodeUsage
}

// NewMonitor creates a new Monitor instance.
func NewMonitor(c client.Client) *Monitor {
	return &Monitor{
		client:    c,
		nodeUsage: make(map[string]*NodeUsage),
	}
}

// CollectNodeUsage gathers IP usage for all nodes in the cluster.
//
// TODO: Implement:
//  1. List all Nodes.
//  2. For each Node, list all Pods running on it (using field selector spec.nodeName).
//  3. Count pods with a valid PodIP (those are the allocated IPs).
//  4. Get total capacity from NodeIPAllocation CRs (the sum of block addresses).
//  5. Compute utilization percentage.
//  6. Check for pods stuck in ContainerCreating (pod.Status.Phase + container statuses).
//  7. Store results in nodeUsage map.
func (m *Monitor) CollectNodeUsage(ctx context.Context) (map[string]*NodeUsage, error) {
	logger := logf.FromContext(ctx).WithName("ipam-monitor")
	logger.V(1).Info("Collecting node IP usage")

	// TODO: List nodes and pods, compute per-node usage.

	return nil, fmt.Errorf("TODO: CollectNodeUsage not yet implemented")
}

// DetectExhaustionEvents checks for FailedCreatePodSandBox events on a node.
//
// TODO: Implement:
//  1. List Events in the default namespace (or all namespaces) with reason "FailedCreatePodSandBox".
//  2. Filter events that mention "no IP addresses available" or similar.
//  3. Group by node (from the event's involvedObject → pod → spec.nodeName).
//  4. Return true if recent events (within last 5 minutes) exist for this node.
func (m *Monitor) DetectExhaustionEvents(ctx context.Context, nodeName string) (bool, error) {
	_ = logf.FromContext(ctx).WithName("ipam-monitor")

	// TODO: Query events for IP exhaustion signals.

	return false, fmt.Errorf("TODO: DetectExhaustionEvents not yet implemented")
}

// EvaluateSignals analyzes current usage and emits signals for the controller.
//
// TODO: Implement:
//  1. Call CollectNodeUsage.
//  2. For each node:
//     a. If utilization > expandThreshold → emit SignalExpandNeeded.
//     b. If utilization < 20% and node has multiple blocks → emit SignalNodeIdle.
//     c. If exhaustion events detected → emit SignalExpandNeeded (urgent).
//  3. Compute cluster-wide utilization. If > 90% → emit SignalPoolExhausted.
func (m *Monitor) EvaluateSignals(ctx context.Context, expandThreshold int) ([]Signal, error) {
	_ = logf.FromContext(ctx).WithName("ipam-monitor")

	// TODO: Implement signal evaluation.

	return nil, fmt.Errorf("TODO: EvaluateSignals not yet implemented")
}

// GetNodeUsage returns the last known usage for a node.
func (m *Monitor) GetNodeUsage(nodeName string) *NodeUsage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nodeUsage[nodeName]
}

// GetAllNodeUsage returns usage for all nodes.
func (m *Monitor) GetAllNodeUsage() map[string]*NodeUsage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy
	result := make(map[string]*NodeUsage, len(m.nodeUsage))
	for k, v := range m.nodeUsage {
		copied := *v
		result[k] = &copied
	}
	return result
}

// isPodRunningWithIP checks if a pod has a valid IP and is running.
//
// TODO: This is a helper. Implement:
//   - Return true if pod.Status.PodIP is non-empty and pod is not in Succeeded/Failed phase.
func isPodRunningWithIP(pod *corev1.Pod) bool {
	if pod.Status.PodIP == "" {
		return false
	}
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
	return true
}
