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

package ipam

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

var t0 = time.Unix(1_000_000, 0)

// nblock builds a NodeBlock as the agent would leave it: in the given phase
// since `since`, with `used` leases.
func nblock(node, cidr, phase string, used int, since time.Time) remediationv1alpha1.NodeBlock {
	ts := metav1.NewTime(since)
	return remediationv1alpha1.NodeBlock{
		ObjectMeta: metav1.ObjectMeta{Name: blockName(cidr), CreationTimestamp: ts},
		Spec:       remediationv1alpha1.NodeBlockSpec{NodeName: node, PoolRef: poolName, CIDR: cidr},
		Status:     remediationv1alpha1.NodeBlockStatus{Phase: phase, UsedAddresses: used, LastTransitionTime: &ts},
	}
}

func plan(spec remediationv1alpha1.IPPoolSpec, nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock, now time.Time, starved ...string) *poolPlan {
	spec.CIDRs = []string{"10.244.0.0/22"}
	s := withDefaults(spec)
	st := map[string]time.Time{}
	for _, n := range starved {
		st[n] = t0.Add(-time.Second)
	}
	return planPool(&s, nodes, blocks, planInput{Now: now, Starved: st})
}

func nodes1() []corev1.Node { return []corev1.Node{*node("n1", 110, "192.168.58.2")} }

const (
	testNS = "ns"
	b0     = "10.244.0.0/27"  // 29 usable
	b1     = "10.244.0.32/27" // 29 usable
)

func TestShrinkDrainsTheEmptiestBlockAfterCooldown(t *testing.T) {
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", b0, "Active", 10, old),
		nblock("n1", b1, "Active", 2, old),
	}
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	if !reflect.DeepEqual(p.Drain, []string{blockName(b1)}) {
		t.Fatalf("drain = %v, want the emptier block", p.Drain)
	}
}

func TestShrinkHoldsBack(t *testing.T) {
	old := t0.Add(-time.Hour)
	cases := map[string][]remediationv1alpha1.NodeBlock{
		"one block only": {nblock("n1", b0, "Active", 0, old)},
		"too busy to fit in the rest": {
			nblock("n1", b0, "Active", 20, old), nblock("n1", b1, "Active", 5, old)},
		"cooldown not over": {
			nblock("n1", b0, "Active", 1, t0.Add(-20*time.Second)), nblock("n1", b1, "Active", 1, t0.Add(-10*time.Second))},
		"already draining": {
			nblock("n1", b0, "Active", 1, old), func() remediationv1alpha1.NodeBlock {
				b := nblock("n1", b1, "Draining", 1, old)
				b.Spec.Drain = true
				return b
			}()},
	}
	for name, blocks := range cases {
		if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0); len(p.Drain) != 0 || len(p.Undrain) != 0 {
			t.Errorf("%s: drain=%v undrain=%v, want none", name, p.Drain, p.Undrain)
		}
	}
}

func TestShrinkKeepsTheInitialCapacity(t *testing.T) {
	// Initial block for 110 pods is a /27 (29 usable). Two /28 blocks give 26,
	// below that floor, so neither may go.
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", "10.244.0.0/28", "Active", 0, old),
		nblock("n1", "10.244.0.16/28", "Active", 0, old),
	}
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0); len(p.Drain) != 0 {
		t.Fatalf("drained below the initial capacity: %v", p.Drain)
	}
}

func TestShrinkCanBeDisabled(t *testing.T) {
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{nblock("n1", b0, "Active", 0, old), nblock("n1", b1, "Active", 0, old)}
	if p := plan(remediationv1alpha1.IPPoolSpec{DisableShrink: true}, nodes1(), blocks, t0); len(p.Drain) != 0 {
		t.Fatalf("drained although shrink is disabled: %v", p.Drain)
	}
}

func TestGrowthUndoesADrainBeforeAllocating(t *testing.T) {
	old := t0.Add(-time.Hour)
	d := nblock("n1", b1, "Draining", 12, old)
	d.Spec.Drain = true
	// 29 usable, 25 used: needs growth, but a draining block is available.
	blocks := []remediationv1alpha1.NodeBlock{nblock("n1", b0, "Active", 25, old), d}
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	if len(p.New) != 0 || !reflect.DeepEqual(p.Undrain, []string{blockName(b1)}) {
		t.Fatalf("new=%v undrain=%v, want to restore the draining block", p.New, p.Undrain)
	}
}

