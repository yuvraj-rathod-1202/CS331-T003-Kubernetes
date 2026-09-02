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

	remediationv1alpha1 "cn-project-1/operator/api/v1alpha1"
	"cn-project-1/operator/pkg/module"
)

var log = logf.Log.WithName("controller").WithName("networkremediation")

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

// Reconcile runs the Check → Evaluate → Remediate pipeline for each enabled module.
func (r *NetworkRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// 1. Fetch the NetworkRemediation CR
	var nr remediationv1alpha1.NetworkRemediation
	if err := r.Get(ctx, req.NamespacedName, &nr); err != nil {
		// CR was deleted — nothing to do
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling NetworkRemediation", "name", nr.Name)

	// 2. Run each module's pipeline
	overallHealthy := true
	for _, reg := range r.modules {
		modName := reg.module.Name()
		modLogger := logger.WithValues("module", modName)

		// Check if this module is enabled
		if !reg.isEnabled(&nr.Spec) {
			modLogger.V(1).Info("Module is disabled, skipping")
			reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
				Healthy: true,
				Enabled: false,
				Message: "Module is disabled",
			})
			continue
		}

		now := metav1.Now()

		// CHECK
		modLogger.Info("Running Check phase")
		checkResult, err := reg.module.Check(ctx, &nr.Spec)
		if err != nil {
			modLogger.Error(err, "Check phase failed")
			reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
				Healthy:     false,
				Enabled:     true,
				Message:     "Check phase error: " + err.Error(),
				LastChecked: &now,
			})
			overallHealthy = false
			continue
		}

		// EVALUATE
		modLogger.Info("Running Evaluate phase")
		evalResult, err := reg.module.Evaluate(ctx, checkResult)
		if err != nil {
			modLogger.Error(err, "Evaluate phase failed")
			reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
				Healthy:     false,
				Enabled:     true,
				Message:     "Evaluate phase error: " + err.Error(),
				LastChecked: &now,
			})
			overallHealthy = false
			continue
		}

		if evalResult.IsHealthy {
			modLogger.Info("Module is healthy")
			reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
				Healthy:     true,
				Enabled:     true,
				Message:     evalResult.Reason,
				LastChecked: &now,
			})
			continue
		}

		overallHealthy = false

		if !evalResult.NeedsRemediation {
			modLogger.Info("Issue detected but no remediation needed", "reason", evalResult.Reason, "severity", evalResult.Severity)
			reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
				Healthy:     false,
				Enabled:     true,
				Message:     evalResult.Reason,
				LastChecked: &now,
			})
			continue
		}

		// REMEDIATE
		modLogger.Info("Running Remediate phase", "reason", evalResult.Reason, "severity", evalResult.Severity)
		remediateResult, err := reg.module.Remediate(ctx, evalResult)
		if err != nil {
			modLogger.Error(err, "Remediate phase failed")
			reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
				Healthy:     false,
				Enabled:     true,
				Message:     "Remediation error: " + err.Error(),
				LastChecked: &now,
			})
			continue
		}

		msg := "Remediated: " + remediateResult.Action
		if !remediateResult.Success {
			msg = "Remediation failed: " + remediateResult.Action
		}
		modLogger.Info(msg, "success", remediateResult.Success)
		reg.setStatus(&nr.Status, remediationv1alpha1.ModuleStatus{
			Healthy:     remediateResult.Success,
			Enabled:     true,
			Message:     msg,
			LastChecked: &now,
		})
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

// SetupWithManager sets up the controller with the Manager.
func (r *NetworkRemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1alpha1.NetworkRemediation{}).
		Named("networkremediation").
		Complete(r)
}
