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

package networkpolicy

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

const (
	testNS       = "default"
	testPolicy   = "deny-all-ingress"
	testPolicyNN = testNS + "/" + testPolicy
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = networkingv1.AddToScheme(s)
	_ = remediationv1alpha1.AddToScheme(s)
	return s
}

// denySpec is a simple deny-all-ingress policy spec.
func denySpec() networkingv1.NetworkPolicySpec {
	return networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
	}
}

// allowSpec is a different spec used to simulate drift.
func allowSpec() networkingv1.NetworkPolicySpec {
	return networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "allowed"}},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
	}
}

func mustJSON(t *testing.T, spec networkingv1.NetworkPolicySpec) string {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("failed to marshal spec: %v", err)
	}
	return string(b)
}

func protectedNP(spec networkingv1.NetworkPolicySpec) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS,
			Name:      testPolicy,
			Labels:    map[string]string{defaultProtectedLabel: labelValueTrue},
		},
		Spec: spec,
	}
}

func baselineCM(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: defaultSnapshotNamespace, Name: defaultSnapshotConfigMap},
		Data:       data,
	}
}

// specFor enables the NetworkPolicy module with the given toggles.
func specFor(autoRestore, verifyEnforcement bool) *remediationv1alpha1.NetworkRemediationSpec {
	return &remediationv1alpha1.NetworkRemediationSpec{
		NetworkPolicy: remediationv1alpha1.NetworkPolicySpec{
			Enabled:           true,
			AutoRestore:       autoRestore,
			VerifyEnforcement: verifyEnforcement,
		},
	}
}

func TestNetworkPolicyModule_Name(t *testing.T) {
	if New(nil).Name() != moduleName {
		t.Fatalf("expected module name %q, got %q", moduleName, New(nil).Name())
	}
}

func TestNetworkPolicy_BaselineCapture(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(protectedNP(denySpec())).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(true, false))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if eval.ActionType != ActionSyncBaseline {
		t.Fatalf("expected action %s, got %s", ActionSyncBaseline, eval.ActionType)
	}
	if _, err := mod.Remediate(ctx, eval); err != nil {
		t.Fatalf("Remediate failed: %v", err)
	}

	var cm corev1.ConfigMap
	if err := fc.Get(ctx, types.NamespacedName{Namespace: defaultSnapshotNamespace, Name: defaultSnapshotConfigMap}, &cm); err != nil {
		t.Fatalf("baseline configmap not created: %v", err)
	}
	if cm.Data[testPolicyNN] != mustJSON(t, denySpec()) {
		t.Fatalf("baseline did not record the protected policy spec, got: %q", cm.Data[testPolicyNN])
	}
}

func TestNetworkPolicy_Healthy(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(
			protectedNP(denySpec()),
			baselineCM(map[string]string{testPolicyNN: mustJSON(t, denySpec())}),
		).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(true, true))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if !eval.IsHealthy || eval.NeedsRemediation {
		t.Fatalf("expected healthy with no remediation, got healthy=%v needs=%v reason=%s",
			eval.IsHealthy, eval.NeedsRemediation, eval.Reason)
	}
}

func TestNetworkPolicy_DeletedPolicy_Recreated(t *testing.T) {
	ctx := context.Background()
	// Baseline knows the policy, but the live policy is gone.
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(baselineCM(map[string]string{testPolicyNN: mustJSON(t, denySpec())})).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(true, false))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if eval.ActionType != ActionRestoreDeleted || !eval.NeedsRemediation {
		t.Fatalf("expected restore_deleted needing remediation, got action=%s needs=%v", eval.ActionType, eval.NeedsRemediation)
	}
	res, err := mod.Remediate(ctx, eval)
	if err != nil || !res.Success {
		t.Fatalf("Remediate failed: err=%v success=%v", err, res.Success)
	}

	var np networkingv1.NetworkPolicy
	if err := fc.Get(ctx, types.NamespacedName{Namespace: testNS, Name: testPolicy}, &np); err != nil {
		t.Fatalf("expected policy to be recreated: %v", err)
	}
	if np.Labels[defaultProtectedLabel] != labelValueTrue {
		t.Fatalf("recreated policy missing protected label")
	}
	if mustJSON(t, np.Spec) != mustJSON(t, denySpec()) {
		t.Fatalf("recreated policy spec does not match baseline")
	}
}

func TestNetworkPolicy_DeletedPolicy_AutoRestoreDisabled(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(baselineCM(map[string]string{testPolicyNN: mustJSON(t, denySpec())})).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(false, false))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if eval.IsHealthy {
		t.Fatalf("expected unhealthy (drift detected)")
	}
	if eval.NeedsRemediation {
		t.Fatalf("expected NeedsRemediation=false when AutoRestore is disabled")
	}
}

func TestNetworkPolicy_DriftedPolicy_Reverted(t *testing.T) {
	ctx := context.Background()
	// Live policy has allowSpec, baseline says it should be denySpec.
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(
			protectedNP(allowSpec()),
			baselineCM(map[string]string{testPolicyNN: mustJSON(t, denySpec())}),
		).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(true, false))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if eval.ActionType != ActionRestoreDrift || !eval.NeedsRemediation {
		t.Fatalf("expected restore_drifted, got action=%s needs=%v", eval.ActionType, eval.NeedsRemediation)
	}
	if _, err := mod.Remediate(ctx, eval); err != nil {
		t.Fatalf("Remediate failed: %v", err)
	}

	var np networkingv1.NetworkPolicy
	if err := fc.Get(ctx, types.NamespacedName{Namespace: testNS, Name: testPolicy}, &np); err != nil {
		t.Fatalf("failed to fetch policy: %v", err)
	}
	if mustJSON(t, np.Spec) != mustJSON(t, denySpec()) {
		t.Fatalf("policy was not reverted to baseline spec")
	}
}

func TestNetworkPolicy_EnforcementAgentUnhealthy_Restarted(t *testing.T) {
	ctx := context.Background()
	crashing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: calicoNamespace,
			Name:      "calico-node-abcde",
			Labels:    map[string]string{calicoLabelKey: calicoLabelValue},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "calico-node",
				Ready:        false,
				RestartCount: 5,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
	// Policy matches baseline so the felix path is reached.
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(
			protectedNP(denySpec()),
			baselineCM(map[string]string{testPolicyNN: mustJSON(t, denySpec())}),
			crashing,
		).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(true, true))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if eval.ActionType != ActionRestartFelix {
		t.Fatalf("expected restart_enforcement_agent, got %s (reason: %s)", eval.ActionType, eval.Reason)
	}
	if _, err := mod.Remediate(ctx, eval); err != nil {
		t.Fatalf("Remediate failed: %v", err)
	}

	var pod corev1.Pod
	err = fc.Get(ctx, types.NamespacedName{Namespace: calicoNamespace, Name: "calico-node-abcde"}, &pod)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected crashing calico-node pod to be deleted, got err=%v", err)
	}
}
