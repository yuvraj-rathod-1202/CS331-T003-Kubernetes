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
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

// Remediator handles the progressive hierarchical automated remediation actions:
// Tier 1: CNI Subsystem pod restart (fast, non-disruptive)
// Tier 2: Node Taint & Cordon (containment)
// Tier 3: Workload Eviction / Node Drain (evacuate workloads)
type Remediator struct {
	Client          client.Client
	RestartAttempts map[string]int
	LastRestartTime map[string]time.Time
}

// NewRemediator creates a new Remediator instance.
func NewRemediator(c client.Client) *Remediator {
	return &Remediator{
		Client:          c,
		RestartAttempts: make(map[string]int),
		LastRestartTime: make(map[string]time.Time),
	}
}

// ExecuteRemediation executes the appropriate tier based on the diagnosis and configured policy.
func (r *Remediator) ExecuteRemediation(
	ctx context.Context,
	decision *TriangulationDecision,
	policy *remediationv1alpha1.RemediationPolicySpec,
) (string, bool, error) {
	log := logf.FromContext(ctx).WithName("remediator")
	nodeName := decision.FaultyNode

	if nodeName == "" {
		return "No target node identified for remediation", false, nil
	}

	if policy != nil && !policy.AutoRemediationEnabled {
		return fmt.Sprintf("Auto remediation disabled by policy for node %s", nodeName), false, nil
	}

	attempts := r.RestartAttempts[nodeName]

	// Determine defaults if policy is unset
	daemonSetName := "calico-node"
	cniNamespace := "kube-system"
	maxRestarts := 2
	escalateAfter := 2
	taintKey := "network-degraded"
	taintValue := "true"
	taintEffect := "NoSchedule"

	if policy != nil {
		if policy.CNISubsystem.DaemonSetName != "" {
			daemonSetName = policy.CNISubsystem.DaemonSetName
		}
		if policy.CNISubsystem.Namespace != "" {
			cniNamespace = policy.CNISubsystem.Namespace
		}
		if policy.CNISubsystem.MaxRestartAttempts > 0 {
			maxRestarts = policy.CNISubsystem.MaxRestartAttempts
		}
		if policy.NodeDrain.EscalateAfterFailedRestarts > 0 {
			escalateAfter = policy.NodeDrain.EscalateAfterFailedRestarts
		}
		if policy.NodeIsolation.TaintKey != "" {
			taintKey = policy.NodeIsolation.TaintKey
		}
		if policy.NodeIsolation.TaintValue != "" {
			taintValue = policy.NodeIsolation.TaintValue
		}
		if policy.NodeIsolation.TaintEffect != "" {
			taintEffect = policy.NodeIsolation.TaintEffect
		}
	}

	// Escalation Check: If CNI restarts failed and reached escalation limit, drain & isolate node
	if attempts >= escalateAfter && policy != nil && policy.NodeDrain.Enabled {
		log.Info("Escalating to Tier 3: Draining workloads on node", "node", nodeName, "failedRestarts", attempts)
		// Apply isolation first
		_ = r.IsolateNode(ctx, nodeName, taintKey, taintValue, taintEffect, true)
		actionMsg, err := r.DrainNode(ctx, nodeName)
		return actionMsg, err == nil, err
	}

	// Tier 2 Direct Trigger: Total egress failure / NIC dead
	if decision.RecommendedAction == "node_isolation" {
		log.Info("Triggering Tier 2: Isolating degraded node", "node", nodeName)
		err := r.IsolateNode(ctx, nodeName, taintKey, taintValue, taintEffect, true)
		if err != nil {
			return fmt.Sprintf("Failed to isolate node %s: %v", nodeName, err), false, err
		}
		return fmt.Sprintf("Tainted node %s with %s=%s:%s and cordoned node", nodeName, taintKey, taintValue, taintEffect), true, nil
	}

	// Tier 1: CNI Subsystem Pod Restart
	if attempts < maxRestarts {
		log.Info("Triggering Tier 1: Restarting CNI agent on node", "node", nodeName, "attempt", attempts+1)
		actionMsg, err := r.RestartCNIPod(ctx, nodeName, daemonSetName, cniNamespace)
		if err == nil {
			r.RestartAttempts[nodeName] = attempts + 1
			r.LastRestartTime[nodeName] = time.Now()
			return actionMsg, true, nil
		}
		return actionMsg, false, err
	}

	// Fallback to isolation if max restarts exceeded
	log.Info("Max CNI restarts reached, applying Tier 2 Node Isolation", "node", nodeName)
	err := r.IsolateNode(ctx, nodeName, taintKey, taintValue, taintEffect, true)
	if err != nil {
		return fmt.Sprintf("Failed to isolate node %s: %v", nodeName, err), false, err
	}
	return fmt.Sprintf("Max CNI restarts exceeded (%d). Tainted and cordoned node %s", maxRestarts, nodeName), true, nil
}

