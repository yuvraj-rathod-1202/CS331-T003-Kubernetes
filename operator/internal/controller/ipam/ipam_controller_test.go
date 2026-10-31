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
	"fmt"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam/buddy"
)

const (
	poolName   = "default"
	firstBlock = "10.244.0.0/27"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := remediationv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func node(name string, pods int64, ip string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	n.Status.Allocatable = corev1.ResourceList{corev1.ResourcePods: *resource.NewQuantity(pods, resource.DecimalSI)}
	n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}}
	return n
}

func pool(name string, cidrs ...string) *remediationv1alpha1.IPPool {
	return &remediationv1alpha1.IPPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.Now()},
		Spec:       remediationv1alpha1.IPPoolSpec{CIDRs: cidrs},
	}
}

func fakeReconciler(t *testing.T, objs ...client.Object) (*IPAMController, client.Client) {
	t.Helper()
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&remediationv1alpha1.NodeBlock{}, &remediationv1alpha1.IPPool{}).Build()
	r := NewIPAMController(c, s)
	r.Recorder = record.NewFakeRecorder(100)
	return r, c
}

func runReconcile(t *testing.T, r *IPAMController, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func listBlocks(t *testing.T, c client.Client) []remediationv1alpha1.NodeBlock {
	t.Helper()
	var l remediationv1alpha1.NodeBlockList
	if err := c.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

func blocksOf(items []remediationv1alpha1.NodeBlock, nodeName string) []string {
	var out []string
	for _, b := range items {
		if b.Spec.NodeName == nodeName {
			out = append(out, b.Spec.CIDR)
		}
	}
	slices.Sort(out)
	return out
}

// activate marks every block Active with the given usage, standing in for the
// node agent.
func activate(t *testing.T, c client.Client, used map[string]int) {
	t.Helper()
	for _, b := range listBlocks(t, c) {
		b.Status.Phase = remediationv1alpha1.NodeBlockActive
		b.Status.UsedAddresses = used[b.Spec.NodeName]
		if err := c.Status().Update(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSizing(t *testing.T) {
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{})
	cases := []struct{ pods, want int }{{110, 32}, {10, 16}, {400, 128}, {600, 256}}
	for _, c := range cases {
		if got := initialBlockSize(&spec, c.pods); got != c.want {
			t.Errorf("initialBlockSize(%d) = %d, want %d", c.pods, got, c.want)
		}
	}
	if got := capacityCap(&spec, 110); got != 132 {
		t.Errorf("capacityCap = %d", got)
	}
	if blockName("10.244.0.64/27") != "10-244-0-64-27" {
		t.Errorf("blockName = %s", blockName("10.244.0.64/27"))
	}
	// 29 usable in a /27: grow when free < max(8, 20% of 29 = 5) i.e. free < 8.
	if needsGrowth(&spec, 29, 21) {
		t.Error("free 8 should not trigger")
	}
	if !needsGrowth(&spec, 29, 22) {
		t.Error("free 7 should trigger")
	}
	// A big node uses the percentage: 1000 capacity, trigger below 200 free.
	if needsGrowth(&spec, 1000, 800) || !needsGrowth(&spec, 1000, 801) {
		t.Error("percentage threshold wrong")
	}
	if got := growthTarget(&spec, 29, 103, 16); got != 16 {
		t.Errorf("growthTarget = %d, want 16", got)
	}
	if got := growthTarget(&spec, 252, 200, 16); got != 128 {
		t.Errorf("growthTarget = %d, want 128", got)
	}
	// Capped by headroom: halve until it fits.
	if got := growthTarget(&spec, 252, 40, 16); got != 32 {
		t.Errorf("growthTarget under cap = %d, want 32", got)
	}
	if got := growthTarget(&spec, 252, 5, 16); got != 0 {
		t.Errorf("growthTarget with no headroom = %d, want 0", got)
	}
}

func TestInitialAllocation(t *testing.T) {
	r, c := fakeReconciler(t,
		pool(poolName, "10.244.0.0/22"),
		node("n1", 110, "192.168.58.2"), node("n2", 110, "192.168.58.3"), node("n3", 110, "192.168.58.4"))

	runReconcile(t, r, poolName)

	items := listBlocks(t, c)
	if len(items) != 3 {
		t.Fatalf("want 3 NodeBlocks, got %d", len(items))
	}
	seen := map[string]bool{}
	for _, b := range items {
		if blockAddresses(b.Spec.CIDR) != 32 {
			t.Errorf("%s: want a /27, got %s", b.Spec.NodeName, b.Spec.CIDR)
		}
		if b.Name != blockName(b.Spec.CIDR) || b.Status.Phase != remediationv1alpha1.NodeBlockPending {
			t.Errorf("unexpected block %+v", b)
		}
		if seen[b.Spec.CIDR] {
			t.Errorf("block %s allocated twice", b.Spec.CIDR)
		}
		seen[b.Spec.CIDR] = true
	}
	var p remediationv1alpha1.IPPool
	_ = c.Get(context.Background(), types.NamespacedName{Name: poolName}, &p)
	if p.Status.Phase != phaseReady || p.Status.TotalAddresses != 1024 || p.Status.AllocatedAddresses != 96 {
		t.Errorf("pool status: %+v", p.Status)
	}
	if len(p.Status.NodeAllocations) != 3 {
		t.Errorf("node summaries: %+v", p.Status.NodeAllocations)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/22"), node("n1", 110, "192.168.58.2"))
	runReconcile(t, r, poolName)
	first := blocksOf(listBlocks(t, c), "n1")
	activate(t, c, map[string]int{"n1": 3})
	for range 3 {
		runReconcile(t, r, poolName)
	}
	if got := blocksOf(listBlocks(t, c), "n1"); !reflect.DeepEqual(got, first) {
		t.Fatalf("blocks changed with no demand: %v -> %v", first, got)
	}
}

func TestStateIsRebuiltFromNodeBlocks(t *testing.T) {
	r1, c := fakeReconciler(t, pool(poolName, "10.244.0.0/22"), node("n1", 110, "192.168.58.2"), node("n2", 110, "192.168.58.3"))
	runReconcile(t, r1, poolName)
	before := listBlocks(t, c)

	// A brand-new controller instance, as after a restart, must neither
	// re-allocate nor hand out an address already taken, when a node joins.
	r2 := NewIPAMController(c, newScheme(t))
	if err := c.Create(context.Background(), node("n3", 110, "192.168.58.4")); err != nil {
		t.Fatal(err)
	}
	runReconcile(t, r2, poolName)
	after := listBlocks(t, c)
	if len(after) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(after))
	}
	for _, b := range before {
		if got := blocksOf(after, b.Spec.NodeName); !reflect.DeepEqual(got, []string{b.Spec.CIDR}) {
			t.Errorf("node %s changed: %v", b.Spec.NodeName, got)
		}
	}
	all := map[string]bool{}
	for _, b := range after {
		if all[b.Spec.CIDR] {
			t.Errorf("duplicate %s", b.Spec.CIDR)
		}
		all[b.Spec.CIDR] = true
	}
}

func TestGrowthNeedsActiveBlockAndWaitsForPending(t *testing.T) {
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/22"), node("n1", 110, "192.168.58.2"))
	runReconcile(t, r, poolName)
	// Block is still Pending, even with heavy usage reported: no growth.
	runReconcile(t, r, poolName)
	if n := len(listBlocks(t, c)); n != 1 {
		t.Fatalf("grew while Pending: %d blocks", n)
	}

	activate(t, c, map[string]int{"n1": 25}) // 29 usable, free 4 < 8
	runReconcile(t, r, poolName)
	got := blocksOf(listBlocks(t, c), "n1")
	if len(got) != 2 {
		t.Fatalf("want 2 blocks after growth, got %v", got)
	}

	// The new block is Pending, so a second pass must not grow again.
	runReconcile(t, r, poolName)
	if n := len(blocksOf(listBlocks(t, c), "n1")); n != 2 {
		t.Fatalf("grew twice while the first growth is Pending: %d", n)
	}
}

func TestGrowInPlaceTakesTheBuddy(t *testing.T) {
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/24"), node("n1", 110, "192.168.58.2"))
	runReconcile(t, r, poolName) // n1 gets 10.244.0.0/27, with its buddy left free
	activate(t, c, map[string]int{"n1": 28})
	runReconcile(t, r, poolName)

	// Growth wants 16 addresses. The free buddy is a /27, at most twice that, so
	// the node doubles in place: the halves merge into one /26 range.
	got := blocksOf(listBlocks(t, c), "n1")
	if !reflect.DeepEqual(got, []string{firstBlock, "10.244.0.32/27"}) {
		t.Fatalf("first growth: %v", got)
	}
	agg, _ := buddy.Aggregate(got)
	if !reflect.DeepEqual(agg, []string{"10.244.0.0/26"}) {
		t.Fatalf("blocks should aggregate to one route, got %v", agg)
	}
	// One range pays the 3-address overhead once: 61 usable, not 29 + 29.
	if got := capacityOf([]string{firstBlock, "10.244.0.32/27"}); got != 61 {
		t.Fatalf("capacity of merged buddies = %d, want 61", got)
	}
	if got := capacityOf([]string{firstBlock, "10.244.0.64/27"}); got != 58 {
		t.Fatalf("capacity of non-buddies = %d, want 58", got)
	}
}

func TestInitialBlocksLeaveTheirBuddiesFree(t *testing.T) {
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/24"),
		node("n1", 110, "192.168.58.2"), node("n2", 110, "192.168.58.3"), node("n3", 110, "192.168.58.4"))
	runReconcile(t, r, poolName)
	taken := map[string]bool{}
	for _, b := range listBlocks(t, c) {
		taken[b.Spec.CIDR] = true
	}
	// No node's first block may sit next to another node's: each can still double.
	for cidr := range taken {
		bd, _ := buddyOf(cidr)
		if taken[bd] {
			t.Errorf("%s and its buddy %s are both allocated", cidr, bd)
		}
	}
}

func buddyOf(cidr string) (string, error) {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	ones, _ := n.Mask.Size()
	ip := n.IP.To4()
	v := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	v ^= 1 << (32 - ones)
	return fmt.Sprintf("%d.%d.%d.%d/%d", v>>24, v>>16&255, v>>8&255, v&255, ones), nil
}

func TestGrowthRespectsCapacityCap(t *testing.T) {
	// 20 pods: cap 24 usable, initial block 16 (13 usable). One growth of 16
	// would give 26 > 24, so it halves to nothing and the cap is reported.
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/22"), node("n1", 20, "192.168.58.2"))
	runReconcile(t, r, poolName)
	activate(t, c, map[string]int{"n1": 12})
	runReconcile(t, r, poolName)
	if n := len(blocksOf(listBlocks(t, c), "n1")); n != 1 {
		t.Fatalf("grew past the cap: %d blocks", n)
	}
	var p remediationv1alpha1.IPPool
	_ = c.Get(context.Background(), types.NamespacedName{Name: poolName}, &p)
	if p.Status.Phase != phaseDegraded || !strings.Contains(p.Status.Message, "capacity cap") {
		t.Errorf("status: %+v", p.Status)
	}
}

func TestExhaustionIsReported(t *testing.T) {
	// A /26 holds only two /27s: the third node cannot get a block.
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/26"),
		node("n1", 110, "192.168.58.2"), node("n2", 110, "192.168.58.3"), node("n3", 110, "192.168.58.4"))
	runReconcile(t, r, poolName)
	if n := len(listBlocks(t, c)); n != 2 {
		t.Fatalf("want 2 blocks, got %d", n)
	}
	var p remediationv1alpha1.IPPool
	_ = c.Get(context.Background(), types.NamespacedName{Name: poolName}, &p)
	if p.Status.Phase != phaseExhausted || !strings.Contains(p.Status.Message, "n3") {
		t.Errorf("status: %+v", p.Status)
	}
}

func TestMixedDemandMultiBlock(t *testing.T) {
	// A 600-pod node starts at 256 and, once heavily used, grows in place.
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/22"), node("big", 600, "192.168.58.2"))
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "big"); len(got) != 1 || blockAddresses(got[0]) != 256 {
		t.Fatalf("initial block: %v", got)
	}
	activate(t, c, map[string]int{"big": 240}) // 253 usable
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "big"); len(got) != 2 {
		t.Fatalf("want 2 blocks after growth, got %v", got)
	}
}

