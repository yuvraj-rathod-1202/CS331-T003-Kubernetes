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

// Package cniresilience implements the top-level controller for Problem 2.
//
// This controller reconciles the CNIResilience CR. On each reconcile, it:
//  1. Reads per-node health status from the health agent
//  2. For each node where the primary path is down:
//     a. If autoRecover: calls recoveryManager.RecoverNode.
//     b. If autoFailover: calls failoverManager.FailoverNode.
//  3. For nodes that were failed-over but whose primary has recovered:
//     calls failoverManager.FailbackNode to restore traffic to primary.
//  4. Updates the CNIResilience CR status with per-node health and counters.
//
// This is an intentionally thin orchestration layer — the heavy lifting lives
// in the healthagent, recovery, and failover packages.
package cniresilience

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/failover"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/recovery"
)

const (
	requeueInterval = 10 * time.Second

	// phaseHealthy is the overall phase when all nodes have healthy primary paths.
	phaseHealthy = "Healthy"
	// phaseDegraded is when one or more nodes have a failed primary path.
	phaseDegraded = "Degraded"
	// phaseFailedOver is when at least one node has failed over to the backup path.
	phaseFailedOver = "FailedOver"

	activePathPrimary = "primary"
	activePathBackup  = "backup"

	// Minimum time between recovery attempts for the same node, to avoid tight
	// restart loops that could OOM-kill the CNI agent on a broken node.
	recoveryBackoff = 2 * time.Minute
)

// CNIResilienceController reconciles the CNIResilience CR.
type CNIResilienceController struct {
	client.Client
	Scheme *runtime.Scheme

	healthAgent     *healthagent.Agent
	recoveryManager *recovery.Manager
	failoverManager *failover.Manager

	// lastRecoveryAttempt tracks when we last tried recovery per node,
	// to implement the recoveryBackoff cooldown.
	lastRecoveryAttempt map[string]time.Time
}

// NewCNIResilienceController creates the controller and wires sub-components.
func NewCNIResilienceController(c client.Client, scheme *runtime.Scheme) *CNIResilienceController {
	return &CNIResilienceController{
		Client:              c,
		Scheme:              scheme,
		healthAgent:         healthagent.NewAgent(c),
		recoveryManager:     recovery.NewManager(c),
		failoverManager:     failover.NewManager(c),
		lastRecoveryAttempt: make(map[string]time.Time),
	}
}

// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=cniresiliencies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=cniresiliencies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main control loop for CNI resilience monitoring and response.
//
// Sequence:
//  1. Fetch CNIResilience CR (one per cluster).
//  2. Configure the health agent from the CR spec (probe interval, timeout, threshold).
//  3. Run a probe cycle against all peer nodes.
//  4. For each node that is now unhealthy on the primary path:
//     a. Failover (create shadow EndpointSlices with backup IPs).
//     b. Recover (restart CNI agent, taint/untaint node).
//  5. For each node that is healthy on the primary path but still in failover state:
//     Failback (restore original EndpointSlices).
//  6. Update CR status with per-node health summary and global counters.
func (r *CNIResilienceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithName("cniresilience-controller")

	// 1. Fetch CR.
	var cr remediationv1alpha1.CNIResilience
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger.Info("Reconciling CNIResilience", "name", cr.Name)

	// 2. Configure agent.
	r.healthAgent.Configure(
		cr.Spec.ProbeIntervalSeconds,
		cr.Spec.ProbeTimeoutSeconds,
		cr.Spec.FailureThreshold,
	)

	// 3. Probe all nodes.
	nodeHealthMap, err := r.healthAgent.ProbeAllNodes(ctx)
	if err != nil {
		logger.Error(err, "Health agent probe cycle failed; will retry")
		// Update last-reconcile time and requeue. Don't fail hard — a transient
		// API server hiccup should not cascade into a crash loop.
		return r.updateStatusAndRequeue(ctx, &cr, nil)
	}

	// 4 & 5. Act on health changes.
	newNodeStatuses := make([]remediationv1alpha1.NodeCNIStatus, 0, len(nodeHealthMap))
	failoverCount := cr.Status.FailoverCount
	recoveryCount := cr.Status.RecoveryCount
	anyFailedOver := false

	for nodeName, health := range nodeHealthMap {
		nodeStatus := buildNodeStatus(nodeName, health)

		primaryDown := !health.PrimaryHealthy
		backupUp := health.BackupHealthy
		isCurrentlyFailedOver := r.failoverManager.IsNodeFailedOver(nodeName)

		switch {
		case primaryDown && backupUp && !isCurrentlyFailedOver:
			// Primary failed; backup is alive - failover then recover.
			logger.Info("Primary CNI down, backup healthy → failover + recover",
				"node", nodeName,
				"primaryConsecutiveFailures", health.PrimaryConsecutiveFailures,
			)

			if cr.Spec.AutoFailover {
				if event, foErr := r.failoverManager.FailoverNode(ctx, nodeName); foErr != nil {
					logger.Error(foErr, "Failover failed", "node", nodeName)
					nodeStatus.Message = fmt.Sprintf("Failover failed: %v", foErr)
				} else if event.Success {
					failoverCount++
					nodeStatus.ActivePath = activePathBackup
					nodeStatus.Message = "Failed over to backup path"
					now := metav1.Now()
					nodeStatus.LastFailoverTime = &now
					anyFailedOver = true
				}
			}

			if cr.Spec.AutoRecover && r.recoveryAllowed(nodeName) {
				r.lastRecoveryAttempt[nodeName] = time.Now()
				// Run recovery asynchronously so the reconcile loop is not blocked
				// for up to 60 seconds waiting for the CNI pod to restart.
				go func(nn string, spec remediationv1alpha1.CNIResilienceSpec) {
					recCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					if action, recErr := r.recoveryManager.RecoverNode(
						recCtx, nn,
						spec.PrimaryCNI.Namespace,
						spec.PrimaryCNI.DaemonSetName,
					); recErr != nil {
						logger.Error(recErr, "CNI recovery failed", "node", nn)
					} else if action.Success {
						logger.Info("CNI recovery succeeded", "node", nn, "duration", action.Duration)
					}
				}(nodeName, cr.Spec)
				recoveryCount++
			}

		case !primaryDown && isCurrentlyFailedOver:
			// Primary has recovered; fail back to primary.
			logger.Info("Primary CNI recovered → failback", "node", nodeName)

			if event, fbErr := r.failoverManager.FailbackNode(ctx, nodeName); fbErr != nil {
				logger.Error(fbErr, "Failback failed", "node", nodeName)
				nodeStatus.Message = fmt.Sprintf("Failback failed: %v", fbErr)
				anyFailedOver = true // still in failover state
			} else if event.Success {
				nodeStatus.ActivePath = activePathPrimary
				nodeStatus.Message = "Recovered; traffic restored to primary path"
				now := metav1.Now()
				nodeStatus.LastFailoverTime = &now
			}

		case primaryDown && !backupUp:
			// Both paths down - catastrophic. Taint node and alert.
			logger.Error(fmt.Errorf("both CNI paths down"), "Catastrophic network failure",
				"node", nodeName)
			nodeStatus.Message = "BOTH primary and backup CNI paths are down"
			if taintErr := r.recoveryManager.TaintNode(ctx, nodeName); taintErr != nil {
				logger.Error(taintErr, "Failed to taint node with both paths down", "node", nodeName)
			}
			anyFailedOver = true

		default:
			// Both paths healthy and on primary.
			nodeStatus.ActivePath = activePathPrimary
			nodeStatus.Message = "All paths healthy"
		}

		newNodeStatuses = append(newNodeStatuses, nodeStatus)
	}

	// 6. Update status.
	cr.Status.NodeStatuses = newNodeStatuses
	cr.Status.FailoverCount = failoverCount
	cr.Status.RecoveryCount = recoveryCount
	cr.Status.FailoverActive = anyFailedOver
	cr.Status.Phase = computePhase(anyFailedOver, newNodeStatuses)

	return r.updateStatusAndRequeue(ctx, &cr, nil)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CNIResilienceController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1alpha1.CNIResilience{}).
		Named("cniresilience").
		Complete(r)
}

// Helpers

// updateStatusAndRequeue updates the CR status and returns a requeue result.
func (r *CNIResilienceController) updateStatusAndRequeue(
	ctx context.Context,
	cr *remediationv1alpha1.CNIResilience,
	statusErr error,
) (ctrl.Result, error) {
	now := metav1.Now()
	cr.Status.LastReconcileTime = &now

	if err := r.Status().Update(ctx, cr); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to update CNIResilience status")
		return ctrl.Result{}, err
	}

	if statusErr != nil {
		return ctrl.Result{RequeueAfter: requeueInterval}, statusErr
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// recoveryAllowed returns true if we have not attempted recovery on this node
// within the recoveryBackoff period.
func (r *CNIResilienceController) recoveryAllowed(nodeName string) bool {
	last, ok := r.lastRecoveryAttempt[nodeName]
	if !ok {
		return true
	}
	return time.Since(last) >= recoveryBackoff
}

// buildNodeStatus converts the health-agent's NodeHealthStatus to the CRD status type.
func buildNodeStatus(nodeName string, h *healthagent.NodeHealthStatus) remediationv1alpha1.NodeCNIStatus {
	now := metav1.Now()
	activePath := activePathPrimary
	if !h.PrimaryHealthy && h.BackupHealthy {
		activePath = activePathBackup
	}
	return remediationv1alpha1.NodeCNIStatus{
		NodeName:                   nodeName,
		PrimaryHealthy:             h.PrimaryHealthy,
		BackupHealthy:              h.BackupHealthy,
		ActivePath:                 activePath,
		PrimaryConsecutiveFailures: h.PrimaryConsecutiveFailures,
		BackupConsecutiveFailures:  h.BackupConsecutiveFailures,
		LastProbeTime:              &now,
	}
}

// computePhase derives the cluster-level phase string from per-node statuses.
func computePhase(anyFailedOver bool, statuses []remediationv1alpha1.NodeCNIStatus) string {
	if anyFailedOver {
		return phaseFailedOver
	}
	for _, s := range statuses {
		if !s.PrimaryHealthy {
			return phaseDegraded
		}
	}
	return phaseHealthy
}