// RestartCNIPod locates and deletes the local CNI DaemonSet pod on the target node.
func (r *Remediator) RestartCNIPod(ctx context.Context, nodeName, daemonSetName, namespace string) (string, error) {
	log := logf.FromContext(ctx).WithName("remediator")

	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("failed to list pods in namespace %s: %w", namespace, err)
	}

	var targetPod *corev1.Pod
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != nodeName {
			continue
		}
		// Match against daemonSetName (calico-node, kindnet, etc.)
		if strings.Contains(pod.Name, daemonSetName) || strings.Contains(pod.Name, "calico-node") || strings.Contains(pod.Name, "kindnet") {
			targetPod = &pod
			break
		}
	}

	if targetPod == nil {
		return fmt.Sprintf("No CNI pod (%s) found on node %s", daemonSetName, nodeName), fmt.Errorf("cni pod not found")
	}

	log.Info("Deleting CNI pod to trigger DaemonSet respawn", "pod", targetPod.Name, "node", nodeName)
	if err := r.Client.Delete(ctx, targetPod); err != nil {
		return fmt.Sprintf("Failed to delete CNI pod %s: %v", targetPod.Name, err), err
	}

	return fmt.Sprintf("Restarted CNI agent pod %s on node %s to rebuild interfaces and routing tables", targetPod.Name, nodeName), nil
}

// IsolateNode cordons the node and applies a network-degraded taint.
func (r *Remediator) IsolateNode(ctx context.Context, nodeName, taintKey, taintValue, taintEffect string, cordon bool) error {
	log := logf.FromContext(ctx).WithName("remediator")

	node := &corev1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	patch := client.MergeFrom(node.DeepCopy())

	if cordon && !node.Spec.Unschedulable {
		log.Info("Cordoning degraded node", "node", nodeName)
		node.Spec.Unschedulable = true
	}

	// Check if taint already exists
	taintExists := false
	effect := corev1.TaintEffect(taintEffect)
	for _, t := range node.Spec.Taints {
		if t.Key == taintKey && t.Effect == effect {
			taintExists = true
			break
		}
	}

	if !taintExists {
		log.Info("Adding network-degraded taint to node", "node", nodeName, "key", taintKey, "effect", taintEffect)
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key:    taintKey,
			Value:  taintValue,
			Effect: effect,
		})
	}

	return r.Client.Patch(ctx, node, patch)
}

// DrainNode safely evicts non-DaemonSet pods from the node.
func (r *Remediator) DrainNode(ctx context.Context, nodeName string) (string, error) {
	log := logf.FromContext(ctx).WithName("remediator")

	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList); err != nil {
		return "", fmt.Errorf("failed to list pods for drain: %w", err)
	}

	evictedCount := 0
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != nodeName {
			continue
		}

		// Skip mirror / static pods
		if _, isMirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirror {
			continue
		}

		// Skip DaemonSet pods
		isDaemonSet := false
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "DaemonSet" {
				isDaemonSet = true
				break
			}
		}
		if isDaemonSet {
			continue
		}

		log.Info("Evicting application pod from degraded node", "pod", pod.Name, "namespace", pod.Namespace)
		if err := r.Client.Delete(ctx, &pod); err == nil {
			evictedCount++
		}
	}

	return fmt.Sprintf("Drained node %s (evicted %d application pods onto healthy nodes)", nodeName, evictedCount), nil
}

// ResetNodeHistory clears restart counters when a node recovers.
func (r *Remediator) ResetNodeHistory(nodeName string) {
	delete(r.RestartAttempts, nodeName)
	delete(r.LastRestartTime, nodeName)
}