func TestOverlappingCIDRsAreRejected(t *testing.T) {
	p := pool(poolName, "192.168.58.0/24", "10.96.0.0/16", "10.244.0.0/22")
	p.Spec.ReservedCIDRs = []string{"10.96.0.0/12"}
	r, c := fakeReconciler(t, p, node("n1", 110, "192.168.58.2"))
	runReconcile(t, r, poolName)

	got := blocksOf(listBlocks(t, c), "n1")
	if len(got) != 1 || !strings.HasPrefix(got[0], "10.244.") {
		t.Fatalf("block must come from the only valid CIDR, got %v", got)
	}
	var st remediationv1alpha1.IPPool
	_ = c.Get(context.Background(), types.NamespacedName{Name: poolName}, &st)
	if st.Status.Phase != phaseDegraded ||
		!strings.Contains(st.Status.Message, "InternalIP") || !strings.Contains(st.Status.Message, "reserved range") {
		t.Errorf("status: %+v", st.Status)
	}
}

func TestSecondPoolIsInactive(t *testing.T) {
	first := pool("a-first", "10.244.0.0/22")
	first.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	r, c := fakeReconciler(t, first, pool("b-second", "10.200.0.0/22"), node("n1", 110, "192.168.58.2"))
	runReconcile(t, r, "b-second")
	if n := len(listBlocks(t, c)); n != 0 {
		t.Fatalf("inactive pool allocated %d blocks", n)
	}
	var st remediationv1alpha1.IPPool
	_ = c.Get(context.Background(), types.NamespacedName{Name: "b-second"}, &st)
	if st.Status.Phase != phaseInactive {
		t.Errorf("status: %+v", st.Status)
	}
	runReconcile(t, r, "a-first")
	if n := len(listBlocks(t, c)); n != 1 {
		t.Fatalf("primary pool should allocate, got %d", n)
	}
}