func TestStarvedNodeGrowsDespiteLowUsage(t *testing.T) {
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{nblock("n1", b0, "Active", 3, old)}
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0); len(p.New) != 0 {
		t.Fatalf("grew without need: %v", p.New)
	}
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0, "n1"); len(p.New) == 0 {
		t.Fatal("a starved node must grow")
	}
}

func TestStarvationIsAnsweredOnceByNewSpace(t *testing.T) {
	// The failure happened at t0-1s; the node's newest block appeared after it.
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", b0, "Active", 3, t0.Add(-time.Hour)),
		nblock("n1", b1, "Active", 0, t0),
	}
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0, "n1"); len(p.New) != 0 {
		t.Fatalf("grew again for a failure the last block already answered: %v", p.New)
	}
}

func TestQuarantinedBlockIsHeldThenDeleted(t *testing.T) {
	q := nblock("n1", b1, "Quarantined", 0, t0.Add(-30*time.Second))
	blocks := []remediationv1alpha1.NodeBlock{nblock("n1", b0, "Active", 3, t0.Add(-time.Hour)), q}

	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	if len(p.Delete) != 0 {
		t.Fatalf("deleted during quarantine: %v", p.Delete)
	}
	// While held, the buddy tree must not hand the block to anyone.
	if got := p.Pool.Find(b1); got == nil || got.State.String() == "free" {
		t.Fatalf("quarantined block was freed: %+v", got)
	}

	p = plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0.Add(31*time.Second))
	if !reflect.DeepEqual(p.Delete, []string{blockName(b1)}) {
		t.Fatalf("delete = %v after quarantine", p.Delete)
	}
}

func TestBlocksOfDeletedNodeAreQuarantined(t *testing.T) {
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("gone", b0, "Active", 5, t0.Add(-time.Hour)),
		nblock("n1", b1, "Active", 3, t0.Add(-time.Hour)),
	}
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	if !reflect.DeepEqual(p.Quarantine, []string{blockName(b0)}) {
		t.Fatalf("quarantine = %v", p.Quarantine)
	}
}

func TestLowWaterAndCIDRUsage(t *testing.T) {
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{"10.244.0.0/26", "10.200.0.0/26"}})
	nodes := nodes1()
	blocks := make([]remediationv1alpha1.NodeBlock, 0, 2)
	blocks = append(blocks, nblock("n1", "10.244.0.0/26", "Active", 0, t0))
	u := cidrUsage(&spec, nodes, blocks)
	if len(u) != 2 || u[0].State != "Full" || u[1].State != "Standby" {
		t.Fatalf("usage: %+v", u)
	}
	if lowWater(&spec, u) {
		t.Fatal("half the space is free, not low")
	}
	blocks = append(blocks, nblock("n1", "10.200.0.0/26", "Active", 0, t0))
	if u = cidrUsage(&spec, nodes, blocks); !lowWater(&spec, u) {
		t.Fatalf("no space left must be low water: %+v", u)
	}
}

