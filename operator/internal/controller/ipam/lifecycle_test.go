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

// testPoolCIDR is the pool the planning tests carve blocks from.
const testPoolCIDR = "10.244.0.0/22"

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
	spec.CIDRs = []string{testPoolCIDR}
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
	p := pool(poolName, testPoolCIDR)
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
	// No pod is waiting for an address any more: the failure is history.
	r, c := fakeReconciler(t, pool(poolName, testPoolCIDR), node("n1", 110, "192.168.58.2"), b.DeepCopy(), ev.DeepCopy())
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "n1"); len(got) != 1 {
		t.Fatalf("grew for a failure no pod is waiting on: %v", got)
	}

	// A pod on n1 still has no IP: grow.
	waiting := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "w1", Namespace: testNS},
		Spec:       corev1.PodSpec{NodeName: "n1"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	r, c = fakeReconciler(t, pool(poolName, testPoolCIDR), node("n1", 110, "192.168.58.2"), &b, ev, waiting)
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
	st, err := r.starvedNodes(context.Background(), time.Now(), map[string]int{"n1": 1})
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

// A restart that fails after the ConfigMap was written must be retried on the
// next pass, even though the config itself is already correct by then.
func TestEnsureKubeProxyRetriesFailedRestart(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: kubeProxyNamespace, Name: kubeProxyName},
		Data:       map[string]string{"config.conf": "kind: KubeProxyConfiguration\nclusterCIDR: 10.244.0.0/16\n"},
	}
	r, c := fakeReconciler(t, cm) // no DaemonSet yet: the restart fails
	ctx := context.Background()
	if _, err := EnsureKubeProxyLocalMode(ctx, c, r.Client); err == nil {
		t.Fatal("expected the restart to fail without a DaemonSet")
	}
	ds := appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: kubeProxyNamespace, Name: kubeProxyName}}
	if err := c.Create(ctx, &ds); err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureKubeProxyLocalMode(ctx, c, r.Client)
	if err != nil || !changed {
		t.Fatalf("retry: changed=%v err=%v", changed, err)
	}
	var got appsv1.DaemonSet
	_ = c.Get(ctx, types.NamespacedName{Namespace: kubeProxyNamespace, Name: kubeProxyName}, &got)
	if got.Spec.Template.Annotations[restartAnnotation] == "" {
		t.Fatal("kube-proxy was not restarted on retry")
	}
	if changed, err := EnsureKubeProxyLocalMode(ctx, c, r.Client); err != nil || changed {
		t.Fatalf("third run: changed=%v err=%v", changed, err)
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

func TestStatusHeartbeat(t *testing.T) {
	r, c := fakeReconciler(t, pool(poolName, testPoolCIDR), node("n1", 110, "192.168.58.2"))
	runReconcile(t, r, poolName)
	get := func() *metav1.Time {
		var p remediationv1alpha1.IPPool
		_ = c.Get(context.Background(), types.NamespacedName{Name: poolName}, &p)
		return p.Status.LastReconcileTime
	}
	first := get()
	if first == nil {
		t.Fatal("status has no timestamp")
	}
	// Nothing changed and the timestamp is fresh: no write.
	runReconcile(t, r, poolName)
	if got := get(); !got.Equal(first) {
		t.Fatalf("timestamp rewritten without need: %v -> %v", first, got)
	}
	if heartbeatDue(first, first.Add(10*time.Second)) || !heartbeatDue(first, first.Add(31*time.Second)) {
		t.Fatal("heartbeat should be due after 30s and not before")
	}
	if !heartbeatDue(nil, time.Now()) {
		t.Fatal("a missing timestamp is always due")
	}
}

func TestNoShrinkRightAfterTheNodeGrew(t *testing.T) {
	// A full old block and a new empty one: the node just grew, so nothing drains,
	// even though the old block is past its own cooldown.
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", b0, "Active", 29, t0.Add(-time.Hour)),
		nblock("n1", b1, "Active", 0, t0.Add(-30*time.Second)),
		nblock("n1", "10.244.0.64/26", "Active", 0, t0.Add(-30*time.Second)),
	}
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0); len(p.Drain) != 0 {
		t.Fatalf("drained %v right after the node grew", p.Drain)
	}
	// Once the node has been stable for the cooldown, the emptiest block goes.
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0.Add(10*time.Minute)); len(p.Drain) == 0 {
		t.Fatal("a stable node with spare blocks should shrink")
	}
}

