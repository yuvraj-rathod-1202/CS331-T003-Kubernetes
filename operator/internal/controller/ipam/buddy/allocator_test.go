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

package buddy

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

const (
	cidrA = "10.0.0.0/26"
	cidrB = "10.0.0.0/25"
	cidrC = "10.0.0.64/26"
	cidrD = "10.0.0.128/26"
)

func mustAlloc(t *testing.T, a *BuddyAllocator, n int, node string) *Block {
	t.Helper()
	b, err := a.Allocate(n, node)
	if err != nil {
		t.Fatalf("Allocate(%d): %v", n, err)
	}
	return b
}

func TestNewBuddyAllocator(t *testing.T) {
	if _, err := NewBuddyAllocator("10.244.0.0/16", 28, 22); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, err := NewBuddyAllocator("invalid", 28, 22); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
	if _, err := NewBuddyAllocator("10.244.0.0/16", 22, 28); err == nil {
		t.Fatal("expected error when minPrefixLen < maxPrefixLen")
	}
	if _, err := NewBuddyAllocator("fd00::/64", 68, 64); err == nil {
		t.Fatal("expected error for IPv6")
	}
}

func TestAllocateBasic(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	b := mustAlloc(t, a, 64, "n1")
	if b.State != BlockAllocated || b.PrefixLen != 26 || b.NodeName != "n1" {
		t.Fatalf("unexpected block %+v", b)
	}
	if b.CIDR != cidrA {
		t.Fatalf("lowest address expected, got %s", b.CIDR)
	}
}

func TestAllocateBestFit(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/22", 28, 22)
	if b := mustAlloc(t, a, 100, "A"); b.PrefixLen != 25 {
		t.Fatalf("100 addrs: want /25, got /%d", b.PrefixLen)
	}
	if b := mustAlloc(t, a, 500, "B"); b.PrefixLen != 23 {
		t.Fatalf("500 addrs: want /23, got /%d", b.PrefixLen)
	}
}

func TestAllocatePrefersSmallestFreeBlock(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	x := mustAlloc(t, a, 16, "x") // 10.0.0.0/28
	mustAlloc(t, a, 16, "y")      // 10.0.0.16/28
	if err := a.Free(x); err != nil {
		t.Fatal(err)
	}
	// A free /28 exists, so another /28 must reuse it instead of splitting.
	z := mustAlloc(t, a, 16, "z")
	if z.CIDR != "10.0.0.0/28" {
		t.Fatalf("expected reuse of 10.0.0.0/28, got %s", z.CIDR)
	}
}

func TestAllocateSplitting(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	b1 := mustAlloc(t, a, 128, "a")
	b2 := mustAlloc(t, a, 128, "b")
	if b1.CIDR != cidrB || b2.CIDR != "10.0.0.128/25" {
		t.Fatalf("got %s and %s", b1.CIDR, b2.CIDR)
	}
	if _, err := a.Allocate(16, "c"); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("want ErrNoSpace, got %v", err)
	}
}

func TestAllocateAboveMaxBlock(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/16", 28, 24)
	if _, err := a.Allocate(512, "n"); err == nil {
		t.Fatal("expected error for request above max block")
	}
}

func TestFreeAndMerge(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	b1 := mustAlloc(t, a, 128, "a")
	b2 := mustAlloc(t, a, 128, "b")
	if err := a.Free(b1); err != nil {
		t.Fatal(err)
	}
	if s := a.Stats(); s.FreeBlocksByLevel[25] != 1 || s.FreeAddresses != 128 {
		t.Fatalf("after first free: %+v", s)
	}
	if err := a.Free(b2); err != nil {
		t.Fatal(err)
	}
	s := a.Stats()
	if s.FreeBlocksByLevel[24] != 1 || s.FreeAddresses != 256 || s.AllocatedBlocks != 0 {
		t.Fatalf("buddies did not merge: %+v", s)
	}
	if err := a.Free(b2); err == nil {
		t.Fatal("double free must fail")
	}
}

