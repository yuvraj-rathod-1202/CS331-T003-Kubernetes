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
//  1. Reads the Multus `network-status` annotation on each pod to get backup IPs.
//  2. Creates "shadow" EndpointSlices that point to those backup IPs.
//  3. Marks the original EndpointSlice endpoints for the node as "not ready"
//     so kube-proxy stops sending new traffic to them.
//  4. On recovery: deletes the shadow slices and marks original endpoints ready again.
//
// This is modelled after VRRP (RFC 5798): a standby path takes over when the active dies.
//
// Key challenge: Kubernetes only knows the primary IP. Services, EndpointSlices,
// kube-proxy and probes all use it. Our shadow EndpointSlices are the mechanism
// to make Kubernetes route traffic to the backup IP without changing pod specs.
package failover

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// shadowLabelKey identifies EndpointSlices managed by this controller.
	shadowLabelKey = "cniresilience.cn-operator/shadow"
	shadowLabelVal = "true"

	// shadowNodeLabelKey records which node the shadow slice is for.
	shadowNodeLabelKey = "cniresilience.cn-operator/node"

	// multusNetworkStatusAnnotation is the Multus annotation key.
	multusNetworkStatusAnnotation = "k8s.v1.cni.cncf.io/network-status"

	// backupNetworkName is the Multus NAD name for the Flannel backup interface.
	backupNetworkName = "flannel-backup"
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

// multusEntry represents one element in the Multus network-status JSON array.
type multusEntry struct {
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

// FailoverNode switches traffic for pods on nodeName from primary to backup path.
//
// Steps:
//  1. List all pods on the node that have a Multus backup IP.
//  2. For each relevant Service/EndpointSlice that includes those pods:
//     a. Create a shadow EndpointSlice with the backup IPs.
//     b. Mark the original EndpointSlice endpoints for this node as NotReady,
//        so kube-proxy drains connections away from the broken primary IP.
//  3. Update nodeStates → StateBackup.
func (m *Manager) FailoverNode(ctx context.Context, nodeName string) (*FailoverEvent, error) {
	logger := logf.FromContext(ctx).WithName("failover").WithValues("node", nodeName)
	logger.Info("Starting failover to backup path")

	event := &FailoverEvent{
		NodeName:  nodeName,
		FromState: StatePrimary,
		ToState:   StateBackup,
		Reason:    "Primary CNI path failure detected",
		Timestamp: time.Now(),
	}

	// 1. Collect backup IPs for all pods on this node.
	backupIPs, affectedPods, err := m.collectBackupIPs(ctx, nodeName)
	if err != nil {
		event.Error(err)
		m.record(event)
		return event, fmt.Errorf("collect backup IPs for node %s: %w", nodeName, err)
	}
	event.AffectedPods = affectedPods
	logger.Info("Collected backup IPs", "pods", affectedPods, "backupIPs", backupIPs)

	if len(backupIPs) == 0 {
		event.Error(fmt.Errorf("no pods with Multus backup IPs found on node %s", nodeName))
		m.record(event)
		return event, fmt.Errorf("no pods with backup IPs on node %s; is Multus configured?", nodeName)
	}

	// 2. Find all EndpointSlices that have endpoints on this node and create shadows.
	shadowsCreated, err := m.createShadowsForNode(ctx, nodeName, backupIPs)
	if err != nil {
		event.Error(err)
		m.record(event)
		return event, err
	}
	logger.Info("Created shadow EndpointSlices", "count", shadowsCreated)

	// 3. Mark original endpoints on this node as NotReady.
	if err := m.setNodeEndpointsReady(ctx, nodeName, false); err != nil {
		// Non-fatal: shadow slices now exist, so kube-proxy will prefer them
		// once it notices the NotReady state. Log and continue.
		logger.Error(err, "Failed to mark original endpoints as NotReady; shadow slices active")
	}

	m.nodeStates[nodeName] = StateBackup
	event.Success = true
	m.record(event)

	logger.Info("Failover complete", "node", nodeName, "shadowsCreated", shadowsCreated)
	return event, nil
}

// FailbackNode switches traffic back from backup to primary path after recovery.
//
// Steps:
//  1. Delete all shadow EndpointSlices for this node.
//  2. Mark original EndpointSlice endpoints for this node as Ready again.
//  3. Update nodeStates → StatePrimary.
func (m *Manager) FailbackNode(ctx context.Context, nodeName string) (*FailoverEvent, error) {
	logger := logf.FromContext(ctx).WithName("failover").WithValues("node", nodeName)
	logger.Info("Starting failback to primary path")

	event := &FailoverEvent{
		NodeName:  nodeName,
		FromState: StateBackup,
		ToState:   StatePrimary,
		Reason:    "Primary CNI path recovered",
		Timestamp: time.Now(),
	}

	// 1. Delete shadow EndpointSlices for this node.
	if err := m.deleteShadowsForNode(ctx, nodeName); err != nil {
		event.Error(err)
		m.record(event)
		return event, fmt.Errorf("delete shadow slices for node %s: %w", nodeName, err)
	}

	// 2. Mark original endpoints ready again.
	if err := m.setNodeEndpointsReady(ctx, nodeName, true); err != nil {
		event.Error(err)
		m.record(event)
		return event, fmt.Errorf("restore original endpoints for node %s: %w", nodeName, err)
	}

	m.nodeStates[nodeName] = StatePrimary
	event.Success = true
	m.record(event)

	logger.Info("Failback complete", "node", nodeName)
	return event, nil
}

// GetBackupIPsForPod reads the Multus network-status annotation on a pod and
// extracts the IP(s) on the backup Flannel network (flannel-backup / net1).
func (m *Manager) GetBackupIPsForPod(ctx context.Context, namespace, podName string) ([]string, error) {
	pod := &corev1.Pod{}
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, pod); err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", namespace, podName, err)
	}

	return parseBackupIPs(pod)
}