func TestControllerWakesWhenATimerRunsOut(t *testing.T) {
	// No event marks the end of a quarantine or a cooldown, so the controller
	// must requeue for it instead of waiting for its idle interval.
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}, QuarantineSeconds: 10, ShrinkCooldownSeconds: 20})
	A := remediationv1alpha1.NodeBlockActive
	Q := remediationv1alpha1.NodeBlockQuarantined

	cases := []struct {
		name   string
		blocks []remediationv1alpha1.NodeBlock
		want   time.Duration
	}{
		{"one block per node: nothing to wait for",
			[]remediationv1alpha1.NodeBlock{nblock("n1", "10.244.0.0/26", A, 0, t0.Add(-time.Hour))}, 0},
		{"quarantine ends in 4s",
			[]remediationv1alpha1.NodeBlock{nblock("n1", "10.244.0.0/26", A, 0, t0.Add(-time.Hour)), nblock("n1", "10.244.0.64/26", Q, 0, t0.Add(-6*time.Second))}, 4 * time.Second},
		{"cooldown of a two-block node ends in 15s",
			[]remediationv1alpha1.NodeBlock{nblock("n1", "10.244.0.0/26", A, 0, t0.Add(-time.Hour)), nblock("n1", "10.244.0.64/26", A, 0, t0.Add(-5*time.Second))}, 15 * time.Second},
		{"expired timers are not waited for",
			[]remediationv1alpha1.NodeBlock{nblock("n1", "10.244.0.0/26", A, 0, t0.Add(-time.Hour)), nblock("n1", "10.244.0.64/26", A, 0, t0.Add(-time.Minute))}, 0},
	}
	for _, c := range cases {
		if got := nextTimer(&spec, c.blocks, t0); got != c.want {
			t.Errorf("%s: nextTimer = %v, want %v", c.name, got, c.want)
		}
	}

	if got := requeueAfter(4 * time.Second); got != 4*time.Second+timerSlack {
		t.Errorf("requeueAfter(4s) = %v", got)
	}
	if got := requeueAfter(0); got != requeueInterval {
		t.Errorf("requeueAfter(0) = %v, want the idle interval", got)
	}
	if got := requeueAfter(10 * time.Minute); got != requeueInterval {
		t.Errorf("requeueAfter(10m) = %v, want the idle interval", got)
	}
}

// A grown /26 of node n1 and its two halves.
const (
	grownCIDR  = "10.244.0.64/26"
	grownLower = "10.244.0.64/27"
	grownUpper = "10.244.0.96/27"
)

// grown builds an Active block node n1 got after its starting block, with
// per-half usage as the agent reports it.
func grown(cidr string, lower, upper int, since time.Time) remediationv1alpha1.NodeBlock {
	b := nblock("n1", cidr, remediationv1alpha1.NodeBlockActive, lower+upper, since)
	b.Status.HalfUsage = []int{lower, upper}
	return b
}

// starting builds node n1's marked starting block.
func starting(cidr string, used int, since time.Time) remediationv1alpha1.NodeBlock {
	b := nblock("n1", cidr, remediationv1alpha1.NodeBlockActive, used, since)
	b.Spec.Initial = true
	return b
}

func TestShrinkNeverDrainsTheStartingBlock(t *testing.T) {
	// The starting block is the emptiest, but it is never given back; the
	// grown /26 is.
	old := t0.Add(-time.Hour)
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), []remediationv1alpha1.NodeBlock{
		starting("10.244.0.0/27", 0, old),
		grown(grownCIDR, 10, 0, old),
	}, t0)
	if !reflect.DeepEqual(p.Drain, []string{blockName(grownCIDR)}) {
		t.Fatalf("Drain = %v, want only the grown block", p.Drain)
	}
}

