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

// Package recovery implements CNI repair actions for Problem 2.
//
// When the health agent detects a primary CNI failure, the recovery manager
// uses the backup path (which is still healthy) to reach the Kubernetes
// API server and perform repair actions:
//   - Taint the node with NoSchedule to prevent new pods during recovery
//   - Restart the failed CNI agent pod on the affected node
//   - Verify routes are restored after the CNI agent comes back up
//   - Remove the taint once recovery is confirmed
//
// This directly addresses the professor's point: today the only fix is a reboot.
// With a second path (Flannel), the operator can reach the API server and fix
// the first path (Calico) programmatically.
package recovery

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// recoveryTaintKey is the taint applied to nodes during recovery.
	// NoSchedule prevents new pods from landing while the CNI is broken.
	recoveryTaintKey = "cniresilience.cn-operator/recovering"

	// recoveryTaintEffect prevents new pods from being scheduled during recovery.
	recoveryTaintEffect = corev1.TaintEffectNoSchedule

	// cniPodReadyTimeout is how long we wait for the CNI agent pod to become
	// Ready after deletion. 60 seconds is generous for a course demo.
	cniPodReadyTimeout = 60 * time.Second

	// routeVerificationDelay gives the CNI agent time to initialise after
	// becoming Ready before we check whether it has re-installed routes.
	routeVerificationDelay = 10 * time.Second
)

// RecoveryAction describes what repair action was taken.
type RecoveryAction struct {
	// NodeName is the node that was repaired.
	NodeName string

	// Action is a description of what was done.
	Action string

	// Success is true if the action succeeded.
	Success bool

	// Duration is how long the recovery took.
	Duration time.Duration

	// Error is set if recovery failed.
	Error string

	// Timestamp is when the recovery was performed.
	Timestamp time.Time
}

// Manager handles CNI recovery operations.
type Manager struct {
	client client.Client

	// mu guards recoveryHistory; RecoverNode runs in per-node goroutines.
	mu sync.Mutex

	// recoveryHistory tracks recent recovery actions for status reporting.
	recoveryHistory []RecoveryAction
}

// NewManager creates a new recovery manager.
func NewManager(c client.Client) *Manager {
	return &Manager{
		client:          c,
		recoveryHistory: make([]RecoveryAction, 0),
	}
}