// CreateShadowEndpointSlice creates a shadow EndpointSlice with backup IPs for a service.
// This is the exported version used by external callers (e.g. tests).
func (m *Manager) CreateShadowEndpointSlice(ctx context.Context, originalName, namespace string, backupIPs map[string]string) error {
	original := &discoveryv1.EndpointSlice{}
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: originalName}, original); err != nil {
		return fmt.Errorf("get EndpointSlice %s/%s: %w", namespace, originalName, err)
	}
	_, err := m.buildAndApplyShadow(ctx, original, backupIPs, "")
	return err
}

// Internal helpers

// collectBackupIPs returns a map[primaryIP]backupIP for all pods on nodeName
// that have a Multus backup interface, plus the count of affected pods.
func (m *Manager) collectBackupIPs(ctx context.Context, nodeName string) (map[string]string, int, error) {
	podList := &corev1.PodList{}
	if err := m.client.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return nil, 0, fmt.Errorf("list pods on node %s: %w", nodeName, err)
	}

	backupIPs := make(map[string]string) // primaryIP → backupIP
	count := 0

	for _, pod := range podList.Items {
		if pod.Status.PodIP == "" {
			continue
		}
		bips, err := parseBackupIPs(&pod)
		if err != nil || len(bips) == 0 {
			continue
		}
		backupIPs[pod.Status.PodIP] = bips[0]
		count++
	}

	return backupIPs, count, nil
}

