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
// When a node's primary path is proven broken, the controller taints the node so no
// new pods land there and this package restarts the CNI agent pod. The taint stays on until the
// probes show the path working again: a restarted pod proves nothing about the path.
// A restart does not clear every fault (an iptables DROP survives it), so the
// controller counts a recovery only when the path is back.
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

// RecoverNode restarts the CNI agent pod on a node. The controller owns the taint:
// it sets it while the node is down and removes it once the probes show the path working.
func (m *Manager) RecoverNode(ctx context.Context, nodeName string, cniNamespace, cniDaemonSet string) (*RecoveryAction, error) {
	logger := logf.FromContext(ctx).WithName("recovery").WithValues("node", nodeName)
	logger.Info("Starting CNI recovery")
	start := time.Now()

	action := &RecoveryAction{
		NodeName:  nodeName,
		Timestamp: start,
	}

	if err := m.RestartCNIAgentPod(ctx, nodeName, cniNamespace, cniDaemonSet); err != nil {
		action.Action = "restart CNI agent pod"
		action.Error = err.Error()
		action.Duration = time.Since(start)
		m.recordAction(action)
		return action, fmt.Errorf("restart CNI agent pod on node %s: %w", nodeName, err)
	}

	action.Action = "restarted CNI agent pod; waiting for probes to confirm the path"
	action.Success = true
	action.Duration = time.Since(start)
	m.recordAction(action)
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