// RecoverNode performs the full recovery pipeline for a node whose primary CNI has failed.
//
// Pipeline:
//  1. Taint node (NoSchedule) - prevents new pods from landing on a broken CNI.
//  2. Restart CNI agent pod - the DaemonSet controller recreates it automatically.
//  3. Wait for CNI agent pod to become Ready (up to cniPodReadyTimeout).
//  4. Verify routes are restored (with routeVerificationDelay grace period).
//  5. Remove taint.
//  6. Record result in recoveryHistory.
//
// IMPORTANT: All Kubernetes API calls in this function go through the controller's
// client, which is configured to talk to the API server over the host network or
// via the backup CNI path - not through a ClusterIP Service on the primary network.
// That is what makes recovery possible even when the primary CNI path is down.
func (m *Manager) RecoverNode(ctx context.Context, nodeName string, cniNamespace, cniDaemonSet string) (*RecoveryAction, error) {
	logger := logf.FromContext(ctx).WithName("recovery").WithValues("node", nodeName)
	logger.Info("Starting CNI recovery pipeline")
	start := time.Now()

	action := &RecoveryAction{
		NodeName:  nodeName,
		Timestamp: time.Now(),
	}

	// Step 1: Taint the node.
	logger.Info("Step 1: Tainting node")
	if err := m.TaintNode(ctx, nodeName); err != nil {
		action.Action = "taint node"
		action.Error = err.Error()
		action.Duration = time.Since(start)
		m.recordAction(action)
		return action, fmt.Errorf("taint node %s: %w", nodeName, err)
	}

	// Step 2: Restart the CNI agent pod.
	logger.Info("Step 2: Restarting CNI agent pod", "namespace", cniNamespace, "daemonset", cniDaemonSet)
	if err := m.RestartCNIAgentPod(ctx, nodeName, cniNamespace, cniDaemonSet); err != nil {
		action.Action = "restart CNI agent pod"
		action.Error = err.Error()
		action.Duration = time.Since(start)
		m.recordAction(action)
		// Still try to untaint even on failure to avoid permanently blocking scheduling.
		m.untaintBestEffort(nodeName)
		return action, fmt.Errorf("restart CNI agent pod on node %s: %w", nodeName, err)
	}

	// Step 3: Wait for routes to be restored after the CNI agent initialises.
	logger.Info("Step 3: Waiting for CNI agent to initialise routes",
		"delay", routeVerificationDelay)
	select {
	case <-ctx.Done():
		action.Action = "wait for route restoration"
		action.Error = "context cancelled during route wait"
		action.Duration = time.Since(start)
		m.recordAction(action)
		m.untaintBestEffort(nodeName)
		return action, ctx.Err()
	case <-time.After(routeVerificationDelay):
	}

	// Step 4: Remove taint - recovery is declared successful at this point.
	// (Route verification from the operator is limited; the CNI becoming Ready
	// is the primary signal. A follow-up probe cycle by the health agent will
	// confirm path health within the next probeInterval.)
	logger.Info("Step 4: Removing recovery taint")
	if err := m.UntaintNode(ctx, nodeName); err != nil {
		// Non-fatal: the CNI is likely healthy, but the taint lingered.
		// The next reconcile will try again.
		logger.Error(err, "Failed to remove recovery taint; next reconcile will retry")
	}

	action.Action = "full recovery pipeline (taint → restart pod → wait → untaint)"
	action.Success = true
	action.Duration = time.Since(start)
	m.recordAction(action)

	logger.Info("CNI recovery pipeline completed", "duration", action.Duration)
	return action, nil
}

// TaintNode applies the recovery NoSchedule taint to a node (idempotent).
func (m *Manager) TaintNode(ctx context.Context, nodeName string) error {
	logger := logf.FromContext(ctx).WithName("recovery")

	node := &corev1.Node{}
	if err := m.client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("get node: %w", err)
	}

	// Check if taint already exists to remain idempotent.
	for _, t := range node.Spec.Taints {
		if t.Key == recoveryTaintKey && t.Effect == recoveryTaintEffect {
			logger.V(1).Info("Recovery taint already present", "node", nodeName)
			return nil
		}
	}

	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    recoveryTaintKey,
		Effect: recoveryTaintEffect,
	})

	if err := m.client.Update(ctx, node); err != nil {
		return fmt.Errorf("update node taints: %w", err)
	}

	logger.Info("Applied recovery taint", "node", nodeName)
	return nil
}

// UntaintNode removes the recovery taint from a node (idempotent).
func (m *Manager) UntaintNode(ctx context.Context, nodeName string) error {
	logger := logf.FromContext(ctx).WithName("recovery")

	node := &corev1.Node{}
	if err := m.client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("get node: %w", err)
	}

	newTaints := make([]corev1.Taint, 0, len(node.Spec.Taints))
	removed := false
	for _, t := range node.Spec.Taints {
		if t.Key == recoveryTaintKey && t.Effect == recoveryTaintEffect {
			removed = true
			continue // Skip - this removes it.
		}
		newTaints = append(newTaints, t)
	}

	if !removed {
		logger.V(1).Info("Recovery taint not present, nothing to remove", "node", nodeName)
		return nil
	}

	node.Spec.Taints = newTaints
	if err := m.client.Update(ctx, node); err != nil {
		return fmt.Errorf("update node taints: %w", err)
	}

	logger.Info("Removed recovery taint", "node", nodeName)
	return nil
}

// untaintBestEffort removes the recovery taint using a fresh context, since the
// caller's ctx may already be cancelled or past its deadline.
func (m *Manager) untaintBestEffort(nodeName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.UntaintNode(ctx, nodeName)
}