func TestSplitGivesBackTheEmptyHalfOfAGrownBlock(t *testing.T) {
	// 25 pods. The grown /26 cannot go whole (29 left for 25 pods), but its
	// upper half is empty: giving it back leaves 58 for 25.
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{
		starting("10.244.0.0/27", 15, old),
		grown(grownCIDR, 10, 0, old),
	}
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	want := []halfRelease{{Name: blockName(grownCIDR), Half: grownUpper}}
	if len(p.Drain) != 0 || !reflect.DeepEqual(p.Release, want) {
		t.Fatalf("Drain = %v, Release = %v, want %v", p.Drain, p.Release, want)
	}

	// Both halves in use: nothing to give back.
	blocks[1] = grown(grownCIDR, 5, 5, old)
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0); len(p.Release) != 0 {
		t.Fatalf("Release = %v with both halves in use", p.Release)
	}

	// Too young: the cooldown applies as to a drain.
	blocks[1] = grown(grownCIDR, 10, 0, t0.Add(-time.Second))
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0); len(p.Release) != 0 {
		t.Fatalf("Release = %v inside the cooldown", p.Release)
	}
}

func TestSplitNeverTouchesTheStartingBlock(t *testing.T) {
	// The starting /25 has an empty upper half and giving it back would fit,
	// but it is the starting block. The grown /24 is busy in both halves.
	old := t0.Add(-time.Hour)
	first := starting("10.244.0.0/25", 5, old)
	first.Status.HalfUsage = []int{5, 0}
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), []remediationv1alpha1.NodeBlock{
		first,
		grown("10.244.1.0/24", 60, 40, old),
	}, t0)
	if len(p.Release) != 0 || len(p.Drain) != 0 {
		t.Fatalf("Release = %v, Drain = %v: the starting block must stay whole", p.Release, p.Drain)
	}
}

func TestStartingBlockWithoutTheMarkIsTheOldest(t *testing.T) {
	// Blocks from before spec.initial existed: the oldest one is the start.
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", grownCIDR, remediationv1alpha1.NodeBlockActive, 0, t0.Add(-time.Minute)),
		nblock("n1", "10.244.0.0/27", remediationv1alpha1.NodeBlockActive, 0, t0.Add(-time.Hour)),
	}
	if got := startingBlock(blocks); got != blockName("10.244.0.0/27") {
		t.Fatalf("startingBlock = %s", got)
	}
	blocks[0].Spec.Initial = true
	if got := startingBlock(blocks); got != blockName(grownCIDR) {
		t.Fatalf("the marked block wins, got %s", got)
	}
}

func TestReleaseIsCompletedOrWithdrawn(t *testing.T) {
	old := t0.Add(-time.Hour)
	rel := grown(grownCIDR, 10, 0, old)
	rel.Spec.Release = grownUpper
	blocks := []remediationv1alpha1.NodeBlock{starting("10.244.0.0/27", 15, old), rel}

	// Not yet reported empty: wait, and plan nothing else for the node.
	p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	if len(p.Split)+len(p.Release)+len(p.Drain)+len(p.Unrelease) != 0 {
		t.Fatalf("waiting for the agent, got %+v", p)
	}

	// The agent reports the half empty: complete the split.
	blocks[1].Status.ReleaseReady = grownUpper
	if p := plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0); !reflect.DeepEqual(p.Split, []string{rel.Name}) {
		t.Fatalf("Split = %v", p.Split)
	}

	// The node fills up before that: keep the half instead of growing.
	blocks[1].Status.ReleaseReady = ""
	blocks[0].Status.UsedAddresses = 29
	blocks[1].Status.UsedAddresses = 29
	p = plan(remediationv1alpha1.IPPoolSpec{}, nodes1(), blocks, t0)
	if !reflect.DeepEqual(p.Unrelease, []string{rel.Name}) || len(p.New) != 0 {
		t.Fatalf("Unrelease = %v, New = %v", p.Unrelease, p.New)
	}
}

