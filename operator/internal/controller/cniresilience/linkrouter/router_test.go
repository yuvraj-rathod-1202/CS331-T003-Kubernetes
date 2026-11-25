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

package linkrouter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

var testNow = time.Unix(1_700_000_000, 0)

// reportJSON builds a path-report annotation value written age ago. Each peer
// is "pb" (both paths ok), "p" (primary only), "b" (backup only) or "-" (both down).
func reportJSON(t *testing.T, age time.Duration, peers map[string]string) string {
	t.Helper()
	r := healthagent.Report{Time: testNow.Add(-age).Unix(), Peers: map[string]healthagent.PeerResult{}}
	for name, v := range peers {
		yes, no := true, false
		switch v {
		case "pb":
			r.Peers[name] = healthagent.PeerResult{Primary: true, Backup: &yes}
		case "p":
			r.Peers[name] = healthagent.PeerResult{Primary: true, Backup: &no}
		case "b":
			r.Peers[name] = healthagent.PeerResult{Primary: false, Backup: &yes}
		default:
			r.Peers[name] = healthagent.PeerResult{Primary: false, Backup: &no}
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testNode(name, podCIDR, ip, report string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{}}}
	n.Spec.PodCIDR = podCIDR
	if ip != "" {
		n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}}
	}
	if report != "" {
		n.Annotations[healthagent.ReportAnnotation] = report
	}
	return n
}

func affinity(name, node, cidr, state string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(BlockAffinityGVK)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, node, "spec", "node")
	_ = unstructured.SetNestedField(u.Object, cidr, "spec", "cidr")
	_ = unstructured.SetNestedField(u.Object, state, "spec", "state")
	return u
}

func newTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := remediationv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	// The BlockAffinity CRD has no Go type here, so register it as unstructured.
	s.AddKnownTypeWithName(BlockAffinityGVK, &unstructured.Unstructured{})
	listGVK := BlockAffinityGVK
	listGVK.Kind += "List"
	s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func crObject() *remediationv1alpha1.CNIResilience {
	return &remediationv1alpha1.CNIResilience{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
}

func newRouter(c client.Client, h Host) *Router {
	return &Router{Reader: c, Writer: c, NodeName: "a", Host: h, Config: DefaultConfig()}
}

func published(t *testing.T, c client.Client, node string) Report {
	t.Helper()
	n := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: node}, n); err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal([]byte(n.Annotations[ReportAnnotation]), &r); err != nil {
		t.Fatalf("decoding %q: %v", n.Annotations[ReportAnnotation], err)
	}
	return r
}

func threeNodes(t *testing.T, aReport string, extra ...client.Object) []client.Object {
	objs := []client.Object{
		testNode("a", "10.244.0.0/24", "192.168.1.10", aReport),
		testNode("b", "10.244.1.0/24", "192.168.1.11", reportJSON(t, 0, map[string]string{"a": "pb", "c": "pb"})),
		testNode("c", "10.244.2.0/24", "192.168.1.12", reportJSON(t, 0, map[string]string{"a": "pb", "b": "pb"})),
		affinity("a-blk", "a", "172.16.20.0/26", "confirmed"),
		affinity("b-blk", "b", "172.16.25.128/26", "confirmed"),
		affinity("c-blk", "c", "172.16.40.0/26", "confirmed"),
		crObject(),
	}
	return append(objs, extra...)
}

func TestSyncSteersBrokenPeerOverFlannel(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "b", "c": "pb"})
	c := newTestClient(t, threeNodes(t, aReport)...)
	h := newFakeHost()
	if err := newRouter(c, h).Sync(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	want := Route{Dst: "172.16.25.128/26", Gateway: "10.244.1.0", Device: "flannel.1"}
	if got := h.routes["172.16.25.128/26"]; got != want || len(h.routes) != 1 {
		t.Errorf("routes = %+v, want only %+v", h.routes, want)
	}
	rep := published(t, c, "a")
	if rep.Links["b"].Path != "backup" || rep.Links["c"].Path != "primary" || len(rep.Links) != 2 {
		t.Errorf("links = %+v", rep.Links)
	}
	if rep.Time != testNow.Unix() {
		t.Errorf("time = %d, want %d", rep.Time, testNow.Unix())
	}
}

func TestSyncKeepsRoutesWhenOwnReportStale(t *testing.T) {
	aReport := reportJSON(t, 31*time.Second, map[string]string{"b": "b", "c": "pb"})
	c := newTestClient(t, threeNodes(t, aReport)...)
	h := newFakeHost()
	h.routes["172.16.25.128/26"] = Route{Dst: "172.16.25.128/26", Gateway: "10.244.1.0", Device: "flannel.1"}
	if err := newRouter(c, h).Sync(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 || len(h.routes) != 1 {
		t.Errorf("calls = %v, routes = %v, want untouched", h.calls, h.routes)
	}
	n := &corev1.Node{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "a"}, n)
	if _, ok := n.Annotations[ReportAnnotation]; ok {
		t.Error("published a report while own probe report was stale")
	}
}