// The reconcile loop must carry the plan out.
func TestReconcileDrainsThenQuarantinesThenReleases(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	mk := func(cidr, phase string, used int) *remediationv1alpha1.NodeBlock {
		b := nblock("n1", cidr, phase, used, old.Time)
		return &b
	}
	p := pool(poolName, "10.244.0.0/22")
	r, c := fakeReconciler(t, p, node("n1", 110, "192.168.58.2"), mk(b0, "Active", 4), mk(b1, "Active", 0))
	get := func(cidr string) remediationv1alpha1.NodeBlock {
		var b remediationv1alpha1.NodeBlock
		if err := c.Get(context.Background(), types.NamespacedName{Name: blockName(cidr)}, &b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	runReconcile(t, r, poolName)
	if !get(b1).Spec.Drain || get(b0).Spec.Drain {
		t.Fatal("the empty block should be asked to drain")
	}

	// The agent drains and, with nothing in it, quarantines the block.
	b := get(b1)
	b.Status.Phase = remediationv1alpha1.NodeBlockQuarantined
	now := metav1.NewTime(time.Now())
	b.Status.LastTransitionTime = &now
	if err := c.Status().Update(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, poolName)
	if len(listBlocks(t, c)) != 2 {
		t.Fatal("block released during quarantine")
	}

	// Once the quarantine has passed, the NodeBlock goes and the space is free.
	b = get(b1)
	past := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	b.Status.LastTransitionTime = &past
	if err := c.Status().Update(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "n1"); !reflect.DeepEqual(got, []string{b0}) {
		t.Fatalf("blocks after release: %v", got)
	}
}

func TestStarvedNodeFromEventsGrowsOnReconcile(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	b := nblock("n1", b0, "Active", 2, old.Time)
	ev := &corev1.Event{
		ObjectMeta:    metav1.ObjectMeta{Name: "e1", Namespace: testNS},
		Reason:        sandboxFailedReason,
		Message:       `failed to set up sandbox: plugin type="ptp" failed (add): failed to allocate for range 0: no IP addresses available in range set: 10.244.0.0-10.244.0.30`,
		Source:        corev1.EventSource{Component: "kubelet", Host: "n1"},
		LastTimestamp: metav1.Now(),
	}
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/22"), node("n1", 110, "192.168.58.2"), &b, ev)
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "n1"); len(got) < 2 {
		t.Fatalf("starved node did not grow: %v", got)
	}

}

func TestOldNoIPEventsAreIgnored(t *testing.T) {
	ev := &corev1.Event{
		ObjectMeta:    metav1.ObjectMeta{Name: "e1", Namespace: testNS},
		Reason:        sandboxFailedReason,
		Message:       "no IP addresses available in range set",
		Source:        corev1.EventSource{Host: "n1"},
		LastTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
	}
	other := &corev1.Event{
		ObjectMeta:    metav1.ObjectMeta{Name: "e2", Namespace: testNS},
		Reason:        sandboxFailedReason,
		Message:       "some other failure",
		Source:        corev1.EventSource{Host: "n2"},
		LastTimestamp: metav1.Now(),
	}
	r, _ := fakeReconciler(t, ev, other)
	st, err := r.starvedNodes(context.Background(), time.Now())
	if err != nil || len(st) != 0 {
		t.Fatalf("starved = %v, err = %v; want none", st, err)
	}
}

func TestKubeProxyLocalMode(t *testing.T) {
	conf := map[string]any{"clusterCIDR": "10.244.0.0/16", "mode": "iptables"}
	if !kubeProxyLocalMode(conf) {
		t.Fatal("expected a change")
	}
	if conf["clusterCIDR"] != "" || conf["detectLocalMode"] != "InterfaceNamePrefix" ||
		conf["detectLocal"].(map[string]any)["interfaceNamePrefix"] != "veth" || conf["mode"] != "iptables" {
		t.Fatalf("conf: %v", conf)
	}
	if kubeProxyLocalMode(conf) {
		t.Fatal("second call must be a no-op")
	}
}

func TestEnsureKubeProxyLocalMode(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: kubeProxyNamespace, Name: kubeProxyName},
		Data:       map[string]string{"config.conf": "kind: KubeProxyConfiguration\nclusterCIDR: 10.244.0.0/16\nmode: iptables\n"},
	}
	ds := &appsDS
	r, c := fakeReconciler(t, cm, ds)

	changed, err := EnsureKubeProxyLocalMode(context.Background(), c, r.Client)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var got corev1.ConfigMap
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: kubeProxyNamespace, Name: kubeProxyName}, &got)
	if !strings.Contains(got.Data["config.conf"], "InterfaceNamePrefix") || !strings.Contains(got.Data["config.conf"], "mode: iptables") {
		t.Fatalf("config.conf: %s", got.Data["config.conf"])
	}
	changed, err = EnsureKubeProxyLocalMode(context.Background(), c, r.Client)
	if err != nil || changed {
		t.Fatalf("second run: changed=%v err=%v", changed, err)
	}
}

var appsDS = appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: kubeProxyNamespace, Name: kubeProxyName}}

func TestShrinkDrainsSeveralBlocksButKeepsTheFloor(t *testing.T) {
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", "10.244.0.0/27", "Active", 2, old),
		nblock("n1", "10.244.0.32/27", "Active", 1, old),
		nblock("n1", "10.244.0.64/27", "Active", 0, old),
		nblock("n1", "10.244.0.96/27", "Active", 0, old),
	}
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	// 110 pods: the initial /27 has 29 usable, so at least one /27 must stay, and
	// the 3 pods must fit in what is left.
	if len(p.Drain) != 3 {
		t.Fatalf("drain = %v, want three of the four blocks", p.Drain)
	}
	if slices.Contains(p.Drain, blockName("10.244.0.0/27")) {
		t.Fatalf("the busiest block should be the one that stays: %v", p.Drain)
	}
}
