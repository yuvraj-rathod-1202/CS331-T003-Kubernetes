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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = remediationv1alpha1.AddToScheme(s)
	return s
}

func healthyCalicoPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: calicoNamespace,
			Name:      name,
			Labels:    map[string]string{calicoLabelKey: calicoLabelValue},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "calico-node",
				Ready:        true,
				RestartCount: 0,
			}},
		},
	}
}

func crashingCalicoPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: calicoNamespace,
			Name:      name,
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
}

func specFor(autoHeal bool) *remediationv1alpha1.NetworkRemediationSpec {
	return &remediationv1alpha1.NetworkRemediationSpec{
		NetworkPolicy: remediationv1alpha1.NetworkPolicySpec{
			Enabled:         true,
			AutoHeal:        autoHeal,
			CooldownSeconds: 60,
		},
	}
}

func TestNetworkPolicyModule_Name(t *testing.T) {
	if New(nil).Name() != moduleName {
		t.Fatalf("expected module name %q, got %q", moduleName, New(nil).Name())
	}
}

func TestNetworkPolicy_AllHealthy(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(
			healthyCalicoPod("calico-node-node1"),
			healthyCalicoPod("calico-node-node2"),
		).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(true))
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

func TestNetworkPolicy_EnforcementAgentUnhealthy_Restarted(t *testing.T) {
	ctx := context.Background()
	crashing := crashingCalicoPod("calico-node-abcde")
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(crashing).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(true))
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
	if !eval.NeedsRemediation {
		t.Fatalf("expected NeedsRemediation=true when autoHeal=true")
	}
	res, err := mod.Remediate(ctx, eval)
	if err != nil || !res.Success {
		t.Fatalf("Remediate failed: err=%v success=%v", err, res.Success)
	}

	var pod corev1.Pod
	err = fc.Get(ctx, types.NamespacedName{Namespace: calicoNamespace, Name: "calico-node-abcde"}, &pod)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected crashing calico-node pod to be deleted, got err=%v", err)
	}
}

func TestNetworkPolicy_EnforcementAgentUnhealthy_AutoHealDisabled(t *testing.T) {
	ctx := context.Background()
	crashing := crashingCalicoPod("calico-node-abcde")
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(crashing).Build()
	mod := New(fc)

	check, err := mod.Check(ctx, specFor(false))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if eval.IsHealthy {
		t.Fatalf("expected unhealthy evaluation")
	}
	if eval.NeedsRemediation {
		t.Fatalf("expected NeedsRemediation=false when autoHeal=false")
	}
}

func TestNetworkPolicy_CooldownActive(t *testing.T) {
	ctx := context.Background()
	pod1 := crashingCalicoPod("calico-node-1")
	pod2 := crashingCalicoPod("calico-node-2")
	fc := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(pod1, pod2).Build()
	mod := New(fc)

	// Set last restart timestamp to just 10 seconds ago (cooldown is 60s)
	mod.lastFelixRestart = time.Now().Add(-10 * time.Second)

	check, err := mod.Check(ctx, specFor(true))
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	eval, err := mod.Evaluate(ctx, check)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	res, err := mod.Remediate(ctx, eval)
	if err != nil {
		t.Fatalf("Remediate failed: %v", err)
	}
	if res.Action != "enforcement-agent restart on cooldown; skipping to avoid thrashing" {
		t.Fatalf("expected cooldown message, got %q", res.Action)
	}

	// Neither pod should have been deleted because of cooldown
	var p corev1.Pod
	if err := fc.Get(ctx, types.NamespacedName{Namespace: calicoNamespace, Name: "calico-node-1"}, &p); err != nil {
		t.Fatalf("expected pod1 to still exist during cooldown: %v", err)
	}
}
