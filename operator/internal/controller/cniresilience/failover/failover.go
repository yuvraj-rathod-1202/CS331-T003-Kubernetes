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

// Package failover implements traffic failover for Problem 2 (Option A: active/standby).
//
// When the primary CNI path fails, this manager switches traffic to the backup path:
//   1. Reads the Multus `network-status` annotation on each pod to get backup IPs.
//   2. Maintains "shadow" EndpointSlices that point to backup IPs.
//   3. On failure: switches Services from primary EndpointSlices to shadow ones.
//   4. On recovery: switches back to primary EndpointSlices.
//
// This is modeled after VRRP (RFC 5798): a standby path takes over when the active dies.
//
// Key challenge: Kubernetes only knows the primary IP. Services, EndpointSlices,
// kube-proxy, and probes all use it. Our shadow EndpointSlices are the mechanism
// to make Kubernetes route traffic to the backup IP.
package failover

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// FailoverState tracks whether a node is on its primary or backup path.
type FailoverState string

const (
	// StatePrimary means traffic is flowing over the primary CNI path.
	StatePrimary FailoverState = "primary"

	// StateBackup means traffic has failed over to the backup CNI path.
	StateBackup FailoverState = "backup"
)

// FailoverEvent records a failover or failback action.
type FailoverEvent struct {
	// NodeName is the affected node.
	NodeName string

	// FromState is the previous state.
	FromState FailoverState

	// ToState is the new state.
	ToState FailoverState

	// Reason is why the failover happened.
	Reason string

	// Timestamp is when it happened.
	Timestamp time.Time

	// Success is whether the failover completed successfully.
	Success bool

	// AffectedPods is how many pods were switched.
	AffectedPods int
}

// MultusNetworkStatus represents one entry in the Multus network-status annotation.
// The annotation is a JSON array of these objects.
//
// Example annotation value:
//
//	[
//	  {"name":"cluster-network", "interface":"eth0", "ips":["10.244.1.5"]},
//	  {"name":"flannel-backup",  "interface":"net1", "ips":["192.168.1.5"]}
//	]
type MultusNetworkStatus struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface"`
	IPs       []string `json:"ips"`
}

// Manager handles traffic failover between primary and backup CNI paths.
type Manager struct {
	client client.Client

	// nodeStates tracks which path each node is currently using.
	nodeStates map[string]FailoverState

	// failoverHistory records recent failover events.
	failoverHistory []FailoverEvent
}

// NewManager creates a new failover manager.
func NewManager(c client.Client) *Manager {
	return &Manager{
		client:          c,
		nodeStates:      make(map[string]FailoverState),
		failoverHistory: make([]FailoverEvent, 0),
	}
}

// FailoverNode switches traffic for pods on a node from primary to backup path.
//
// TODO: Implement the failover flow:
//
//  1. Get all pods on the affected node.
//
//  2. For each pod, read the Multus `k8s.v1.cni.cncf.io/network-status` annotation.
//     Parse it as JSON → []MultusNetworkStatus.
//     Extract the backup IP (the one from the "flannel-backup" network).
//
//  3. Find all Services whose EndpointSlices include these pods.
//
//  4. Create "shadow" EndpointSlices for each Service:
//     - Same labels/ownership as the original.
//     - But with the backup IP instead of the primary IP.
//     - Mark the original EndpointSlice endpoints for this node as "not ready".
//
//  5. Update the node's failover state to StateBackup.
//
//  6. Record the FailoverEvent.
//
// IMPORTANT: The shadow EndpointSlice must be managed by our controller,
// not by kube-controller-manager. Use a distinct label to identify our
// managed EndpointSlices (e.g. "managed-by: cniresilience-controller").
func (m *Manager) FailoverNode(ctx context.Context, nodeName string) (*FailoverEvent, error) {
	logger := logf.FromContext(ctx).WithName("failover").WithValues("node", nodeName)
	logger.Info("Failing over to backup path")

	// TODO(Arpan): Implement failover logic.

	event := &FailoverEvent{
		NodeName:  nodeName,
		FromState: StatePrimary,
		ToState:   StateBackup,
		Reason:    "Primary CNI path failure detected",
		Timestamp: time.Now(),
		Success:   false,
	}
	m.failoverHistory = append(m.failoverHistory, *event)

	return event, fmt.Errorf("TODO: FailoverNode not yet implemented")
}

// FailbackNode switches traffic back from backup to primary path after recovery.
//
// TODO: Implement the failback flow:
//   1. Verify the primary path is actually healthy (caller should confirm).
//   2. For each pod on the node, restore the primary IP in EndpointSlices.
//   3. Remove the shadow EndpointSlices.
//   4. Update state to StatePrimary.
//   5. Record the FailoverEvent.
func (m *Manager) FailbackNode(ctx context.Context, nodeName string) (*FailoverEvent, error) {
	logger := logf.FromContext(ctx).WithName("failover").WithValues("node", nodeName)
	logger.Info("Failing back to primary path")

	// TODO: Implement failback logic.

	event := &FailoverEvent{
		NodeName:  nodeName,
		FromState: StateBackup,
		ToState:   StatePrimary,
		Reason:    "Primary CNI path recovered",
		Timestamp: time.Now(),
		Success:   false,
	}
	m.failoverHistory = append(m.failoverHistory, *event)

	return event, fmt.Errorf("TODO: FailbackNode not yet implemented")
}

// GetBackupIPsForPod reads the Multus network-status annotation and extracts backup IPs.
//
// TODO: Implement:
//   1. Get the pod.
//   2. Read annotation "k8s.v1.cni.cncf.io/network-status".
//   3. Parse as JSON → []MultusNetworkStatus.
//   4. Find the entry for the backup network (by name or interface "net1").
//   5. Return the backup IPs.
func (m *Manager) GetBackupIPsForPod(ctx context.Context, namespace, podName string) ([]string, error) {
	// TODO: Implement Multus annotation parsing.

	return nil, fmt.Errorf("TODO: GetBackupIPsForPod not yet implemented")
}

// CreateShadowEndpointSlice creates a shadow EndpointSlice with backup IPs.
//
// TODO: Implement:
//   1. Clone the original EndpointSlice structure.
//   2. Replace pod IPs with backup IPs for the affected node.
//   3. Add a label "managed-by: cniresilience-controller".
//   4. Create the shadow EndpointSlice.
func (m *Manager) CreateShadowEndpointSlice(ctx context.Context, originalName, namespace string, backupIPs map[string]string) error {
	// TODO: Implement shadow EndpointSlice creation.

	return fmt.Errorf("TODO: CreateShadowEndpointSlice not yet implemented")
}

// GetNodeState returns the current failover state of a node.
func (m *Manager) GetNodeState(nodeName string) FailoverState {
	state, ok := m.nodeStates[nodeName]
	if !ok {
		return StatePrimary
	}
	return state
}

// GetFailoverHistory returns recent failover events.
func (m *Manager) GetFailoverHistory() []FailoverEvent {
	result := make([]FailoverEvent, len(m.failoverHistory))
	copy(result, m.failoverHistory)
	return result
}

// IsNodeFailedOver returns true if the node is currently on the backup path.
func (m *Manager) IsNodeFailedOver(nodeName string) bool {
	return m.GetNodeState(nodeName) == StateBackup
}
