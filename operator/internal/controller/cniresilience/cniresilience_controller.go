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
// On each reconcile it reads the per-node health the agents reported, then:
//   - primary path broken: fail over (if a backup works) and restart the CNI agent;
//   - primary path working again: fail back and remove the recovery taint;
//   - anything unproven: take no action and say so in the status.
//
// The heavy lifting lives in the healthagent, recovery and failover packages.
package cniresilience

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/failover"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/recovery"
)

const (
	requeueInterval = 10 * time.Second

	// phaseHealthy is the overall phase when all nodes are proven healthy.
	phaseHealthy = "Healthy"
	// phaseDegraded is when one or more nodes have a failed primary path.
	phaseDegraded = "Degraded"
	// phaseFailedOver is when at least one node has failed over to the backup path.
	phaseFailedOver = "FailedOver"
	// phaseUnknown is when some node has no agent reports, so nothing is proven.
	phaseUnknown = "Unknown"

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

	recoveryManager *recovery.Manager
	failoverManager *failover.Manager

	// mu guards the maps below; recovery runs in per-node goroutines.
	mu sync.Mutex

	// lastRecoveryAttempt tracks when we last tried recovery per node,
	// to implement the recoveryBackoff cooldown.
	lastRecoveryAttempt map[string]time.Time

	// recoveryPending marks nodes restarted but not yet proven working again.
	recoveryPending map[string]bool
}

// NewCNIResilienceController creates the controller and wires sub-components.
func NewCNIResilienceController(c client.Client, scheme *runtime.Scheme) *CNIResilienceController {
	return &CNIResilienceController{
		Client:              c,
		Scheme:              scheme,
		recoveryManager:     recovery.NewManager(c),
		failoverManager:     failover.NewManager(c),
		lastRecoveryAttempt: make(map[string]time.Time),
		recoveryPending:     make(map[string]bool),
	}
}

// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=cniresiliences,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=cniresiliences/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main control loop for CNI resilience monitoring and response.
func (r *CNIResilienceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithName("cniresilience-controller")

	var cr remediationv1alpha1.CNIResilience
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	original := cr.DeepCopy()

	failedOver, err := r.failoverManager.FailedOverNodes(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.failoverManager.EnsureGates(ctx, failedOver); err != nil {
		return ctrl.Result{}, err
	}

	healthByNode, err := healthagent.ReadNodeHealth(ctx, r.Client, time.Now())
	if err != nil {
		return ctrl.Result{}, err
	}

	previous := make(map[string]remediationv1alpha1.NodeCNIStatus, len(cr.Status.NodeStatuses))
	for _, s := range cr.Status.NodeStatuses {
		previous[s.NodeName] = s
	}

	statuses := make([]remediationv1alpha1.NodeCNIStatus, 0, len(healthByNode))
	for nodeName, health := range healthByNode {
		status := buildNodeStatus(nodeName, health, previous[nodeName])
		switch {
		case !health.Measured:
			// Nothing is proven, so nothing is changed.
			if failedOver[nodeName] {
				status.ActivePath = activePathBackup
			}

		case !health.PrimaryHealthy:
			r.handlePrimaryDown(ctx, &cr, nodeName, health, failedOver, &status)

		case !health.Working:
			// Not proven broken, but not proven working either: keep what is in place.
			if failedOver[nodeName] {
				status.ActivePath = activePathBackup
			}
			if status.Message == "" {
				status.Message = "A peer's probe is failing; this node is not blamed"
			}

		default:
			if failedOver[nodeName] {
				logger.Info("Primary path is back, failing back", "node", nodeName)
				if err := r.failoverManager.FailbackNode(ctx, nodeName); err != nil {
					status.ActivePath = activePathBackup
					status.Message = fmt.Sprintf("Failback failed: %v", err)
					statuses = append(statuses, status)
					continue
				}
				delete(failedOver, nodeName)
			}
			if err := r.recoveryManager.UntaintNode(ctx, nodeName); err != nil {
				logger.Error(err, "Failed to remove recovery taint", "node", nodeName)
			}
			if r.finishRecovery(nodeName) {
				cr.Status.RecoveryCount++
				now := metav1.Now()
				status.LastRecoveryTime = &now
			}
			if status.Message == "" {
				status.Message = "Primary path working"
			}
		}
		statuses = append(statuses, status)
	}

	cr.Status.NodeStatuses = statuses
	cr.Status.FailoverActive = len(failedOver) > 0
	cr.Status.Phase = computePhase(cr.Status.FailoverActive, healthByNode)
	return r.updateStatusAndRequeue(ctx, original, &cr)
}

// handlePrimaryDown taints the node, fails over if a backup works, and restarts the CNI agent.
func (r *CNIResilienceController) handlePrimaryDown(
	ctx context.Context,
	cr *remediationv1alpha1.CNIResilience,
	nodeName string,
	health *healthagent.NodeHealthStatus,
	failedOver map[string]bool,
	status *remediationv1alpha1.NodeCNIStatus,
) {
	logger := logf.FromContext(ctx).WithName("cniresilience-controller")
	backupWorks := health.BackupMeasured && health.BackupHealthy

	switch {
	case failedOver[nodeName]:
		status.ActivePath = activePathBackup
		status.Message = "Primary path down; traffic on backup path"
	case !health.BackupMeasured:
		status.Message = "Primary path down; no backup path exists"
	case !backupWorks:
		status.Message = "Primary and backup paths are both down"
	case !cr.Spec.AutoFailover:
		status.Message = "Primary path down; autoFailover disabled"
	default:
		logger.Info("Primary path down, backup works: failing over", "node", nodeName)
		if _, err := r.failoverManager.FailoverNode(ctx, nodeName); err != nil {
			logger.Error(err, "Failover failed", "node", nodeName)
			status.Message = fmt.Sprintf("Failover failed: %v", err)
		} else {
			failedOver[nodeName] = true
			cr.Status.FailoverCount++
			status.ActivePath = activePathBackup
			status.Message = "Failed over to backup path"
			now := metav1.Now()
			status.LastFailoverTime = &now
		}
	}

	if cr.Spec.AutoRecover {
		// The taint holds for as long as the node is down, cooldown or not.
		if err := r.recoveryManager.TaintNode(ctx, nodeName); err != nil {
			logger.Error(err, "Failed to taint node", "node", nodeName)
		}
	}
	if cr.Spec.AutoRecover && r.claimRecovery(nodeName) {
		// Runs on its own so the loop is not blocked while the CNI pod restarts.
		go func(spec remediationv1alpha1.CNIResilienceSpec) {
			recCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if _, err := r.recoveryManager.RecoverNode(recCtx, nodeName, spec.PrimaryCNI.Namespace, spec.PrimaryCNI.DaemonSetName); err != nil {
				logger.Error(err, "CNI recovery failed", "node", nodeName)
			}
		}(cr.Spec)
	}
}

// claimRecovery returns true if a recovery attempt on this node may start now.
func (r *CNIResilienceController) claimRecovery(nodeName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.lastRecoveryAttempt[nodeName]; ok && time.Since(last) < recoveryBackoff {
		return false
	}
	r.lastRecoveryAttempt[nodeName] = time.Now()
	r.recoveryPending[nodeName] = true
	return true
}

// finishRecovery reports whether a restarted node has now been proven working.
func (r *CNIResilienceController) finishRecovery(nodeName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.recoveryPending[nodeName]
	delete(r.recoveryPending, nodeName)
	return pending
}

// SetupWithManager sets up the controller with the Manager. It reconciles on spec
// changes and agent reports. Status writes must not trigger it, or it would spin.
func (r *CNIResilienceController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1alpha1.CNIResilience{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.allCRs),
			builder.WithPredicates(predicate.AnnotationChangedPredicate{})).
		Named("cniresilience").
		Complete(r)
}

