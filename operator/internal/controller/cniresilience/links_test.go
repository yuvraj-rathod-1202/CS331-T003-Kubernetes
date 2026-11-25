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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/linkrouter"
)

// linkAnn builds a link-routes annotation; each entry is "peer=path" or "peer=relay:via".
func linkAnn(at time.Time, entries ...string) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		peer, rest, _ := strings.Cut(e, "=")
		path, via, _ := strings.Cut(rest, ":")
		parts = append(parts, fmt.Sprintf(`"%s":{"path":"%s","via":"%s"}`, peer, path, via))
	}
	return fmt.Sprintf(`{"time":%d,"links":{%s}}`, at.Unix(), strings.Join(parts, ","))
}

func linkNode(name, ann string) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: name, Annotations: map[string]string{linkrouter.ReportAnnotation: ann},
	}}
}

func TestLinkStatusesListsOnlyNonPrimary(t *testing.T) {
	now := time.Now()
	nodes := []corev1.Node{
		linkNode("b", linkAnn(now, "a=relay:c")),
		linkNode("a", linkAnn(now, "b=backup", "c=primary")),
	}
	links, _, _ := linkStatuses(nodes, now, nil)
	if len(links) != 2 {
		t.Fatalf("got %d links: %+v", len(links), links)
	}
	if links[0].From != "a" || links[0].To != "b" || links[0].Path != "backup" {
		t.Errorf("first = %+v", links[0])
	}
	if links[1].From != "b" || links[1].To != "a" || links[1].Path != "relay" || links[1].Via != "c" {
		t.Errorf("second = %+v", links[1])
	}
}

func TestLinkStatusesTransitions(t *testing.T) {
	now := time.Now()
	prev := []remediationv1alpha1.LinkStatus{{From: "a", To: "b", Path: "none"}}
	nodes := []corev1.Node{linkNode("a", linkAnn(now, "b=backup", "c=none"))}
	_, entered, left := linkStatuses(nodes, now, prev)
	if len(entered) != 1 || entered[0] != [2]string{"a", "c"} {
		t.Errorf("entered = %v", entered)
	}
	if len(left) != 1 || left[0] != [2]string{"a", "b"} {
		t.Errorf("left = %v", left)
	}
}

func TestLinkStatusesKeepsPreviousForStaleNode(t *testing.T) {
	now := time.Now()
	prev := []remediationv1alpha1.LinkStatus{{From: "a", To: "b", Path: "backup"}}
	nodes := []corev1.Node{linkNode("a", linkAnn(now.Add(-31*time.Second), "b=primary"))}
	links, entered, left := linkStatuses(nodes, now, prev)
	if len(links) != 1 || links[0].Path != "backup" {
		t.Errorf("links = %+v", links)
	}
	if len(entered) != 0 || len(left) != 0 {
		t.Errorf("entered = %v left = %v", entered, left)
	}
}

func TestLinkStatusesCarriesSince(t *testing.T) {
	now := time.Now()
	since := metav1.NewTime(now.Add(-time.Hour))
	prev := []remediationv1alpha1.LinkStatus{{From: "a", To: "b", Path: "backup", Since: &since}}
	nodes := []corev1.Node{linkNode("a", linkAnn(now, "b=backup"))}
	links, _, _ := linkStatuses(nodes, now, prev)
	if len(links) != 1 || links[0].Since == nil || !links[0].Since.Equal(&since) {
		t.Errorf("links = %+v", links)
	}
}

func linkTestSetup(t *testing.T, linkAnns map[string]string) (client.Client, *CNIResilienceController, *record.FakeRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, remediationv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cr := &remediationv1alpha1.CNIResilience{ObjectMeta: metav1.ObjectMeta{Name: "r"}}
	peers := map[string]string{"a": "b", "b": "c", "c": "a"}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		})
	for name, peer := range peers {
		ann := report(peer, true)
		if l, ok := linkAnns[name]; ok {
			ann[linkrouter.ReportAnnotation] = l
		}
		builder = builder.WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: ann}})
	}
	c := builder.Build()
	recorder := record.NewFakeRecorder(10)
	return c, NewCNIResilienceController(c, scheme, recorder), recorder
}

func setLinkAnn(t *testing.T, c client.Client, node, ann string) {
	t.Helper()
	n := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: node}, n); err != nil {
		t.Fatal(err)
	}
	n.Annotations[linkrouter.ReportAnnotation] = ann
	if err := c.Update(context.Background(), n); err != nil {
		t.Fatal(err)
	}
}

func drain(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestReconcileSetsConditionAndEvents(t *testing.T) {
	ctx := context.Background()
	c, r, rec := linkTestSetup(t, map[string]string{"a": linkAnn(time.Now(), "b=none")})
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: "r"}}
	get := func() remediationv1alpha1.CNIResilience {
		var cr remediationv1alpha1.CNIResilience
		if err := c.Get(ctx, req.NamespacedName, &cr); err != nil {
			t.Fatal(err)
		}
		return cr
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(get().Status.Conditions, remediationv1alpha1.ConditionLinksReachable)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "NoPath" || !strings.Contains(cond.Message, "a→b") {
		t.Fatalf("condition = %+v", cond)
	}
	if ev := drain(rec); len(ev) != 1 || ev[0] != "Warning NoPath No path from a to b" {
		t.Fatalf("events = %v", ev)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if ev := drain(rec); len(ev) != 0 {
		t.Fatalf("unchanged reconcile emitted %v", ev)
	}

	setLinkAnn(t, c, "a", linkAnn(time.Now(), "b=backup"))
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	cond = meta.FindStatusCondition(get().Status.Conditions, remediationv1alpha1.ConditionLinksReachable)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "AllLinksReachable" {
		t.Fatalf("condition = %+v", cond)
	}
	if ev := drain(rec); len(ev) != 1 || ev[0] != "Normal PathRestored Path from a to b restored" {
		t.Fatalf("events = %v", ev)
	}
}

func TestReconcileToleratesMalformedLinkReport(t *testing.T) {
	_, r, _ := linkTestSetup(t, map[string]string{"b": "{"})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "r"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPhaseDegradedOnNoPath(t *testing.T) {
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: "r"}}
	phase := func(ann string) string {
		c, r, _ := linkTestSetup(t, map[string]string{"a": ann})
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		var cr remediationv1alpha1.CNIResilience
		if err := c.Get(ctx, req.NamespacedName, &cr); err != nil {
			t.Fatal(err)
		}
		return cr.Status.Phase
	}
	if got := phase(linkAnn(time.Now(), "b=none")); got != "Degraded" {
		t.Errorf("none link: phase = %s", got)
	}
	if got := phase(linkAnn(time.Now(), "b=backup", "c=relay:b")); got != "Healthy" {
		t.Errorf("backup/relay: phase = %s", got)
	}
}
