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

package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/pkg/module"
)

// requeueInterval is how often the reconciler re-runs after a successful reconcile.
const requeueInterval = 30 * time.Second

// moduleEnabledFunc is a function that checks if a module is enabled in the spec.
type moduleEnabledFunc func(spec *remediationv1alpha1.NetworkRemediationSpec) bool

// moduleStatusSetter is a function that sets a module's status in the CR status.
type moduleStatusSetter func(status *remediationv1alpha1.NetworkRemediationStatus, moduleStatus remediationv1alpha1.ModuleStatus)

// registeredModule binds a Module implementation with its enable-check and status-setter.
type registeredModule struct {
	module    module.Module
	isEnabled moduleEnabledFunc
	setStatus moduleStatusSetter
}

// NetworkRemediationReconciler reconciles a NetworkRemediation object.
// It acts as a dispatcher: on each reconcile, it iterates through all registered
// modules and runs the Check → Evaluate → Remediate pipeline for each enabled module.
type NetworkRemediationReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	modules []registeredModule
}

// RegisterModule adds a module to the reconciler's module list.
func (r *NetworkRemediationReconciler) RegisterModule(
	mod module.Module,
	isEnabled moduleEnabledFunc,
	setStatus moduleStatusSetter,
) {
	r.modules = append(r.modules, registeredModule{
		module:    mod,
		isEnabled: isEnabled,
		setStatus: setStatus,
	})
}

// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=networkremediations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=networkremediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=networkremediations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=crd.projectcalico.org,resources=ippools,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=crd.projectcalico.org,resources=ipamblocks,verbs=get;list;watch

// Reconcile runs the Check → Evaluate → Remediate pipeline for each enabled module.
func (r *NetworkRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// 1. Fetch the NetworkRemediation CR
	var nr remediationv1alpha1.NetworkRemediation
	if err := r.Get(ctx, req.NamespacedName, &nr); err != nil {
		// CR was deleted - nothing to do
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling NetworkRemediation", "name", nr.Name)

	// 2. Run each module's pipeline
	overallHealthy := true
	for _, reg := range r.modules {
		if healthy := r.reconcileModule(ctx, &nr, reg); !healthy {
			overallHealthy = false
		}
	}

	// 3. Update overall status
	now := metav1.Now()
	nr.Status.LastReconcileTime = &now
	if overallHealthy {
		nr.Status.Phase = "Running"
	} else {
		nr.Status.Phase = "Degraded"
	}

	if err := r.Status().Update(ctx, &nr); err != nil {
		logger.Error(err, "Failed to update NetworkRemediation status")
		return ctrl.Result{}, err
	}

	// 4. Requeue for periodic reconciliation
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// reconcileModule executes the Check -> Evaluate -> Remediate pipeline for a single module.
// Returns true if the module is healthy (or disabled), false otherwise.
func (r *NetworkRemediationReconciler) reconcileModule(
	ctx context.Context,
	nr *remediationv1alpha1.NetworkRemediation,
	reg registeredModule,
) bool {
	modName := reg.module.Name()
	logger := logf.FromContext(ctx).WithValues("module", modName)

	// Check if this module is enabled
	if !reg.isEnabled(&nr.Spec) {
		logger.V(1).Info("Module is disabled, skipping")
		reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
			Healthy: true,
			Enabled: false,
			Message: "Module is disabled",
		})
		return true
	}

	now := metav1.Now()

	// CHECK
	logger.Info("Running Check phase")
	checkResult, err := reg.module.Check(ctx, &nr.Spec)
	if err != nil {
		logger.Error(err, "Check phase failed")
		reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
			Healthy:     false,
			Enabled:     true,
			Message:     "Check phase error: " + err.Error(),
			LastChecked: &now,
		})
		return false
	}

	// EVALUATE
	logger.Info("Running Evaluate phase")
	evalResult, err := reg.module.Evaluate(ctx, checkResult)
	if err != nil {
		logger.Error(err, "Evaluate phase failed")
		reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
			Healthy:     false,
			Enabled:     true,
			Message:     "Evaluate phase error: " + err.Error(),
			LastChecked: &now,
		})
		return false
	}

	if evalResult.IsHealthy {
		logger.Info("Module is healthy")
		reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
			Healthy:     true,
			Enabled:     true,
			Message:     evalResult.Reason,
			LastChecked: &now,
		})
		return true
	}

	if !evalResult.NeedsRemediation {
		logger.Info("Issue detected but no remediation needed", "reason", evalResult.Reason, "severity", evalResult.Severity)
		reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
			Healthy:     false,
			Enabled:     true,
			Message:     evalResult.Reason,
			LastChecked: &now,
		})
		return false
	}

	// REMEDIATE
	logger.Info("Running Remediate phase", "reason", evalResult.Reason, "severity", evalResult.Severity)
	remediateResult, err := reg.module.Remediate(ctx, evalResult)
	if err != nil {
		logger.Error(err, "Remediate phase failed")
		reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
			Healthy:     false,
			Enabled:     true,
			Message:     "Remediation error: " + err.Error(),
			LastChecked: &now,
		})
		return false
	}

	msg := "Remediated: " + remediateResult.Action
	if !remediateResult.Success {
		msg = "Remediation failed: " + remediateResult.Action
	}
	logger.Info(msg, "success", remediateResult.Success)
	reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
		Healthy:     remediateResult.Success,
		Enabled:     true,
		Message:     msg,
		LastChecked: &now,
	})
	return remediateResult.Success
}

// SetupWithManager sets up the controller with the Manager.
func (r *NetworkRemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1alpha1.NetworkRemediation{}).
		Named("networkremediation").
		Complete(r)
}