func TestReconcileSplitsABlock(t *testing.T) {
	old := metav1.NewTime(t0.Add(-time.Hour))
	first := starting("10.244.0.0/27", 15, old.Time)
	rel := grown(grownCIDR, 10, 0, old.Time)
	rel.Spec.Release = grownUpper
	rel.Status.ReleaseReady = grownUpper
	for _, b := range []*remediationv1alpha1.NodeBlock{&first, &rel} {
		b.Spec.PoolRef = poolName
	}
	r, c := fakeReconciler(t, pool(poolName, testPoolCIDR), node("n1", 110, "192.168.58.2"), &first, &rel)
	runReconcile(t, r, poolName)

	var kept remediationv1alpha1.NodeBlock
	if err := c.Get(context.Background(), types.NamespacedName{Name: rel.Name}, &kept); err != nil {
		t.Fatal(err)
	}
	if kept.Spec.CIDR != grownLower || kept.Spec.Release != "" || kept.Status.ReleaseReady != "" {
		t.Fatalf("kept block: %+v %+v", kept.Spec, kept.Status)
	}
	var half remediationv1alpha1.NodeBlock
	if err := c.Get(context.Background(), types.NamespacedName{Name: blockName(grownUpper)}, &half); err != nil {
		t.Fatalf("released half: %v", err)
	}
	if half.Spec.NodeName != "n1" || !half.Spec.Drain || half.Status.Phase != remediationv1alpha1.NodeBlockQuarantined {
		t.Fatalf("released half: %+v %+v", half.Spec, half.Status)
	}
	// The pool sees both, without overlap.
	if got := blocksOf(listBlocks(t, c), "n1"); !reflect.DeepEqual(got, []string{"10.244.0.0/27", grownLower, grownUpper}) {
		t.Fatalf("blocks: %v", got)
	}
	var p remediationv1alpha1.IPPool
	_ = c.Get(context.Background(), types.NamespacedName{Name: poolName}, &p)
	if strings.Contains(p.Status.Message, "cannot be placed") {
		t.Fatalf("pool status: %s", p.Status.Message)
	}
}

func TestNewStartingBlocksAreMarked(t *testing.T) {
	r, c := fakeReconciler(t, pool(poolName, testPoolCIDR), node("n1", 110, "192.168.58.2"))
	runReconcile(t, r, poolName)
	for _, b := range listBlocks(t, c) {
		if !b.Spec.Initial {
			t.Fatalf("starting block %s not marked", b.Spec.CIDR)
		}
	}
}

func TestFailuresRightAfterAGrowDoNotGrowAgain(t *testing.T) {
	// A block went Active at t0. Pods that read the old config fail one second
	// later; that is not a new shortage. A failure after the grace period is.
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", b0, "Active", 3, t0.Add(-time.Hour)),
		nblock("n1", b1, "Active", 0, t0),
	}
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}})
	late := map[string]time.Time{"n1": t0.Add(time.Second)}
	if p := planPool(&spec, nodes1(), blocks, planInput{Now: t0.Add(2 * time.Second), Starved: late}); len(p.New) != 0 {
		t.Fatalf("grew for failures from before the new block: %v", p.New)
	}
	later := map[string]time.Time{"n1": t0.Add(starveGrace + time.Second)}
	if p := planPool(&spec, nodes1(), blocks, planInput{Now: t0.Add(10 * time.Second), Starved: later}); len(p.New) == 0 {
		t.Fatal("a failure after the grace period must still grow the node")
	}
}

func TestReleasedBlockDoesNotMakeOldFailuresCountAgain(t *testing.T) {
	// Seen live: a node with no pods grew, drained and released the same block
	// eight times. The "no IP" failures from an earlier burst were newer than its
	// one remaining block, so after every release they looked new again.
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	first := starting(b0, 0, old.Time)
	first.Spec.PoolRef = poolName
	rest := nblock("n1", b1, remediationv1alpha1.NodeBlockQuarantined, 0, time.Now().Add(-time.Minute))
	rest.Spec.PoolRef = poolName
	rest.Spec.Drain = true
	ev := &corev1.Event{
		ObjectMeta:    metav1.ObjectMeta{Name: "e1", Namespace: testNS},
		Reason:        sandboxFailedReason,
		Message:       `failed to set up sandbox: plugin type="ptp" failed (add): failed to allocate for range 0: no IP addresses available in range set: 10.244.0.0-10.244.0.30`,
		Source:        corev1.EventSource{Component: "kubelet", Host: "n1"},
		LastTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
	}
	p := pool(poolName, testPoolCIDR)
	p.Spec.QuarantineSeconds = 10
	r, c := fakeReconciler(t, p, node("n1", 110, "192.168.58.2"), &first, &rest, ev)

	// Pass 1 releases the quarantined block and remembers the change.
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "n1"); !reflect.DeepEqual(got, []string{b0}) {
		t.Fatalf("after release: %v", got)
	}
	var st remediationv1alpha1.IPPool
	_ = c.Get(context.Background(), types.NamespacedName{Name: poolName}, &st)
	if len(st.Status.NodeAllocations) != 1 || st.Status.NodeAllocations[0].LastBlockChange == nil {
		t.Fatalf("release not remembered: %+v", st.Status.NodeAllocations)
	}

	// Pass 2: the old failure is older than the release, so no grow.
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "n1"); !reflect.DeepEqual(got, []string{b0}) {
		t.Fatalf("grew again for a failure the release came after: %v", got)
	}
}

