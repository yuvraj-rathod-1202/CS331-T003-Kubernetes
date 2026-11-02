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

package cniresilience

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

func report(peer string, ok bool) map[string]string {
	return map[string]string{healthagent.ReportAnnotation: fmt.Sprintf(
		`{"time":%d,"peers":{"%s":{"primary":%t}}}`, time.Now().Unix(), peer, ok)}
}

func setReport(t *testing.T, c client.Client, node, peer string, ok bool) {
	t.Helper()
	n := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: node}, n); err != nil {
		t.Fatal(err)
	}
	n.Annotations = report(peer, ok)
	if err := c.Update(context.Background(), n); err != nil {
		t.Fatal(err)
	}
}

// A node in recovery whose verdict softens to "link broken" is not repaired yet: the
// lab showed this removing the taint and counting a recovery while traffic still failed.
func TestRepairNeedsProofNotJustNoVerdict(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, remediationv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cr := &remediationv1alpha1.CNIResilience{ObjectMeta: metav1.ObjectMeta{Name: "r"}}
	node := func(name, peer string, ok bool, taints ...corev1.Taint) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: report(peer, ok)},
			Spec:       corev1.NodeSpec{Taints: taints},
		}
	}
	taint := corev1.Taint{Key: "cniresilience.cn-operator/recovering", Effect: corev1.TaintEffectNoSchedule}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cr, node("a", "b", false), node("b", "c", true, taint), node("c", "a", true)).
		WithStatusSubresource(cr).Build()
	r := NewCNIResilienceController(c, scheme)
	r.recoveryPending["b"] = true
	ctx := context.Background()
	request := ctrl.Request{NamespacedName: client.ObjectKey{Name: "r"}}
	check := func(wantTaints, wantCount int) {
		t.Helper()
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
		n, got := &corev1.Node{}, &remediationv1alpha1.CNIResilience{}
		if err := c.Get(ctx, client.ObjectKey{Name: "b"}, n); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, client.ObjectKey{Name: "r"}, got); err != nil {
			t.Fatal(err)
		}
		if len(n.Spec.Taints) != wantTaints || got.Status.RecoveryCount != wantCount {
			t.Fatalf("taints=%d recoveries=%d, want %d and %d", len(n.Spec.Taints), got.Status.RecoveryCount, wantTaints, wantCount)
		}
	}

	check(1, 0)
	setReport(t, c, "a", "b", true)
	check(0, 1)
}

func TestHealthyNeedsProof(t *testing.T) {
	good := &healthagent.NodeHealthStatus{Measured: true, Working: true}
	link := &healthagent.NodeHealthStatus{Measured: true}
	unclear := &healthagent.NodeHealthStatus{Measured: true, Ambiguous: true}
	silent := &healthagent.NodeHealthStatus{}
	for _, tc := range []struct {
		health map[string]*healthagent.NodeHealthStatus
		want   string
	}{
		{map[string]*healthagent.NodeHealthStatus{"a": good, "b": good}, phaseHealthy},
		{map[string]*healthagent.NodeHealthStatus{"a": good, "b": link}, phaseDegraded},
		{map[string]*healthagent.NodeHealthStatus{"a": link, "b": unclear}, phaseUnknown},
		{map[string]*healthagent.NodeHealthStatus{"a": good, "b": silent}, phaseUnknown},
	} {
		if got := computePhase(false, tc.health); got != tc.want {
			t.Fatalf("%+v: got %s, want %s", tc.health, got, tc.want)
		}
	}
}
