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
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

const (
	testTaintKey = "cniresilience.cn-operator/recovering"
	calicoNS     = "kube-system"
)

func auditClient(t *testing.T, objs ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, remediationv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&remediationv1alpha1.CNIResilience{}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).Build()
	return c, scheme
}

func shadowFor(node string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "shadow-" + node, Namespace: "default", Labels: map[string]string{
			"cniresilience.cn-operator/shadow": "true", "cniresilience.cn-operator/node": node,
		}},
		AddressType: discoveryv1.AddressTypeIPv4,
	}
}

func reconcileOnce(t *testing.T, r *CNIResilienceController) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "r"}}); err != nil {
		t.Fatal(err)
	}
}

// Deleting the CR fails nodes back and removes taints before the CR goes away.
func TestDeletingTheCRCleansUp(t *testing.T) {
	cr := &remediationv1alpha1.CNIResilience{ObjectMeta: metav1.ObjectMeta{Name: "r"}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"},
		Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: testTaintKey, Effect: corev1.TaintEffectNoSchedule}}}}
	c, scheme := auditClient(t, cr, node, shadowFor("b"))
	ctx := context.Background()
	r := NewCNIResilienceController(c, scheme)

	reconcileOnce(t, r) // adds the finalizer
	got := &remediationv1alpha1.CNIResilience{}
	if err := c.Get(ctx, client.ObjectKey{Name: "r"}, got); err != nil || len(got.Finalizers) != 1 {
		t.Fatalf("finalizer not added: %v %v", got.Finalizers, err)
	}
	if err := c.Delete(ctx, got); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r)

	if err := c.Get(ctx, client.ObjectKey{Name: "r"}, got); !apierrors.IsNotFound(err) {
		t.Fatalf("CR still present: %v", err)
	}
	n := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "b"}, n); err != nil || len(n.Spec.Taints) != 0 {
		t.Fatalf("taints left: %v %v", n.Spec.Taints, err)
	}
	var slices discoveryv1.EndpointSliceList
	if err := c.List(ctx, &slices); err != nil || len(slices.Items) != 0 {
		t.Fatalf("shadows left: %d %v", len(slices.Items), err)
	}
}

// Shadows of a node that no longer exists are removed, so the cluster does not
// stay FailedOver forever.
func TestShadowsOfDeletedNodesAreRemoved(t *testing.T) {
	cr := &remediationv1alpha1.CNIResilience{ObjectMeta: metav1.ObjectMeta{Name: "r"}}
	c, scheme := auditClient(t, cr, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}}, shadowFor("gone"))
	reconcileOnce(t, NewCNIResilienceController(c, scheme))

	got := &remediationv1alpha1.CNIResilience{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "r"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.FailoverActive || got.Status.Phase == phaseFailedOver {
		t.Fatalf("still failed over: %+v", got.Status)
	}
}

// A fault that starts within recoveryBackoff of the last restart gets its restart
// later, instead of none for the whole episode.
func TestRestartInsideCooldownIsPostponedNotSkipped(t *testing.T) {
	cr := &remediationv1alpha1.CNIResilience{
		ObjectMeta: metav1.ObjectMeta{Name: "r"},
		Spec: remediationv1alpha1.CNIResilienceSpec{
			PrimaryCNI: remediationv1alpha1.CNIConfig{Namespace: calicoNS, DaemonSetName: "calico-node"}, AutoRecover: true,
		},
	}
	objs := []client.Object{cr}
	for _, name := range []string{"a", "b", "c"} {
		objs = append(objs, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	objs = append(objs, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "calico-b", Namespace: calicoNS, Labels: map[string]string{"k8s-app": "calico-node"}},
		Spec:       corev1.PodSpec{NodeName: "b"},
	})
	c, scheme := auditClient(t, objs...)
	ctx := context.Background()

	// b is broken: every probe into and out of it fails.
	for name, p := range map[string]string{
		"a": `"b":{"primary":false},"c":{"primary":true}`,
		"b": `"a":{"primary":false},"c":{"primary":false}`,
		"c": `"a":{"primary":true},"b":{"primary":false}`,
	} {
		n := &corev1.Node{}
		if err := c.Get(ctx, client.ObjectKey{Name: name}, n); err != nil {
			t.Fatal(err)
		}
		n.Annotations = map[string]string{healthagent.ReportAnnotation: fmt.Sprintf(`{"time":%d,"peers":{%s}}`, time.Now().Unix(), p)}
		if err := c.Update(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	calicoGone := func() bool {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if err := c.Get(ctx, client.ObjectKey{Namespace: calicoNS, Name: "calico-b"}, &corev1.Pod{}); err != nil {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	r := NewCNIResilienceController(c, scheme)
	r.lastRecoveryAttempt["b"] = time.Now() // a restart just happened
	reconcileOnce(t, r)
	if calicoGone() {
		t.Fatal("restarted inside the cooldown")
	}
	if !r.isRestartDue("b") {
		t.Fatal("restart was not postponed")
	}

	r.mu.Lock()
	r.lastRecoveryAttempt["b"] = time.Now().Add(-2 * recoveryBackoff)
	r.mu.Unlock()
	reconcileOnce(t, r)
	if !calicoGone() {
		t.Fatal("postponed restart never happened")
	}
}