func TestSyncRemovesRoutesWhenCRDeleted(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "b", "c": "pb"})
	c := newTestClient(t, threeNodes(t, aReport)...)
	h := newFakeHost()
	r := newRouter(c, h)
	ctx := context.Background()
	if err := r.Sync(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if len(h.routes) != 1 {
		t.Fatalf("routes = %v, want 1", h.routes)
	}
	if err := c.Delete(ctx, crObject()); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(ctx, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(h.routes) != 0 || len(h.rules) != 3 {
		t.Errorf("routes = %v, rules = %v, want no routes and 3 rules", h.routes, h.rules)
	}
	rep := published(t, c, "a")
	if rep.Links["b"].Path != "primary" || rep.Links["c"].Path != "primary" {
		t.Errorf("links = %+v, want all primary", rep.Links)
	}
}

func TestSyncDropsRouteForDeletedPeer(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "b", "c": "pb"})
	c := newTestClient(t, threeNodes(t, aReport)...)
	h := newFakeHost()
	r := newRouter(c, h)
	ctx := context.Background()
	if err := r.Sync(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if len(h.routes) != 1 {
		t.Fatalf("routes = %v, want 1", h.routes)
	}
	if err := c.Delete(ctx, testNode("b", "", "", "")); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(ctx, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(h.routes) != 0 {
		t.Errorf("routes = %v, want none after peer deleted", h.routes)
	}
	if _, ok := published(t, c, "a").Links["b"]; ok {
		t.Error("deleted peer still in published links")
	}
}

func TestSyncSkipsPeerWithoutPodCIDROrBlocks(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "b", "c": "pb", "d": "b"})
	d := testNode("d", "", "192.168.1.13", reportJSON(t, 0, map[string]string{"a": "pb"}))
	c := newTestClient(t, threeNodes(t, aReport, d)...)
	h := newFakeHost()
	if err := newRouter(c, h).Sync(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.routes["172.16.25.128/26"]; !ok || len(h.routes) != 1 {
		t.Errorf("routes = %v, want only b's", h.routes)
	}
}

func TestSyncIgnoresMalformedPeerReport(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "-", "c": "pb"})
	objs := threeNodes(t, aReport)
	objs[2].(*corev1.Node).Annotations[healthagent.ReportAnnotation] = "{"
	c := newTestClient(t, objs...)
	if err := newRouter(c, newFakeHost()).Sync(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if got := published(t, c, "a").Links["b"]; got.Path != "none" {
		t.Errorf("b = %+v, want none (c has no usable report)", got)
	}
}

func TestSyncStickinessSurvivesRestart(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "-", "c": "pb", "c2": "pb"})
	c2 := testNode("c2", "10.244.3.0/24", "192.168.1.13", reportJSON(t, 0, map[string]string{"a": "pb", "b": "pb"}))
	objs := threeNodes(t, aReport, c2)
	old, _ := json.Marshal(Report{Time: testNow.Add(-time.Hour).Unix(), Links: map[string]Choice{
		"b": {Path: "relay", Via: "c2"},
	}})
	objs[0].(*corev1.Node).Annotations[ReportAnnotation] = string(old)
	c := newTestClient(t, objs...)
	if err := newRouter(c, newFakeHost()).Sync(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if got := published(t, c, "a").Links["b"]; got.Path != "relay" || got.Via != "c2" {
		t.Errorf("b = %+v, want relay via c2", got)
	}
}

func TestSyncIgnoresUnconfirmedBlocks(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "b", "c": "pb"})
	c := newTestClient(t, threeNodes(t, aReport, affinity("b-pending", "b", "172.16.99.0/26", "pending"))...)
	h := newFakeHost()
	if err := newRouter(c, h).Sync(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.routes["172.16.99.0/26"]; ok {
		t.Error("route installed for unconfirmed block")
	}
}

func TestSyncPublishesOnlyOnChangeOrHeartbeat(t *testing.T) {
	aReport := reportJSON(t, 0, map[string]string{"b": "b", "c": "pb"})
	c := newTestClient(t, threeNodes(t, aReport)...)
	r := newRouter(c, newFakeHost())
	ctx := context.Background()
	if err := r.Sync(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(ctx, testNow.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := published(t, c, "a").Time; got != testNow.Unix() {
		t.Errorf("time = %d, want unchanged %d", got, testNow.Unix())
	}
	later := testNow.Add(HeartbeatSeconds * time.Second)
	if err := r.Sync(ctx, later); err != nil {
		t.Fatal(err)
	}
	if got := published(t, c, "a").Time; got != later.Unix() {
		t.Errorf("time = %d, want heartbeat %d", got, later.Unix())
	}
}

func TestFlannelAddr(t *testing.T) {
	for in, want := range map[string]string{"10.244.1.0/24": "10.244.1.0", "": "", "junk": ""} {
		if got := FlannelAddr(in); got != want {
			t.Errorf("FlannelAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
