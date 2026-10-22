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
// uses the **backup path** (which is still healthy) to reach the Kubernetes
// API server and perform repair actions:
//   - Restart the failed CNI agent pod on the affected node
//   - Re-apply missing routes
//   - Taint the node to prevent new pods until recovery completes
//   - Remove the taint once recovery is confirmed
//
// This directly addresses the professor's point: today the only fix is a reboot.
// With a second path, we can fix the first path programmatically.
package recovery

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// recoveryTaintKey is the taint applied to nodes during recovery.
	recoveryTaintKey = "cniresilience.cn-operator/recovering"

	// recoveryTaintEffect prevents new pods from being scheduled during recovery.
	recoveryTaintEffect = corev1.TaintEffectNoSchedule
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

// RecoverNode performs recovery actions for a node whose primary CNI has failed.
//
// TODO: Implement the recovery pipeline:
//
//  1. Taint the node with NoSchedule to prevent new pods:
//     node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
//         Key: recoveryTaintKey, Effect: recoveryTaintEffect,
//     })
//
//  2. Restart the failed CNI agent pod:
//     a. Find the CNI agent pod on this node (e.g. calico-node-xxxxx on nodeName).
//     b. Delete it — the DaemonSet controller will recreate it.
//     c. Wait for the new pod to become Ready.
//
//  3. Verify routes are restored:
//     a. After the CNI agent restarts, check that expected routes are present.
//     b. If routes are still missing, try to re-apply them manually.
//
//  4. Remove the taint once recovery is confirmed.
//
//  5. Record the recovery action for status reporting.
//
// IMPORTANT: All API calls during recovery must go through the backup path.
// If the primary network is down, the standard kubeconfig API endpoint
// (which goes through a Service → primary network) may not work. The
// controller should be configured to reach the API server directly
// (e.g. via the host network or the backup CNI's IP).
func (m *Manager) RecoverNode(ctx context.Context, nodeName string, cniNamespace, cniDaemonSet string) (*RecoveryAction, error) {
	logger := logf.FromContext(ctx).WithName("recovery").WithValues("node", nodeName)
	logger.Info("Starting CNI recovery")
	start := time.Now()

	// TODO: Implement the recovery pipeline.
	// Step 1: Taint the node.
	// Step 2: Restart the CNI agent pod.
	// Step 3: Verify routes.
	// Step 4: Remove taint.
	// Step 5: Record result.

	action := &RecoveryAction{
		NodeName:  nodeName,
		Action:    "TODO: not yet implemented",
		Success:   false,
		Duration:  time.Since(start),
		Error:     "not yet implemented",
		Timestamp: time.Now(),
	}
	m.recoveryHistory = append(m.recoveryHistory, *action)

	return action, fmt.Errorf("TODO: RecoverNode not yet implemented")
}

// TaintNode applies the recovery taint to a node.
//
// TODO: Implement:
//   1. Get the node.
//   2. Check if taint already exists (idempotent).
//   3. Add the taint.
//   4. Update the node.
func (m *Manager) TaintNode(ctx context.Context, nodeName string) error {
	// TODO: Implement node tainting.

	return fmt.Errorf("TODO: TaintNode not yet implemented")
}

// UntaintNode removes the recovery taint from a node.
//
// TODO: Implement:
//   1. Get the node.
//   2. Remove the recovery taint from the taints list.
//   3. Update the node.
func (m *Manager) UntaintNode(ctx context.Context, nodeName string) error {
	// TODO: Implement taint removal.

	return fmt.Errorf("TODO: UntaintNode not yet implemented")
}

// RestartCNIAgentPod deletes the CNI agent pod on a node so the DaemonSet recreates it.
//
// TODO: Implement:
//   1. List pods in cniNamespace with label selector matching the DaemonSet.
//   2. Filter for the pod running on the target node (pod.Spec.NodeName == nodeName).
//   3. Delete the pod.
//   4. Wait for the new pod to reach Ready state (with timeout).
func (m *Manager) RestartCNIAgentPod(ctx context.Context, nodeName, cniNamespace, cniDaemonSet string) error {
	_ = logf.FromContext(ctx).WithName("recovery")

	// TODO: Implement CNI agent pod restart.

	return fmt.Errorf("TODO: RestartCNIAgentPod not yet implemented")
}

// VerifyRoutesRestored checks that the CNI has re-established routes after restart.
//
// TODO: Implement:
//   1. Wait a few seconds for the CNI agent to initialize.
//   2. Check that routes to other nodes' pod CIDRs are present.
//   3. Return true if routes are restored, false otherwise.
func (m *Manager) VerifyRoutesRestored(ctx context.Context, nodeName string) (bool, error) {
	// TODO: Implement route verification post-recovery.

	return false, fmt.Errorf("TODO: VerifyRoutesRestored not yet implemented")
}

// GetRecoveryHistory returns recent recovery actions for status reporting.
func (m *Manager) GetRecoveryHistory() []RecoveryAction {
	result := make([]RecoveryAction, len(m.recoveryHistory))
	copy(result, m.recoveryHistory)
	return result
}