// createShadowsForNode finds all EndpointSlices that contain endpoints on
// nodeName and creates shadow slices with backup IPs. Returns count of shadows created.
func (m *Manager) createShadowsForNode(ctx context.Context, nodeName string, backupIPs map[string]string) (int, error) {
	logger := logf.FromContext(ctx).WithName("failover")

	// List all EndpointSlices cluster-wide (we filter by node below).
	sliceList := &discoveryv1.EndpointSliceList{}
	if err := m.client.List(ctx, sliceList); err != nil {
		return 0, fmt.Errorf("list EndpointSlices: %w", err)
	}

	created := 0
	for i := range sliceList.Items {
		slice := &sliceList.Items[i]

		// Skip slices we already manage (our own shadows).
		if slice.Labels[shadowLabelKey] == shadowLabelVal {
			continue
		}

		// Check if any endpoint in this slice is on our failing node.
		hasNodeEndpoint := false
		for _, ep := range slice.Endpoints {
			if ep.NodeName != nil && *ep.NodeName == nodeName {
				hasNodeEndpoint = true
				break
			}
		}
		if !hasNodeEndpoint {
			continue
		}

		shadowName, err := m.buildAndApplyShadow(ctx, slice, backupIPs, nodeName)
		if err != nil {
			logger.Error(err, "Failed to create shadow EndpointSlice", "original", slice.Name)
			continue
		}
		logger.V(1).Info("Created shadow EndpointSlice", "shadow", shadowName, "original", slice.Name)
		created++
	}

	return created, nil
}

// buildAndApplyShadow clones an EndpointSlice, replaces primary IPs with backup
// IPs for endpoints on targetNode, and creates/updates the shadow in the cluster.
func (m *Manager) buildAndApplyShadow(ctx context.Context, original *discoveryv1.EndpointSlice, backupIPs map[string]string, targetNode string) (string, error) {
	shadow := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			// Shadow name: "shadow-<original-name>" (truncated if needed).
			Name:      shadowName(original.Name),
			Namespace: original.Namespace,
			Labels:    copyShadowLabels(original.Labels, targetNode),
		},
		AddressType: original.AddressType,
		Ports:       original.Ports,
	}

	// Rebuild endpoints: replace primary IP with backup IP where available.
	for _, ep := range original.Endpoints {
		newEP := ep.DeepCopy()

		// Only replace IPs for endpoints on the target node (if specified).
		if targetNode != "" && (ep.NodeName == nil || *ep.NodeName != targetNode) {
			shadow.Endpoints = append(shadow.Endpoints, *newEP)
			continue
		}

		// Replace addresses: map each primary IP to its backup.
		newAddresses := make([]string, 0, len(ep.Addresses))
		for _, addr := range ep.Addresses {
			if bip, ok := backupIPs[addr]; ok {
				newAddresses = append(newAddresses, bip)
			}
			// If no backup IP known for this address, omit the endpoint
			// from the shadow (better to have no endpoint than a wrong one).
		}
		if len(newAddresses) == 0 {
			continue
		}
		newEP.Addresses = newAddresses
		shadow.Endpoints = append(shadow.Endpoints, *newEP)
	}

	if len(shadow.Endpoints) == 0 {
		return "", fmt.Errorf("shadow EndpointSlice for %s would have no endpoints", original.Name)
	}

	// Use server-side apply to create or update the shadow.
	if err := m.client.Create(ctx, shadow); err != nil {
		// Already exists from a previous failover — update it.
		existing := &discoveryv1.EndpointSlice{}
		if getErr := m.client.Get(ctx, client.ObjectKey{
			Namespace: shadow.Namespace, Name: shadow.Name,
		}, existing); getErr != nil {
			return "", fmt.Errorf("get existing shadow slice: %w", getErr)
		}
		existing.Endpoints = shadow.Endpoints
		existing.Ports = shadow.Ports
		if updateErr := m.client.Update(ctx, existing); updateErr != nil {
			return "", fmt.Errorf("update shadow slice: %w", updateErr)
		}
	}

	return shadow.Name, nil
}

