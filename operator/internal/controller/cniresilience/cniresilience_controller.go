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
//   1. Reads health status from the health agent (Deep's component).
//   2. If a primary path is down, triggers recovery (Arpan's component).
//   3. If recovery is needed, triggers failover (Arpan's component).
//   4. Updates the CNIResilience CR status.
//
// This is intentionally a thin orchestration layer — the heavy lifting is
// in the healthagent, recovery, and failover packages.
package cniresilience

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/failover"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/recovery"
)

const (
	requeueInterval = 10 * time.Second
)

// CNIResilienceController reconciles the CNIResilience CR.
type CNIResilienceController struct {
	client.Client
	Scheme *runtime.Scheme

	healthAgent       *healthagent.Agent
	recoveryManager   *recovery.Manager
	failoverManager   *failover.Manager
}

// NewCNIResilienceController creates the controller and wires sub-components.
//
// TODO: Wire the health agent, recovery, and failover managers.
func NewCNIResilienceController(c client.Client, scheme *runtime.Scheme) *CNIResilienceController {
	return &CNIResilienceController{
		Client:          c,
		Scheme:          scheme,
		healthAgent:     healthagent.NewAgent(c),
		recoveryManager: recovery.NewManager(c),
		failoverManager: failover.NewManager(c),
	}
}

// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=cniresiliencies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=cniresiliencies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main loop for CNI resilience monitoring and response.
//
// TODO: Implement the orchestration:
//   1. Fetch the CNIResilience CR.
//   2. Call healthAgent.ProbeAllNodes() to get per-node health status.
//   3. For each node where primaryHealthy == false:
//      a. If autoRecover: call recoveryManager.RecoverNode(node, spec).
//      b. If autoFailover: call failoverManager.FailoverNode(node, spec).
//   4. For each node that was previously failed-over but primary is now healthy:
//      a. Call failoverManager.FailbackNode(node) to switch back.
//   5. Update the CNIResilience CR status with per-node health.
//   6. Requeue.
func (r *CNIResilienceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithName("cniresilience-controller")

	// 1. Fetch the CNIResilience CR
	var cr remediationv1alpha1.CNIResilience
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling CNIResilience", "name", cr.Name)

	// TODO: Steps 2-5 — implement the orchestration pipeline.
	// 2. Probe all nodes
	// 3. Handle failures (recover + failover)
	// 4. Handle recoveries (failback)
	// 5. Update status

	// Update last reconcile time
	now := metav1.Now()
	cr.Status.LastReconcileTime = &now
	if err := r.Status().Update(ctx, &cr); err != nil {
		logger.Error(err, "Failed to update CNIResilience status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CNIResilienceController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1alpha1.CNIResilience{}).
		Named("cniresilience").
		Complete(r)
}