func TestExhaustion(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/28", 28, 28)
	mustAlloc(t, a, 16, "n")
	if _, err := a.Allocate(16, "m"); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("want ErrNoSpace, got %v", err)
	}
}

func TestDefragmentIsConsistent(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	bs := make([]*Block, 0, 8)
	for range 8 {
		bs = append(bs, mustAlloc(t, a, 16, "n"))
	}
	for _, i := range []int{3, 0, 5, 1, 7, 2, 6, 4} {
		if err := a.Free(bs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if ev := a.Defragment(); len(ev) != 0 {
		t.Fatalf("eager merge should leave nothing to defragment, got %d", len(ev))
	}
	if s := a.Stats(); s.FreeBlocksByLevel[24] != 1 {
		t.Fatalf("tree did not collapse: %+v", s)
	}
}

func TestBlockAlignment(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/16", 28, 20)
	for _, n := range []int{16, 40, 100, 16, 300, 64, 16, 1000} {
		b := mustAlloc(t, a, n, "n")
		if b.base%uint32(b.Addresses()) != 0 {
			t.Fatalf("%s is not aligned to its size", b.CIDR)
		}
	}
}

func TestQuarantine(t *testing.T) {
	now := time.Unix(1000, 0)
	a, _ := NewBuddyAllocator("10.0.0.0/28", 28, 28)
	a.SetClock(func() time.Time { return now })
	a.SetQuarantine(5 * time.Minute)

	b := mustAlloc(t, a, 16, "n")
	if err := a.Free(b); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Allocate(16, "m"); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("quarantined block must not be allocatable, got %v", err)
	}
	if s := a.Stats(); s.QuarantinedAddresses != 16 || s.FreeAddresses != 0 {
		t.Fatalf("stats: %+v", s)
	}
	now = now.Add(6 * time.Minute)
	if got := mustAlloc(t, a, 16, "m"); got.CIDR != b.CIDR {
		t.Fatalf("want %s after quarantine, got %s", b.CIDR, got.CIDR)
	}
}

func TestQuarantineDelaysMerge(t *testing.T) {
	now := time.Unix(0, 0)
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	a.SetClock(func() time.Time { return now })
	b1 := mustAlloc(t, a, 128, "a")
	b2 := mustAlloc(t, a, 128, "b")
	a.SetQuarantine(time.Minute)
	_ = a.Free(b1)
	_ = a.Free(b2)
	if s := a.Stats(); s.FreeAddresses != 0 {
		t.Fatalf("nothing may be free during quarantine: %+v", s)
	}
	now = now.Add(2 * time.Minute)
	if n := a.Sweep(); n != 2 {
		t.Fatalf("Sweep released %d, want 2", n)
	}
	if s := a.Stats(); s.FreeBlocksByLevel[24] != 1 {
		t.Fatalf("expected merge after sweep: %+v", s)
	}
}

func TestAllocateBuddyOf(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	x := mustAlloc(t, a, 64, "n") // 10.0.0.0/26
	y, err := a.AllocateBuddyOf(x, "n")
	if err != nil {
		t.Fatal(err)
	}
	if y.CIDR != cidrC {
		t.Fatalf("buddy = %s", y.CIDR)
	}
	if _, err := a.AllocateBuddyOf(x, "n"); !errors.Is(err, ErrBuddyUnavailable) {
		t.Fatalf("want ErrBuddyUnavailable, got %v", err)
	}
	agg, _ := Aggregate([]string{x.CIDR, y.CIDR})
	if !reflect.DeepEqual(agg, []string{cidrB}) {
		t.Fatalf("aggregate = %v", agg)
	}
}

func TestReserve(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	if _, err := a.Reserve(cidrC, "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve("10.0.0.64/27", "m"); err == nil {
		t.Fatal("reserving inside an allocated block must fail")
	}
	if _, err := a.Reserve(cidrB, "m"); err == nil {
		t.Fatal("reserving a block that contains an allocated one must fail")
	}
	if _, err := a.Reserve("10.1.0.0/26", "m"); err == nil {
		t.Fatal("reserving outside the CIDR must fail")
	}
	// The failed attempts must not have changed the tree.
	if s := a.Stats(); s.AllocatedAddresses != 64 || s.FreeAddresses != 192 {
		t.Fatalf("stats: %+v", s)
	}
	if b := a.Find(cidrC); b == nil || b.NodeName != "n" {
		t.Fatalf("Find = %+v", b)
	}
	// A rebuilt tree must hand out the remaining space without overlap.
	b := mustAlloc(t, a, 64, "x")
	if b.CIDR != cidrA {
		t.Fatalf("got %s", b.CIDR)
	}
}

func TestExternalFragmentation(t *testing.T) {
	a, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	bs := make([]*Block, 0, 16)
	for range 16 {
		bs = append(bs, mustAlloc(t, a, 16, "n"))
	}
	for i := 0; i < 16; i += 2 {
		_ = a.Free(bs[i])
	}
	s := a.Stats()
	if s.FreeAddresses != 128 || s.LargestFreeBlock != 16 {
		t.Fatalf("stats: %+v", s)
	}
	if want := 1 - 16.0/128.0; s.ExternalFragmentation != want {
		t.Fatalf("fragmentation = %v, want %v", s.ExternalFragmentation, want)
	}
}

func TestSplitDemand(t *testing.T) {
	cases := []struct {
		demand int
		want   []int
	}{
		{16, []int{28}},
		{65, []int{26, 28}},      // 80 addresses
		{100, []int{26, 27, 28}}, // 112 addresses
		{300, []int{24, 27, 28}},
		{500, []int{23}},
		{2048, []int{22, 22}}, // capped at the max block
	}
	for _, c := range cases {
		if got := SplitDemand(c.demand, 28, 22); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitDemand(%d) = %v, want %v", c.demand, got, c.want)
		}
	}
}

