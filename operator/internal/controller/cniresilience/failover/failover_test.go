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

package failover

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testPod(name, node, ip, net1 string, gated bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{PodIP: ip},
	}
	if net1 != "" {
		pod.Annotations = map[string]string{multusNetworkStatusAnnotation: fmt.Sprintf(`[{"interface":"eth0","ips":["%s"]},{"interface":"net1","ips":["%s"]}]`, ip, net1)}
	}
	if gated {
		pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: ReadinessGate}}
	}
	return pod
}

func endpoint(ip, node string) discoveryv1.Endpoint {
	ready := true
	return discoveryv1.Endpoint{Addresses: []string{ip}, NodeName: &node, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}
}

func setup(t *testing.T) (*Manager, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	app := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "app-1", Namespace: "default", Labels: map[string]string{serviceNameLabel: "app"}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{endpoint("10.0.0.1", "a"), endpoint("10.0.0.2", "b")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(app,
			testPod("app-a", "a", "10.0.0.1", "10.9.0.1", true),
			testPod("app-b", "b", "10.0.0.2", "10.9.0.2", true),
			testPod("coredns", "a", "10.0.0.5", "", false)).
		WithStatusSubresource(&corev1.Pod{}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).Build()
	return NewManager(c), c
}

func gate(t *testing.T, c client.Client, name string) corev1.ConditionStatus {
	t.Helper()
	pod := &corev1.Pod{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, pod); err != nil {
		t.Fatal(err)
	}
	if condition := gateCondition(pod); condition != nil {
		return condition.Status
	}
	return ""
}

func shadowIPs(t *testing.T, c client.Client) map[string][]string {
	t.Helper()
	var list discoveryv1.EndpointSliceList
	if err := c.List(context.Background(), &list, client.MatchingLabels{shadowLabelKey: shadowLabelVal}); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, slice := range list.Items {
		for _, ep := range slice.Endpoints {
			out[slice.Labels[shadowNodeLabelKey]] = append(out[slice.Labels[shadowNodeLabelKey]], ep.Addresses...)
		}
	}
	return out
}

func TestPodsWithoutBackupKeepTheirPlace(t *testing.T) {
	m, c := setup(t)
	if _, err := m.FailoverNode(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if got := gate(t, c, "app-a"); got != corev1.ConditionFalse {
		t.Fatalf("app-a gate=%q", got)
	}
	if got := gate(t, c, "coredns"); got != "" {
		t.Fatalf("coredns was touched: %q", got)
	}
	if got := shadowIPs(t, c)["a"]; len(got) != 1 || got[0] != "10.9.0.1" {
		t.Fatalf("shadow=%v", got)
	}
}

func TestTwoBrokenNodesDoNotTouchEachOther(t *testing.T) {
	m, c := setup(t)
	ctx := context.Background()
	for _, node := range []string{"a", "b"} {
		if _, err := m.FailoverNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	if got := shadowIPs(t, c); len(got["a"]) != 1 || len(got["b"]) != 1 {
		t.Fatalf("shadows=%v", got)
	}
	if err := m.FailbackNode(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	got := shadowIPs(t, c)
	if len(got["a"]) != 0 || len(got["b"]) != 1 || got["b"][0] != "10.9.0.2" {
		t.Fatalf("after failback of a: %v", got)
	}
	if gate(t, c, "app-a") != corev1.ConditionTrue || gate(t, c, "app-b") != corev1.ConditionFalse {
		t.Fatal("wrong gates after failback of a")
	}
}

func TestStateSurvivesOperatorRestart(t *testing.T) {
	m, c := setup(t)
	ctx := context.Background()
	if _, err := m.FailoverNode(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	failedOver, err := NewManager(c).FailedOverNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !failedOver["b"] || failedOver["a"] {
		t.Fatalf("failedOver=%v", failedOver)
	}
}

func TestGateStartsOnButNotDuringFailover(t *testing.T) {
	m, c := setup(t)
	ctx := context.Background()
	if err := m.EnsureGates(ctx, map[string]bool{"b": true}); err != nil {
		t.Fatal(err)
	}
	if gate(t, c, "app-a") != corev1.ConditionTrue || gate(t, c, "app-b") != "" {
		t.Fatal("EnsureGates set the wrong pods")
	}
}

func TestNoBackupIsAnError(t *testing.T) {
	m, _ := setup(t)
	if _, err := m.FailoverNode(context.Background(), "nobody"); err == nil {
		t.Fatal("expected an error")
	}
}

// The lab showed a failed-over node whose backup then died too: its shadow kept
// sending half the traffic to a dead backup address. Isolating must take the node's
// gated pods out (gate off, no shadow), leave other nodes and ungated pods alone,
// and failback must bring the pods back.
func TestIsolateWhenNoPathWorks(t *testing.T) {
	m, c := setup(t)
	ctx := context.Background()
	if _, err := m.FailoverNode(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.FailoverNode(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	n, err := m.IsolateNode(ctx, "a")
	if err != nil || n != 1 {
		t.Fatalf("isolated=%d err=%v, want 1 pod", n, err)
	}
	shadows := shadowIPs(t, c)
	if _, ok := shadows["a"]; ok || len(shadows["b"]) != 1 {
		t.Fatalf("shadows=%v, want only b's", shadows)
	}
	if gate(t, c, "app-a") != corev1.ConditionFalse || gate(t, c, "coredns") != "" {
		t.Fatalf("app-a=%q coredns=%q", gate(t, c, "app-a"), gate(t, c, "coredns"))
	}
	failedOver, err := m.FailedOverNodes(ctx)
	if err != nil || failedOver["a"] || !failedOver["b"] {
		t.Fatalf("failedOver=%v err=%v", failedOver, err)
	}
	if err := m.FailbackNode(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if gate(t, c, "app-a") != corev1.ConditionTrue {
		t.Fatalf("after failback app-a=%q", gate(t, c, "app-a"))
	}
}

// The lab showed the failover count rising every round on a node whose pods are in
// no Service: nothing moved, yet each round counted a failover. Such a node must get
// no shadow and no gate change, and report zero.
func TestNothingToMoveIsNotAFailover(t *testing.T) {
	m, c := setup(t)
	ctx := context.Background()
	if err := c.Create(ctx, testPod("lonely", "c", "10.0.0.3", "10.9.0.3", true)); err != nil {
		t.Fatal(err)
	}
	written, err := m.FailoverNode(ctx, "c")
	if err != nil || written != 0 {
		t.Fatalf("written=%d err=%v, want 0 and no error", written, err)
	}
	if got := gate(t, c, "lonely"); got != "" {
		t.Fatalf("lonely gate=%q, want untouched", got)
	}
	if got := shadowIPs(t, c); len(got) != 0 {
		t.Fatalf("shadows=%v, want none", got)
	}
	if written, err := m.FailoverNode(ctx, "a"); err != nil || written != 1 {
		t.Fatalf("node a: written=%d err=%v, want 1", written, err)
	}
}
