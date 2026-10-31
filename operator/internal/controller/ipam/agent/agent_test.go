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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

const (
	ipA          = "10.244.0.2"
	ipB          = "10.244.0.3"
	phaseActive  = "Active"
	phasePending = "Pending"
	block0       = "10.244.0.0/27"
	block1       = "10.244.0.32/27"
	block2       = "10.244.0.64/27"
)

func mkNode(name, ip string, ready bool) corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}},
		},
	}
}

func mkBlock(node, cidr, phase string, acked ...string) remediationv1alpha1.NodeBlock {
	t := metav1.NewTime(time.Unix(1000, 0))
	return remediationv1alpha1.NodeBlock{
		ObjectMeta: metav1.ObjectMeta{Name: blockNameOf(cidr), CreationTimestamp: t},
		Spec:       remediationv1alpha1.NodeBlockSpec{NodeName: node, PoolRef: "p", CIDR: cidr},
		Status:     remediationv1alpha1.NodeBlockStatus{Phase: phase, RoutesAckedBy: acked, LastTransitionTime: &t},
	}
}

func blockNameOf(cidr string) string {
	out := []byte(cidr)
	for i, c := range out {
		if c == '.' || c == '/' {
			out[i] = '-'
		}
	}
	return string(out)
}

type fakeRoutes struct {
	mu      sync.Mutex
	table   map[Route]bool
	failFor string
}

func (f *fakeRoutes) Managed() ([]Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Route, 0, len(f.table))
	for r := range f.table {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeRoutes) Replace(r Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFor == r.Dst {
		return errors.New("netlink: operation not permitted")
	}
	f.table[r] = true
	return nil
}

func (f *fakeRoutes) Delete(r Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.table, r)
	return nil
}

func (f *fakeRoutes) dsts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.table))
	for r := range f.table {
		out = append(out, r.Dst)
	}
	slices.Sort(out)
	return out
}

func TestDesiredRoutesAggregatesAndSkipsOwn(t *testing.T) {
	nodes := []corev1.Node{mkNode("n1", "192.168.0.1", true), mkNode("n2", "192.168.0.2", true), mkNode("n3", "192.168.0.3", true)}
	blocks := []remediationv1alpha1.NodeBlock{
		mkBlock("n1", block0, phaseActive),
		mkBlock("n2", block1, phaseActive),
		mkBlock("n2", block2, phasePending),
		mkBlock("n2", "10.244.0.96/27", phaseActive), // buddy of block2: aggregates
		mkBlock("n3", "10.244.1.0/24", "Quarantined"),
		mkBlock("n3", "10.244.2.0/24", "Released"),
	}
	got, problems := DesiredRoutes("n1", nodes, blocks)
	want := []Route{{Dst: "10.244.0.32/27", Gateway: "192.168.0.2"}, {Dst: "10.244.0.64/26", Gateway: "192.168.0.2"}}
	if !reflect.DeepEqual(got, want) || len(problems) != 0 {
		t.Fatalf("routes = %v problems = %v", got, problems)
	}
	// n3's blocks are Quarantined or Released: no route. n1's own: none.
	ack := AckableBlocks("n1", blocks)
	if len(ack) != 3 {
		t.Fatalf("ackable = %v", ack)
	}
}

func TestDesiredRoutesNeedsOwnerIP(t *testing.T) {
	blocks := []remediationv1alpha1.NodeBlock{mkBlock("ghost", block1, phaseActive)}
	got, problems := DesiredRoutes("n1", nil, blocks)
	if len(got) != 0 || len(problems) != 1 {
		t.Fatalf("routes = %v problems = %v", got, problems)
	}
}