func TestSpillsToSecondDiscontiguousCIDR(t *testing.T) {
	// First CIDR holds a single /27; the next node must come from the second.
	r, c := fakeReconciler(t, pool(poolName, firstBlock, "10.200.0.0/24"),
		node("n1", 110, "192.168.58.2"), node("n2", 110, "192.168.58.3"))
	runReconcile(t, r, poolName)
	if got := blocksOf(listBlocks(t, c), "n1"); !strings.HasPrefix(got[0], "10.244.0.0/") {
		t.Errorf("n1 = %v", got)
	}
	if got := blocksOf(listBlocks(t, c), "n2"); !strings.HasPrefix(got[0], "10.200.0.") {
		t.Errorf("n2 = %v", got)
	}
}

func TestBlockNameCollisionIsNotAnError(t *testing.T) {
	r, c := fakeReconciler(t, pool(poolName, "10.244.0.0/22"), node("n1", 110, "192.168.58.2"))
	// Another writer already holds the block the controller will pick first,
	// but no NodeBlock says so for this pool (as with a stale cache).
	other := &remediationv1alpha1.NodeBlock{
		ObjectMeta: metav1.ObjectMeta{Name: "10-244-0-0-27"},
		Spec:       remediationv1alpha1.NodeBlockSpec{NodeName: "x", PoolRef: "someone-else", CIDR: firstBlock},
	}
	if err := c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	res := runReconcile(t, r, poolName)
	if res.RequeueAfter != requeueBusy {
		t.Errorf("want a fast requeue after a collision, got %v", res.RequeueAfter)
	}
	runReconcile(t, r, poolName)
	if n := len(blocksOf(listBlocks(t, c), "n1")); n != 0 {
		// The name stays taken, so n1 never gets a duplicate of that block.
		t.Logf("n1 blocks: %d", n)
	}
	for _, b := range listBlocks(t, c) {
		if b.Spec.NodeName == "n1" && b.Spec.CIDR == firstBlock {
			t.Fatal("n1 was given a block that is already named")
		}
	}
}