func (r *CNIResilienceController) allCRs(ctx context.Context, _ client.Object) []reconcile.Request {
	var list remediationv1alpha1.CNIResilienceList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

// Helpers

// updateStatusAndRequeue patches the CR status and returns a requeue result. A patch
// is used because the cached copy can lag our own last write, and an Update would
// then fail with a conflict. This controller is the only writer of status.
func (r *CNIResilienceController) updateStatusAndRequeue(
	ctx context.Context,
	original, cr *remediationv1alpha1.CNIResilience,
) (ctrl.Result, error) {
	now := metav1.Now()
	cr.Status.LastReconcileTime = &now
	if err := r.Status().Patch(ctx, cr, client.MergeFrom(original)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// buildNodeStatus converts agent health to the CRD status type. A node with no
// reports is not called healthy; times carry over from the previous status.
func buildNodeStatus(
	nodeName string,
	h *healthagent.NodeHealthStatus,
	previous remediationv1alpha1.NodeCNIStatus,
) remediationv1alpha1.NodeCNIStatus {
	now := metav1.Now()
	return remediationv1alpha1.NodeCNIStatus{
		NodeName:         nodeName,
		PrimaryHealthy:   h.Measured && h.PrimaryHealthy,
		BackupHealthy:    h.BackupMeasured && h.BackupHealthy,
		ActivePath:       activePathPrimary,
		LastProbeTime:    &now,
		LastFailoverTime: previous.LastFailoverTime,
		LastRecoveryTime: previous.LastRecoveryTime,
		Message:          h.Note,
	}
}

// computePhase derives the cluster-level phase. Healthy needs proof: every node's
// probes must work. Without this, a failure too wide to pin on one node read as Healthy.
func computePhase(anyFailedOver bool, health map[string]*healthagent.NodeHealthStatus) string {
	if anyFailedOver {
		return phaseFailedOver
	}
	phase := phaseHealthy
	for _, h := range health {
		if !h.Measured || h.Ambiguous {
			return phaseUnknown
		}
		if !h.Working {
			phase = phaseDegraded
		}
	}
	return phase
}