func TestReadyToPromote(t *testing.T) {
	nodes := []corev1.Node{mkNode("self", "1.1.1.1", true), mkNode("a", "1.1.1.2", true), mkNode("b", "1.1.1.3", true), mkNode("down", "1.1.1.4", false)}
	now := time.Unix(1010, 0)
	timeout := 30 * time.Second
	pend := func(acked ...string) *remediationv1alpha1.NodeBlock {
		b := mkBlock("self", block0, phasePending, acked...)
		return &b
	}
	if ReadyToPromote("self", pend("a"), nodes, now, timeout) {
		t.Error("b has not acked")
	}
	if !ReadyToPromote("self", pend("a", "b"), nodes, now, timeout) {
		t.Error("all Ready nodes acked; a NotReady node must not block")
	}
	if !ReadyToPromote("self", pend(), nodes, time.Unix(1031, 0), timeout) {
		t.Error("after the timeout the block is promoted anyway")
	}
	if !ReadyToPromote("self", pend(), []corev1.Node{mkNode("self", "1.1.1.1", true)}, now, timeout) {
		t.Error("a single-node cluster needs no acks")
	}
}

func TestRenderCNIConfig(t *testing.T) {
	b, err := RenderCNIConfig("buddy", "/var/lib/cni/networks", []string{block2, block0})
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Name    string `json:"name"`
		Plugins []struct {
			Type   string `json:"type"`
			IPMasq *bool  `json:"ipMasq"`
			IPAM   struct {
				Type   string `json:"type"`
				Ranges [][]struct {
					Subnet string `json:"subnet"`
				} `json:"ranges"`
			} `json:"ipam"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	p := c.Plugins[0]
	if p.Type != "ptp" || p.IPAM.Type != "host-local" || p.IPMasq == nil || *p.IPMasq {
		t.Fatalf("plugin: %+v", p)
	}
	// One inner array holding every block, in address order.
	if len(p.IPAM.Ranges) != 1 || len(p.IPAM.Ranges[0]) != 2 ||
		p.IPAM.Ranges[0][0].Subnet != block0 || p.IPAM.Ranges[0][1].Subnet != block2 {
		t.Fatalf("ranges: %+v", p.IPAM.Ranges)
	}
	if _, err := RenderCNIConfig("x", "/d", nil); err == nil {
		t.Error("no blocks must be an error")
	}
	if _, err := RenderCNIConfig("x", "/d", []string{"nope"}); err == nil {
		t.Error("bad CIDR must be an error")
	}
}

func TestCountLeases(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{ipA, ipB, "10.244.0.40", "10.244.0.70", "10.9.9.9", "last_reserved_ip.0", "lock"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("id"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := CountLeases(dir, []string{block0, block1, block2})
	if err != nil {
		t.Fatal(err)
	}
	if got[block0] != 2 || got[block1] != 1 || got[block2] != 1 {
		t.Fatalf("counts: %v", got)
	}
	// A missing directory (no pod ever ran) means zero, not an error.
	if got, err := CountLeases(filepath.Join(dir, "nope"), []string{block0}); err != nil || got[block0] != 0 {
		t.Fatalf("missing dir: %v %v", got, err)
	}
}

func TestWriteFileIfChanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "net.d", "10-buddy.conflist")
	if ch, err := WriteFileIfChanged(p, []byte("a")); err != nil || !ch {
		t.Fatalf("first write: %v %v", ch, err)
	}
	if ch, err := WriteFileIfChanged(p, []byte("a")); err != nil || ch {
		t.Fatalf("same content must not rewrite: %v %v", ch, err)
	}
	if ch, err := WriteFileIfChanged(p, []byte("b")); err != nil || !ch {
		t.Fatalf("changed content: %v %v", ch, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

type env struct {
	t      *testing.T
	c      client.Client
	routes *fakeRoutes
	conf   string
	leases string
	now    time.Time
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = remediationv1alpha1.AddToScheme(s)
	dir := t.TempDir()
	return &env{
		t:      t,
		c:      fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&remediationv1alpha1.NodeBlock{}).Build(),
		routes: &fakeRoutes{table: map[Route]bool{}},
		conf:   filepath.Join(dir, "net.d", "10-buddy.conflist"),
		leases: filepath.Join(dir, "networks", "buddy"),
		now:    time.Unix(1010, 0),
	}
}

func (e *env) agent(node string) *Agent {
	return &Agent{
		Client: e.c, NodeName: node, Routes: e.routes, ConfPath: e.conf, NetName: "buddy",
		DataDir: filepath.Dir(e.leases), LeaseDir: e.leases, PromoteTimeout: 30 * time.Second,
		Now: func() time.Time { return e.now },
	}
}

func (e *env) block(cidr string) remediationv1alpha1.NodeBlock {
	var b remediationv1alpha1.NodeBlock
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: blockNameOf(cidr)}, &b); err != nil {
		e.t.Fatal(err)
	}
	return b
}

func obj(b remediationv1alpha1.NodeBlock) *remediationv1alpha1.NodeBlock { return &b }
func nobj(n corev1.Node) *corev1.Node                                    { return &n }

func TestSyncPromotesOnlyAfterRoutesAreAcked(t *testing.T) {
	// n1 owns a Pending block. n2 is Ready and has not acked yet.
	e := newEnv(t,
		nobj(mkNode("n1", "192.168.0.1", true)), nobj(mkNode("n2", "192.168.0.2", true)),
		obj(mkBlock("n1", block0, phasePending)))

	if err := e.agent("n1").Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.block(block0).Status.Phase; got != phasePending {
		t.Fatalf("promoted before n2 acked: %s", got)
	}
	if _, err := os.Stat(e.conf); err == nil {
		t.Fatal("CNI config written before the routes were acknowledged")
	}

	// n2's agent installs the route and acks.
	if err := e.agent("n2").Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.block(block0).Status.RoutesAckedBy; !reflect.DeepEqual(got, []string{"n2"}) {
		t.Fatalf("acks = %v", got)
	}

	if err := e.agent("n1").Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.block(block0).Status.Phase; got != phaseActive {
		t.Fatalf("phase = %s, want Active", got)
	}
	if _, err := os.Stat(e.conf); err != nil {
		t.Fatalf("CNI config missing: %v", err)
	}
}

func TestSyncPromotesAfterTimeoutWhenNodeIsSilent(t *testing.T) {
	e := newEnv(t,
		nobj(mkNode("n1", "192.168.0.1", true)), nobj(mkNode("n2", "192.168.0.2", true)),
		obj(mkBlock("n1", block0, phasePending)))
	a := e.agent("n1")
	_ = a.Sync(context.Background())
	if e.block(block0).Status.Phase != phasePending {
		t.Fatal("must wait for n2 first")
	}
	e.now = time.Unix(1000, 0).Add(31 * time.Second)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.block(block0).Status.Phase; got != phaseActive {
		t.Fatalf("phase = %s, want Active after the timeout", got)
	}
}

func TestRouteFailureBlocksPromotion(t *testing.T) {
	e := newEnv(t,
		nobj(mkNode("n1", "192.168.0.1", true)), nobj(mkNode("n2", "192.168.0.2", true)),
		obj(mkBlock("n1", block0, phasePending)), obj(mkBlock("n2", block1, phaseActive)))
	e.routes.failFor = block1
	err := e.agent("n1").Sync(context.Background())
	if err == nil {
		t.Fatal("a route failure must be reported")
	}
	if got := e.block(block0).Status.Phase; got != phasePending {
		t.Fatalf("promoted with a missing route: %s", got)
	}
	if len(e.block(block1).Status.RoutesAckedBy) != 0 {
		t.Fatal("acked a route that was never installed")
	}
}

func TestSyncRemovesStaleRoutesAndRestoresAfterReboot(t *testing.T) {
	e := newEnv(t,
		nobj(mkNode("n1", "192.168.0.1", true)), nobj(mkNode("n2", "192.168.0.2", true)),
		obj(mkBlock("n2", block1, phaseActive)))
	a := e.agent("n1")
	// A leftover route from a block that no longer exists.
	e.routes.table[Route{Dst: "10.99.0.0/24", Gateway: "192.168.0.9"}] = true
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.routes.dsts(); !reflect.DeepEqual(got, []string{block1}) {
		t.Fatalf("routes = %v", got)
	}
	// A reboot loses every kernel route; the next pass restores them.
	e.routes.table = map[Route]bool{}
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.routes.dsts(); !reflect.DeepEqual(got, []string{block1}) {
		t.Fatalf("routes after reboot = %v", got)
	}
}

func TestSyncReportsLeaseUsageAndKeepsConfigStable(t *testing.T) {
	e := newEnv(t, nobj(mkNode("n1", "192.168.0.1", true)),
		obj(mkBlock("n1", block0, phaseActive)), obj(mkBlock("n1", block1, phaseActive)))
	if err := os.MkdirAll(e.leases, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{ipA, ipB, "10.244.0.40"} {
		_ = os.WriteFile(filepath.Join(e.leases, f), []byte("x"), 0o600)
	}
	a := e.agent("n1")
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.block(block0).Status.UsedAddresses != 2 || e.block(block1).Status.UsedAddresses != 1 {
		t.Fatalf("usage: %d %d", e.block(block0).Status.UsedAddresses, e.block(block1).Status.UsedAddresses)
	}
	st, _ := os.Stat(e.conf)
	first := st.ModTime()
	time.Sleep(10 * time.Millisecond)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(e.conf)
	if !st2.ModTime().Equal(first) {
		t.Fatal("config rewritten although nothing changed")
	}
}

func drainBlock(cidr, phase string) *remediationv1alpha1.NodeBlock {
	b := mkBlock("n1", cidr, phase)
	b.Spec.Drain = true
	return &b
}

func confRanges(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Plugins []struct {
			IPAM struct {
				Ranges [][]struct {
					Subnet string `json:"subnet"`
				} `json:"ranges"`
			} `json:"ipam"`
		} `json:"plugins"`
	}
	_ = json.Unmarshal(raw, &c)
	ranges := c.Plugins[0].IPAM.Ranges[0]
	out := make([]string, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, r.Subnet)
	}
	return out
}

func TestSyncKeepsDrainingAndPendingOutOfConfig(t *testing.T) {
	e := newEnv(t, nobj(mkNode("n1", "192.168.0.1", true)), nobj(mkNode("n2", "192.168.0.2", true)),
		obj(mkBlock("n1", block0, phaseActive)),
		drainBlock(block1, "Draining"),
		obj(mkBlock("n1", block2, phasePending)))
	if err := e.agent("n1").Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := confRanges(t, e.conf); !reflect.DeepEqual(r, []string{block0}) {
		t.Fatalf("only the Active block belongs in the config, got %v", r)
	}
}

func TestDrainLifecycle(t *testing.T) {
	e := newEnv(t, nobj(mkNode("n1", "192.168.0.1", true)),
		obj(mkBlock("n1", block0, phaseActive)),
		drainBlock(block1, phaseActive))
	if err := os.MkdirAll(e.leases, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.leases, "10.244.0.40"), []byte("x"), 0o600)
	a := e.agent("n1")
	a.DrainSettle = 5 * time.Second

	// Active + drain: out of the config, phase Draining, still busy.
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := confRanges(t, e.conf); !reflect.DeepEqual(r, []string{block0}) {
		t.Fatalf("config: %v", r)
	}
	if got := e.block(block1).Status.Phase; got != "Draining" {
		t.Fatalf("phase %s, want Draining", got)
	}

	// The last pod leaves, but the settle time has not passed: stay Draining.
	_ = os.Remove(filepath.Join(e.leases, "10.244.0.40"))
	e.now = e.now.Add(time.Second)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.block(block1).Status.Phase; got != "Draining" {
		t.Fatalf("phase %s before settle, want Draining", got)
	}

	e.now = e.now.Add(10 * time.Second)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.block(block1).Status.Phase; got != "Quarantined" {
		t.Fatalf("phase %s, want Quarantined", got)
	}
}

func TestDrainIsUndoneWhenControllerWithdrawsIt(t *testing.T) {
	b := mkBlock("n1", block1, "Draining") // spec.drain is false again
	e := newEnv(t, nobj(mkNode("n1", "192.168.0.1", true)), obj(mkBlock("n1", block0, phaseActive)), obj(b))
	if err := e.agent("n1").Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// block0 and block1 are buddies, so the restored block merges into one range.
	if r := confRanges(t, e.conf); !reflect.DeepEqual(r, []string{"10.244.0.0/26"}) {
		t.Fatalf("config: %v", r)
	}
	if got := e.block(block1).Status.Phase; got != phaseActive {
		t.Fatalf("phase %s, want Active", got)
	}
}

func TestReclaimLeases(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(100000, 0)
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write(ipA, time.Hour)            // held by a pod
	write(ipB, time.Hour)            // leaked
	write("10.244.0.4", time.Second) // too young to judge
	write("10.244.9.9", time.Hour)   // not in an owned block
	write("last_reserved_ip.0", time.Hour)

	got, err := ReclaimLeases(dir, []string{block0}, map[string]bool{ipA: true}, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]int{block0: 1}) {
		t.Fatalf("reclaimed %v", got)
	}
	for name, want := range map[string]bool{ipA: true, ipB: false, "10.244.0.4": true, "10.244.9.9": true, "last_reserved_ip.0": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s present=%v, want %v", name, err == nil, want)
		}
	}
}

func TestSyncReclaimsLeakedLeaseAndCountsIt(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = remediationv1alpha1.AddToScheme(s)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "n1"},
		Status:     corev1.PodStatus{PodIP: ipA},
	}
	node := mkNode("n1", "192.168.0.1", true)
	blk := mkBlock("n1", block0, phaseActive)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod, &node, &blk).
		WithStatusSubresource(&remediationv1alpha1.NodeBlock{}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).Build()
	e := newEnv(t)
	e.c = c
	if err := os.MkdirAll(e.leases, 0o755); err != nil {
		t.Fatal(err)
	}
	old := e.now.Add(-time.Hour)
	for _, ip := range []string{ipA, ipB} {
		p := filepath.Join(e.leases, ip)
		_ = os.WriteFile(p, []byte("x"), 0o600)
		_ = os.Chtimes(p, old, old)
	}
	a := e.agent("n1")
	a.ReclaimLeaks, a.LeakMinAge = true, time.Minute
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := e.block(block0).Status
	if st.UsedAddresses != 1 || st.ReclaimedLeases != 1 {
		t.Fatalf("used=%d reclaimed=%d, want 1 and 1", st.UsedAddresses, st.ReclaimedLeases)
	}
}

func TestRunSyncsOnTriggerWithoutWaitingForTheInterval(t *testing.T) {
	e := newEnv(t, nobj(mkNode("n1", "192.168.0.1", true)), nobj(mkNode("n2", "192.168.0.2", true)),
		obj(mkBlock("n2", block1, "Active")))
	a := e.agent("n1")
	ctx := t.Context()
	trigger := make(chan struct{}, 1)
	go a.Run(ctx, time.Hour, trigger)

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for range 200 {
			if ok() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the first pass", func() bool { return slices.Contains(e.routes.dsts(), block1) })

	// A new block for n2 appears; only the trigger can make the agent notice.
	if err := e.c.Create(context.Background(), obj(mkBlock("n2", block2, "Active"))); err != nil {
		t.Fatal(err)
	}
	trigger <- struct{}{}
	waitFor("the pass after the trigger", func() bool { return slices.Contains(e.routes.dsts(), block2) })
}

func TestRenderMergesBuddiesIntoOneRange(t *testing.T) {
	raw, err := RenderCNIConfig("buddy", "/var/lib/cni/networks", []string{block1, block0, block2})
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Plugins []struct {
			IPAM struct {
				Ranges [][]struct {
					Subnet string `json:"subnet"`
				} `json:"ranges"`
			} `json:"ipam"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	rs := c.Plugins[0].IPAM.Ranges[0]
	got := make([]string, 0, len(rs))
	for _, r := range rs {
		got = append(got, r.Subnet)
	}
	// block0 + block1 are buddies; block2 stands alone.
	if want := []string{"10.244.0.0/26", block2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ranges = %v, want %v", got, want)
	}
}