// deleteShadowsForNode removes all shadow EndpointSlices tagged for nodeName.
func (m *Manager) deleteShadowsForNode(ctx context.Context, nodeName string) error {
	sliceList := &discoveryv1.EndpointSliceList{}
	if err := m.client.List(ctx, sliceList,
		client.MatchingLabelsSelector{
			Selector: labels.Set{
				shadowLabelKey:     shadowLabelVal,
				shadowNodeLabelKey: nodeName,
			}.AsSelector(),
		},
	); err != nil {
		return fmt.Errorf("list shadow EndpointSlices: %w", err)
	}

	for i := range sliceList.Items {
		if err := m.client.Delete(ctx, &sliceList.Items[i]); err != nil {
			return fmt.Errorf("delete shadow slice %s: %w", sliceList.Items[i].Name, err)
		}
	}
	return nil
}

// setNodeEndpointsReady sets the Ready condition on all endpoints for nodeName
// in all non-shadow EndpointSlices. On failover we set ready=false so kube-proxy
// drains. On failback we restore ready=true.
func (m *Manager) setNodeEndpointsReady(ctx context.Context, nodeName string, ready bool) error {
	sliceList := &discoveryv1.EndpointSliceList{}
	if err := m.client.List(ctx, sliceList); err != nil {
		return fmt.Errorf("list EndpointSlices: %w", err)
	}

	for i := range sliceList.Items {
		slice := &sliceList.Items[i]
		if slice.Labels[shadowLabelKey] == shadowLabelVal {
			continue // skip our own shadow slices
		}

		changed := false
		for j := range slice.Endpoints {
			ep := &slice.Endpoints[j]
			if ep.NodeName != nil && *ep.NodeName == nodeName {
				r := ready
				ep.Conditions.Ready = &r
				changed = true
			}
		}

		if changed {
			if err := m.client.Update(ctx, slice); err != nil {
				return fmt.Errorf("update EndpointSlice %s: %w", slice.Name, err)
			}
		}
	}
	return nil
}

// parseBackupIPs reads the Multus network-status annotation on a pod and
// extracts IPs from the backup Flannel network entry.
func parseBackupIPs(pod *corev1.Pod) ([]string, error) {
	annotation, ok := pod.Annotations[multusNetworkStatusAnnotation]
	if !ok || annotation == "" {
		return nil, nil
	}

	var entries []multusEntry
	if err := json.Unmarshal([]byte(annotation), &entries); err != nil {
		return nil, fmt.Errorf("parse Multus annotation on %s: %w", pod.Name, err)
	}

	for _, entry := range entries {
		if entry.Name == backupNetworkName || entry.Interface == "net1" {
			return entry.IPs, nil
		}
	}
	return nil, nil
}

// shadowName generates a deterministic shadow EndpointSlice name from the original.
// We prefix with "shadow-" and truncate to stay under the 253-char k8s name limit.
func shadowName(originalName string) string {
	const prefix = "shadow-"
	name := prefix + originalName
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}

// copyShadowLabels builds the label set for a shadow EndpointSlice.
// We copy service-name labels (needed for kube-proxy discovery) and add our
// own management labels so we can list/delete our shadows efficiently.
func copyShadowLabels(original map[string]string, nodeName string) map[string]string {
	out := make(map[string]string)

	// Service discovery label — kube-proxy uses this to find slices for a service.
	if svc, ok := original["kubernetes.io/service-name"]; ok {
		out["kubernetes.io/service-name"] = svc
	}

	// Our management labels.
	out[shadowLabelKey] = shadowLabelVal
	if nodeName != "" {
		out[shadowNodeLabelKey] = nodeName
	}

	return out
}

// FailoverEvent helpers

// Error records a failure on the event (avoids embedding error in struct).
func (e *FailoverEvent) Error(err error) {
	e.Success = false
	// We store the error message in Reason for simplicity (no extra field needed).
	e.Reason = fmt.Sprintf("FAILED: %v", err)
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

func (m *Manager) record(e *FailoverEvent) {
	m.failoverHistory = append(m.failoverHistory, *e)
	const maxHistory = 20
	if len(m.failoverHistory) > maxHistory {
		m.failoverHistory = m.failoverHistory[len(m.failoverHistory)-maxHistory:]
	}
}