func TestGrowthIsSizedToTheShortfall(t *testing.T) {
	// 52 pods on 61 usable addresses (two buddy /27s): short by 4 for the spare
	// room the pool asks for. One /28 is enough; a /27 would be over-allocation.
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{
		starting(b0, 26, old),
		nblock("n1", b1, remediationv1alpha1.NodeBlockActive, 26, old),
	}
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}})
	p := planPool(&spec, nodes1(), blocks, planInput{Now: t0})
	if len(p.New) != 1 || blockAddresses(p.New[0].CIDR) != 16 {
		t.Fatalf("New = %v, want a single /28", p.New)
	}
}

func TestPodsWaitingForAnAddressSizeTheGrow(t *testing.T) {
	// 10 leases but 40 more pods scheduled and waiting: demand 50 needs 63, so
	// 34 more usable addresses: a whole /27 buddy plus a /28, in one step.
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{starting(b0, 10, old)}
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}})
	p := planPool(&spec, nodes1(), blocks, planInput{Now: t0, Waiting: map[string]int{"n1": 40}})
	total := 0
	for _, nb := range p.New {
		total += blockAddresses(nb.CIDR)
	}
	if total != 48 {
		t.Fatalf("New = %v (%d addresses), want 48", p.New, total)
	}

	// The same node with no waiting pods and 10 in use does not grow at all.
	if p := planPool(&spec, nodes1(), blocks, planInput{Now: t0}); len(p.New) != 0 {
		t.Fatalf("grew without demand: %v", p.New)
	}
	// Pods that already hold an address but are not in the lease count yet
	// (the agent reports with a delay) count as demand too.
	if p := planPool(&spec, nodes1(), blocks, planInput{Now: t0, Pods: map[string]int{"n1": 28}}); len(p.New) == 0 {
		t.Fatal("28 pods on 29 addresses must grow the node")
	}
}

func TestAWholeBuddyIsEnoughWhenItMergesIntoTheNeededSize(t *testing.T) {
	// Seen live: 49 pods on a /27 got the neighbouring /27 and then a /28 on top
	// (74 addresses). The two /27s merge into one /26 with 61 usable, which
	// already holds 49 pods with the spare room the pool asks for.
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{starting(b0, 0, old)}
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}})
	p := planPool(&spec, nodes1(), blocks, planInput{Now: t0, Waiting: map[string]int{"n1": 49}})
	if len(p.New) != 1 || p.New[0].CIDR != b1 {
		t.Fatalf("New = %v, want only the buddy %s", p.New, b1)
	}
	if got := capacityOf([]string{b0, p.New[0].CIDR}); got != 61 {
		t.Fatalf("capacity %d, want 61", got)
	}

	// A node with a /26 and 50 pods is one address short: one /28, nothing more.
	blocks = []remediationv1alpha1.NodeBlock{starting("10.244.0.64/26", 0, old)}
	p = planPool(&spec, nodes1(), blocks, planInput{Now: t0, Waiting: map[string]int{"n1": 50}})
	if len(p.New) != 1 || blockAddresses(p.New[0].CIDR) != 16 {
		t.Fatalf("New = %v, want a single /28", p.New)
	}
}

func TestShrinkTrustsTheRealPodCountOverALaggingLeaseCount(t *testing.T) {
	// The agent reports 10 leases, so the node looks like it could give back its
	// second /27. But 40 pods really run there (the report lags): it must not.
	old := t0.Add(-time.Hour)
	blocks := []remediationv1alpha1.NodeBlock{
		starting(b0, 5, old),
		nblock("n1", b1, remediationv1alpha1.NodeBlockActive, 5, old),
	}
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}})
	if p := planPool(&spec, nodes1(), blocks, planInput{Now: t0}); len(p.Drain) != 1 {
		t.Fatalf("with 10 pods the second block should be given back, Drain = %v", p.Drain)
	}
	if p := planPool(&spec, nodes1(), blocks, planInput{Now: t0, Pods: map[string]int{"n1": 40}}); len(p.Drain) != 0 || len(p.Release) != 0 {
		t.Fatalf("40 pods are running: Drain = %v, Release = %v", p.Drain, p.Release)
	}
}