func TestAggregate(t *testing.T) {
	got, err := Aggregate([]string{cidrC, cidrA, cidrD, "10.0.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{cidrB, cidrD, "10.0.1.0/24"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Cascading merge: four /26 -> one /24.
	got, _ = Aggregate([]string{cidrA, cidrC, cidrD, "10.0.0.192/26"})
	if !reflect.DeepEqual(got, []string{"10.0.0.0/24"}) {
		t.Fatalf("cascade: %v", got)
	}
}

func TestPoolMultiCIDRFillsFirstThenSpills(t *testing.T) {
	p := NewPool()
	if err := p.AddCIDR("10.0.0.0/24", 28, 24); err != nil {
		t.Fatal(err)
	}
	if err := p.AddCIDR("10.200.0.0/24", 28, 24); err != nil {
		t.Fatal(err)
	}
	if err := p.AddCIDR("10.0.0.128/25", 28, 24); err == nil {
		t.Fatal("overlapping CIDR must be rejected")
	}
	b1, _ := p.Allocate(256, "a")
	if b1.CIDR != "10.0.0.0/24" {
		t.Fatalf("first CIDR should fill first, got %s", b1.CIDR)
	}
	b2, err := p.Allocate(128, "b")
	if err != nil || b2.CIDR != "10.200.0.0/25" {
		t.Fatalf("expected spill to second CIDR, got %v %v", b2, err)
	}
	if err := p.Free(b2); err != nil {
		t.Fatal(err)
	}
	if s := p.PoolStats(); s.TotalAddresses != 512 || s.AllocatedAddresses != 256 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestPoolAllocateDemand(t *testing.T) {
	p := NewPool()
	_ = p.AddCIDR("10.0.0.0/22", 28, 22)
	blocks, err := p.AllocateDemand(100, "n")
	if err != nil {
		t.Fatal(err)
	}
	prefixes := make([]int, 0, len(blocks))
	total := 0
	for _, b := range blocks {
		prefixes = append(prefixes, b.PrefixLen)
		total += b.Addresses()
	}
	if !reflect.DeepEqual(prefixes, []int{26, 27, 28}) || total != 112 {
		t.Fatalf("prefixes %v total %d", prefixes, total)
	}
	if got := len(p.GetBlocksForNode("n")); got != 3 {
		t.Fatalf("GetBlocksForNode = %d", got)
	}
}

func TestPoolAllocateDemandRollsBack(t *testing.T) {
	p := NewPool()
	_ = p.AddCIDR(cidrB, 28, 25) // 128 addresses
	if _, err := p.AllocateDemand(200, "n"); err == nil {
		t.Fatal("expected failure")
	}
	if s := p.PoolStats(); s.AllocatedAddresses != 0 || s.FreeAddresses != 128 {
		t.Fatalf("rollback left state behind: %+v", s)
	}
}

func TestPoolAllocateDemandFallsBackToSmallerBlocks(t *testing.T) {
	p := NewPool()
	_ = p.AddCIDR("10.0.0.0/24", 28, 24)
	// Fragment the pool so no free /25 exists but plenty of /26s do.
	a, _ := p.Allocate(64, "x")
	b, _ := p.Allocate(64, "y")
	c, _ := p.Allocate(64, "z")
	d, _ := p.Allocate(64, "w")
	_ = p.Free(a)
	_ = p.Free(c)
	_, _ = b, d
	blocks, err := p.AllocateDemand(128, "n")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].PrefixLen != 26 || blocks[1].PrefixLen != 26 {
		t.Fatalf("want two /26, got %+v", blocks)
	}
}

func TestPoolClampsMaxBlockToSmallCIDRs(t *testing.T) {
	p := NewPool()
	// Default max block is /22, but these CIDRs are smaller than that.
	if err := p.AddCIDR("10.0.0.0/24", 28, 22); err != nil {
		t.Fatal(err)
	}
	if err := p.AddCIDR("10.1.0.0/27", 28, 22); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AllocatePrefix(24); err != nil {
		t.Fatalf("a /24 should fit the first CIDR: %v", err)
	}
	b, err := p.Allocate(32, "n")
	if err != nil || b.CIDR != "10.1.0.0/27" {
		t.Fatalf("expected the /27 CIDR, got %v %v", b, err)
	}
}

func TestAllocateSpreadLeavesBuddiesFree(t *testing.T) {
	a, err := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]*Block, 0, 4)
	for range 4 {
		b, err := a.AllocateSpread(32, "n")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b)
	}
	for _, b := range got {
		if bd := b.Buddy(); bd == nil || bd.State != BlockFree {
			t.Fatalf("%s: buddy %v is not free", b.CIDR, bd)
		}
	}
	// Plain Allocate would have packed them: the second block is the first's buddy.
	p, _ := NewBuddyAllocator("10.0.0.0/24", 28, 24)
	x, _ := p.Allocate(32, "n")
	y, _ := p.Allocate(32, "n")
	if x.Buddy() != y {
		t.Fatalf("Allocate no longer packs buddies: %s %s", x.CIDR, y.CIDR)
	}
}

func TestPoolSpreadKeepsCIDROrder(t *testing.T) {
	p := NewPool()
	for _, c := range []string{"10.0.0.0/26", "10.1.0.0/26"} {
		if err := p.AddCIDR(c, 28, 26); err != nil {
			t.Fatal(err)
		}
	}
	cidrs := make([]string, 0, 3)
	for range 3 {
		b, err := p.AllocateSpread(32, "n")
		if err != nil {
			t.Fatal(err)
		}
		cidrs = append(cidrs, b.CIDR)
	}
	// The first /26 is used up (second /27 takes the first's buddy) before the
	// second CIDR is touched.
	if want := []string{"10.0.0.0/27", "10.0.0.32/27", "10.1.0.0/27"}; !reflect.DeepEqual(cidrs, want) {
		t.Fatalf("blocks %v, want %v", cidrs, want)
	}
}