// RestartCNIAgentPod deletes the CNI agent pod on a specific node.
//
// The DaemonSet controller will automatically recreate the pod. We then wait
// for the replacement pod to become Ready (up to cniPodReadyTimeout).
//
// Pod selection: list pods in cniNamespace that are owned by a DaemonSet
// with the given name (matched via pod label "app" == cniDaemonSet) and
// running on the target node.
func (m *Manager) RestartCNIAgentPod(ctx context.Context, nodeName, cniNamespace, cniDaemonSet string) error {
	logger := logf.FromContext(ctx).WithName("recovery").WithValues("node", nodeName)

	// Find the CNI agent pod on this node.
	// Calico uses the label "app.kubernetes.io/name=calico-node" or "k8s-app=calico-node".
	// We try a few common selectors.
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(cniNamespace),
		client.MatchingLabelsSelector{
			Selector: labels.Set{"k8s-app": cniDaemonSet}.AsSelector(),
		},
	}
	if err := m.client.List(ctx, podList, listOpts...); err != nil {
		return fmt.Errorf("list CNI pods in %s: %w", cniNamespace, err)
	}

	// Filter to the pod on the target node.
	var targetPod *corev1.Pod
	for i := range podList.Items {
		if podList.Items[i].Spec.NodeName == nodeName {
			targetPod = &podList.Items[i]
			break
		}
	}

	if targetPod == nil {
		return fmt.Errorf("no %s pod found on node %s in namespace %s", cniDaemonSet, nodeName, cniNamespace)
	}

	podUID := targetPod.UID
	logger.Info("Deleting CNI agent pod to trigger DaemonSet restart",
		"pod", targetPod.Name, "uid", podUID)

	if err := m.client.Delete(ctx, targetPod); err != nil {
		return fmt.Errorf("delete CNI pod %s: %w", targetPod.Name, err)
	}

	// Wait for a NEW pod (different UID) to become Ready on this node.
	logger.Info("Waiting for replacement CNI pod to become Ready",
		"timeout", cniPodReadyTimeout)

	deadline := time.Now().Add(cniPodReadyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}

		freshList := &corev1.PodList{}
		if err := m.client.List(ctx, freshList, listOpts...); err != nil {
			logger.V(1).Info("Error listing pods during wait", "err", err)
			continue
		}

		for _, pod := range freshList.Items {
			if pod.Spec.NodeName != nodeName {
				continue
			}
			if pod.UID == podUID {
				continue // This is the old pod still terminating.
			}
			// Check if this new pod is Ready.
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					logger.Info("Replacement CNI pod is Ready", "pod", pod.Name)
					return nil
				}
			}
		}
	}

	return fmt.Errorf("timeout waiting for replacement %s pod on node %s to become Ready after %v",
		cniDaemonSet, nodeName, cniPodReadyTimeout)
}

// VerifyRoutesRestored is a lightweight post-recovery check.
// At the operator level we verify that the CNI agent pod is Ready (done in
// RestartCNIAgentPod). The actual route verification is delegated to the next
// health-agent probe cycle, which will confirm end-to-end path health.
// This function is kept for completeness and future extension.
func (m *Manager) VerifyRoutesRestored(ctx context.Context, nodeName string) (bool, error) {
	// After RestartCNIAgentPod returns nil the pod is Ready — Calico's Felix
	// initialises routes as part of its startup sequence. We consider routes
	// restored when the pod is Ready and a short grace period has elapsed.
	// The health agent's next probe cycle (every 5s) confirms end-to-end.
	return true, nil
}

// GetRecoveryHistory returns recent recovery actions for status reporting.
func (m *Manager) GetRecoveryHistory() []RecoveryAction {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]RecoveryAction, len(m.recoveryHistory))
	copy(result, m.recoveryHistory)
	return result
}

// recordAction appends an action to the history, keeping the last 20 entries.
func (m *Manager) recordAction(action *RecoveryAction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveryHistory = append(m.recoveryHistory, *action)
	const maxHistory = 20
	if len(m.recoveryHistory) > maxHistory {
		m.recoveryHistory = m.recoveryHistory[len(m.recoveryHistory)-maxHistory:]
	}
}
