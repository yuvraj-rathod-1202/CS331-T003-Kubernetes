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
	"fmt"
	"math/bits"
	"net"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam/buddy"
)

// Why a block was planned.
const (
	reasonInitial     = "initial"
	reasonGrow        = "grow"
	reasonGrowInPlace = "grow-in-place"
	// reasonGrowBeside: a piece of the node's free buddy, because the whole
	// buddy is more than the node needs.
	reasonGrowBeside = "grow-beside"
)

// plannedBlock is a block the controller decided to give a node.
type plannedBlock struct {
	NodeName string
	CIDR     string
	Reason   string
}

// poolPlan is the outcome of one planning pass over a pool.
type poolPlan struct {
	// Pool is the buddy forest rebuilt from the existing NodeBlocks plus the
	// planned blocks.
	Pool *buddy.Pool
	// New lists the blocks to create.
	New []plannedBlock
	// Problems are conditions a human should look at (bad CIDRs, exhaustion).
	Problems []string
	// Exhausted is true when a node needed space the pool could not give.
	Exhausted bool
	// Pending is true while some node has a block that is not yet Active.
	Pending bool
	// Drain, Undrain, Quarantine and Delete name NodeBlocks to act on.
	// Quarantine is for blocks whose node is gone; the agent does it otherwise.
	Drain, Undrain, Quarantine, Delete []string
	// Release starts giving back one empty half of a block (a split);
	// Unrelease withdraws that because the node needs the space; Split
	// completes it once the agent reports the half empty.
	Release          []halfRelease
	Unrelease, Split []string
	// CIDRs is the per-CIDR usage for the pool status.
	CIDRs []remediationv1alpha1.CIDRUsage
}

// halfRelease asks the owner of block Name to stop using Half.
type halfRelease struct {
	Name string
	Half string
}

// withDefaults fills zero values with the documented defaults, so planning
// behaves the same whether or not the API server applied them.
func withDefaults(in remediationv1alpha1.IPPoolSpec) remediationv1alpha1.IPPoolSpec {
	s := in
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&s.MinBlockSize, 28)
	def(&s.MaxBlockSize, 22)
	def(&s.ExpandThresholdPercent, 80)
	def(&s.TriggerFloor, 8)
	def(&s.CapacityCapPercent, 120)
	def(&s.InitialBlockPercent, 25)
	def(&s.ShrinkThresholdPercent, 50)
	def(&s.ShrinkCooldownSeconds, 300)
	def(&s.QuarantineSeconds, 60)
	def(&s.LowWaterPercent, 10)
	return s
}

func allocatablePods(n *corev1.Node) int {
	if q, ok := n.Status.Allocatable[corev1.ResourcePods]; ok && q.Value() > 0 {
		return int(q.Value())
	}
	return defaultAllocatablePods
}

// blockPhase treats a missing phase as Pending: the controller creates the
// object and then sets the phase, and a crash in between must not matter.
func blockPhase(b *remediationv1alpha1.NodeBlock) string {
	if b.Status.Phase == "" {
		return remediationv1alpha1.NodeBlockPending
	}
	return b.Status.Phase
}

func blockAddresses(cidr string) int {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0
	}
	ones, width := n.Mask.Size()
	return 1 << (width - ones)
}

// validCIDRs returns the pool CIDRs that may be used, in order, and a problem
// for each one that may not: unparseable, overlapping a reserved range, or
// containing a node's InternalIP (which would silently black-hole traffic).
func validCIDRs(spec *remediationv1alpha1.IPPoolSpec, nodes []corev1.Node) (ok, problems []string) {
	var reserved []*net.IPNet
	for _, r := range spec.ReservedCIDRs {
		if _, n, err := net.ParseCIDR(r); err == nil {
			reserved = append(reserved, n)
		}
	}
	for _, c := range spec.CIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			problems = append(problems, fmt.Sprintf("CIDR %q is invalid: %v", c, err))
			continue
		}
		bad := ""
		for _, r := range reserved {
			if r.Contains(n.IP) || n.Contains(r.IP) {
				bad = fmt.Sprintf("CIDR %s overlaps reserved range %s", c, r)
				break
			}
		}
		if bad == "" {
			for i := range nodes {
				for _, a := range nodes[i].Status.Addresses {
					if a.Type != corev1.NodeInternalIP {
						continue
					}
					if ip := net.ParseIP(a.Address); ip != nil && n.Contains(ip) {
						bad = fmt.Sprintf("CIDR %s contains the InternalIP %s of node %s", c, a.Address, nodes[i].Name)
					}
				}
			}
		}
		if bad != "" {
			problems = append(problems, bad)
			continue
		}
		ok = append(ok, c)
	}
	return ok, problems
}

// buildPool rebuilds the buddy forest from scratch: the pool CIDRs, minus every
// block that a NodeBlock says is taken. The controller keeps no other state.
func buildPool(spec *remediationv1alpha1.IPPoolSpec, nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock) (*buddy.Pool, []string) {
	p := buddy.NewPool()
	cidrs, problems := validCIDRs(spec, nodes)
	for _, c := range cidrs {
		if err := p.AddCIDR(c, spec.MinBlockSize, spec.MaxBlockSize); err != nil {
			problems = append(problems, err.Error())
		}
	}
	sorted := append([]remediationv1alpha1.NodeBlock(nil), blocks...)
	slices.SortFunc(sorted, func(a, b remediationv1alpha1.NodeBlock) int { return strings.Compare(a.Spec.CIDR, b.Spec.CIDR) })
	for i := range sorted {
		b := &sorted[i]
		if blockPhase(b) == remediationv1alpha1.NodeBlockReleased {
			continue
		}
		if _, err := p.Reserve(b.Spec.CIDR, b.Spec.NodeName); err != nil {
			problems = append(problems, fmt.Sprintf("NodeBlock %s (%s) cannot be placed in the pool: %v", b.Name, b.Spec.CIDR, err))
		}
	}
	return p, problems
}

// planInput is everything planning needs besides the pool spec and the objects.
type planInput struct {
	Now time.Time
	// Starved maps a node to the time of its latest "no IP addresses available"
	// failure. A failure only counts if it is newer than the node's latest block
	// change: space added since then is already the answer to it.
	Starved map[string]time.Time
	// LastChange maps a node to when the controller last changed its blocks
	// (from the pool status). It covers changes that leave no NodeBlock behind:
	// after a block is released, the failures from before must not count again.
	LastChange map[string]time.Time
	// Waiting maps a node to how many pods scheduled to it have no address yet,
	// and Pods to how many pods on it hold or need one. They size a grow to the
	// demand actually there, and cover usage numbers that lag. Both may be nil.
	Waiting, Pods map[string]int
}

// planPool decides which blocks to create, drain or delete. It never changes the
// API. It takes the clock and the set of starved nodes. Per node, in this order: initial block; wait while a change is in flight; grow (first by
// undoing a drain, otherwise by a new block); or shrink by draining one block.
// Quarantined blocks are deleted once their quarantine has passed.
func planPool(spec *remediationv1alpha1.IPPoolSpec, nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock, in planInput) *poolPlan {
	p, problems := buildPool(spec, nodes, blocks)
	plan := &poolPlan{Pool: p, Problems: problems}

	exists := map[string]bool{}
	for i := range nodes {
		exists[nodes[i].Name] = true
	}
	quarantine := time.Duration(spec.QuarantineSeconds) * time.Second

	byNode := map[string][]remediationv1alpha1.NodeBlock{}
	for _, b := range blocks {
		ph := blockPhase(&b)
		if ph == remediationv1alpha1.NodeBlockReleased {
			continue
		}
		if ph == remediationv1alpha1.NodeBlockQuarantined {
			if in.Now.Sub(transitionTime(&b)) >= quarantine {
				plan.Delete = append(plan.Delete, b.Name)
			}
		} else if !exists[b.Spec.NodeName] {
			// The owner is gone: nobody will drain this block, so the controller
			// withdraws it itself. Its pods went away with the node.
			plan.Quarantine = append(plan.Quarantine, b.Name)
		}
		byNode[b.Spec.NodeName] = append(byNode[b.Spec.NodeName], b)
	}

	sorted := append([]corev1.Node(nil), nodes...)
	slices.SortFunc(sorted, func(a, b corev1.Node) int { return strings.Compare(a.Name, b.Name) })
	minAddrs := 1 << (32 - spec.MinBlockSize)

	for i := range sorted {
		n := &sorted[i]
		if !n.DeletionTimestamp.IsZero() {
			continue
		}
		owned := byNode[n.Name]
		pods := allocatablePods(n)

		if len(owned) == 0 {
			size := initialBlockSize(spec, pods)
			b, err := p.AllocateSpread(size, n.Name)
			if err != nil {
				plan.Exhausted = true
				plan.Problems = append(plan.Problems, fmt.Sprintf("no initial block for node %s: %v", n.Name, err))
				continue
			}
			plan.New = append(plan.New, plannedBlock{NodeName: n.Name, CIDR: b.CIDR, Reason: reasonInitial})
			plan.Pending = true
			continue
		}

		// Only one change in flight per node: wait for Pending blocks and for a
		// drain that is being undone before judging capacity.
		st := classify(p, owned)
		used, active, activeBlocks, draining, releasing := st.used, st.active, st.activeBlocks, st.draining, st.releasing
		inFlight := st.inFlight
		if inFlight {
			plan.Pending = true
			continue
		}
		// A split whose released half the agent reports empty is completed
		// first; it changes the node, so nothing else is judged this pass.
		if plan.completeSplits(releasing) {
			continue
		}
		capacity := blocksCapacity(activeBlocks)

		// Demand: leases plus pods still waiting for an address, or the pods
		// themselves when the lease count lags behind them.
		demand := max(used+in.Waiting[n.Name], in.Pods[n.Name])
		if needsGrowth(spec, capacity, demand) || starved(in.Starved[n.Name], owned, in.LastChange[n.Name]) {
			// A half being given back is still the node's: keep it instead of
			// asking the pool for more.
			if len(releasing) > 0 {
				for _, b := range releasing {
					plan.Unrelease = append(plan.Unrelease, b.Name)
				}
				continue
			}
			// A block that is still draining holds addresses the node already
			// has: take it back before asking the pool for more.
			if len(draining) > 0 {
				slices.SortFunc(draining, func(a, b *remediationv1alpha1.NodeBlock) int {
					return b.Status.UsedAddresses - a.Status.UsedAddresses
				})
				plan.Undrain = append(plan.Undrain, draining[0].Name)
				plan.Pending = true
				continue
			}
			plan.growNode(spec, p, n.Name, pods, capacity, demand, minAddrs, active)
			continue
		}

		// One release at a time per node: wait for it before shrinking more.
		if !spec.DisableShrink && len(releasing) == 0 &&
			in.Now.Sub(latestChange(owned)) >= time.Duration(spec.ShrinkCooldownSeconds)*time.Second {
			plan.shrinkNode(spec, owned, pods, used, in.Pods[n.Name], activeBlocks, draining, minAddrs, in.Now)
		}
	}

	plan.CIDRs = cidrUsage(spec, nodes, blocks)
	if lowWater(spec, plan.CIDRs) && !plan.Exhausted {
		plan.Problems = append(plan.Problems, fmt.Sprintf("pool is low on free addresses (below %d%%)", spec.LowWaterPercent))
	}
	slices.Sort(plan.Delete)
	slices.Sort(plan.Quarantine)
	return plan
}

// growNode plans exactly the capacity a node is short of (see growthNeed). It
// takes it from the free buddy of a range the node already owns first: a buddy
// no larger than the piece is taken whole, so the two merge into one range and
// one route; a larger buddy gives only the piece next to the node's range, and
// the rest stays free beside the node. After every piece the node's real
// capacity is recomputed with merged ranges, because a piece that merges loses
// none of the 3 reserved addresses and may already be enough. Whatever is still
// missing comes from the pool, in as few blocks as possible.
func (plan *poolPlan) growNode(spec *remediationv1alpha1.IPPoolSpec, p *buddy.Pool, node string, pods, capacity, demand, minAddrs int, active []*buddy.Block) {
	limit := capacityCap(spec, pods)
	required := requiredCapacity(spec, demand)
	cidrs := make([]string, len(active))
	for i, b := range active {
		cidrs[i] = b.CIDR
	}
	have, added := capacity, 0
	for step := 0; step < 8 && (added == 0 || have < required); step++ {
		need := growthNeed(spec, have, demand, limit-have, minAddrs)
		if need == 0 {
			if added == 0 {
				plan.Problems = append(plan.Problems, fmt.Sprintf("node %s reached its capacity cap (%d%% of %d pods)", node, spec.CapacityCapPercent, pods))
			}
			return
		}
		slices.SortFunc(active, func(a, b *buddy.Block) int { return b.Addresses() - a.Addresses() })
		piece := 1 << (bits.Len(uint(need)) - 1) // largest power of two within what is missing
		placed := false
		for _, blk := range active {
			got, err := p.AllocateFromBuddy(blk, piece, node)
			if err != nil {
				continue
			}
			reason := reasonGrowInPlace
			if got.Addresses() < blk.Addresses() {
				reason = reasonGrowBeside
			}
			plan.New = append(plan.New, plannedBlock{NodeName: node, CIDR: got.CIDR, Reason: reason})
			cidrs = append(cidrs, got.CIDR)
			placed = true
			break
		}
		if !placed {
			got, err := p.AllocateDemand(need, node)
			if err != nil {
				plan.Exhausted = true
				plan.Problems = append(plan.Problems, fmt.Sprintf("cannot grow node %s: %v", node, err))
				break
			}
			for _, b := range got {
				plan.New = append(plan.New, plannedBlock{NodeName: node, CIDR: b.CIDR, Reason: reasonGrow})
				cidrs = append(cidrs, b.CIDR)
			}
			added++
			break
		}
		added++
		have = capacityOf(cidrs)
	}
	if added > 0 {
		plan.Pending = true
	}
}

// latestChange is the most recent phase change of any of the node's blocks.
// nodeBlocks is a node's blocks sorted by what the planner does with them.
type nodeBlocks struct {
	used                              int
	inFlight                          bool
	active                            []*buddy.Block
	activeBlocks, draining, releasing []*remediationv1alpha1.NodeBlock
}

// classify sorts a node's blocks. Active blocks count towards its capacity and
// usage; a Pending block, or a drain being undone, is a change in flight.
func classify(p *buddy.Pool, owned []remediationv1alpha1.NodeBlock) nodeBlocks {
	var st nodeBlocks
	for i := range owned {
		b := &owned[i]
		switch blockPhase(b) {
		case remediationv1alpha1.NodeBlockActive:
			if b.Spec.Drain {
				st.draining = append(st.draining, b)
				continue
			}
			st.used += b.Status.UsedAddresses
			st.activeBlocks = append(st.activeBlocks, b)
			if b.Spec.Release != "" {
				st.releasing = append(st.releasing, b)
			}
			if blk := p.Find(b.Spec.CIDR); blk != nil {
				st.active = append(st.active, blk)
			}
		case remediationv1alpha1.NodeBlockPending:
			st.inFlight = true
		case remediationv1alpha1.NodeBlockDraining:
			if b.Spec.Drain {
				st.draining = append(st.draining, b)
			} else {
				st.inFlight = true
			}
		}
	}
	return st
}

// completeSplits plans the split of every releasing block whose released half
// the agent reports empty, and reports whether there was one.
func (plan *poolPlan) completeSplits(releasing []*remediationv1alpha1.NodeBlock) bool {
	split := false
	for _, b := range releasing {
		if b.Status.ReleaseReady != "" && b.Status.ReleaseReady == b.Spec.Release {
			plan.Split = append(plan.Split, b.Name)
			split = true
		}
	}
	return split
}

// shrinkNode gives back what the node can spare: as many whole blocks as
// possible, or else the empty half of one grown block. The pods still in
// draining blocks will be replaced inside the remaining ones, so they count
// towards the node's demand. The caller checks that the node has been stable
// for the cooldown: right after a grow, the only block old enough to drain is
// a full one, and draining it does nothing while its pods run.
func (plan *poolPlan) shrinkNode(spec *remediationv1alpha1.IPPoolSpec, owned []remediationv1alpha1.NodeBlock, pods, used, podCount int,
	activeBlocks, draining []*remediationv1alpha1.NodeBlock, minAddrs int, now time.Time) {
	demand := used
	for _, d := range draining {
		demand += d.Status.UsedAddresses
	}
	// The agent reports leases with a delay. A node that really runs more pods
	// than that must not be shrunk on the old number (seen live: a block full of
	// running pods was drained because the count still said 39 of 49).
	demand = max(demand, podCount)
	first := startingBlock(owned)
	drained := false
	for {
		name := shrinkCandidate(spec, pods, demand, activeBlocks, first, now)
		if name == "" {
			break
		}
		plan.Drain = append(plan.Drain, name)
		drained = true
		activeBlocks = slices.DeleteFunc(activeBlocks, func(b *remediationv1alpha1.NodeBlock) bool { return b.Name == name })
	}
	if drained {
		return
	}
	if name, half := splitCandidate(spec, pods, demand, activeBlocks, first, minAddrs, now); name != "" {
		plan.Release = append(plan.Release, halfRelease{Name: name, Half: half})
	}
}

// nextTimer is how long until the earliest timer in the pool runs out: a
// quarantine ends (the block can be released), or a shrink cooldown ends (a
// node with more than one Active block may give one back). Nothing in the
// cluster changes when a timer runs out, so no watch event wakes the
// controller; without this it would only notice at its idle requeue, which
// made every cooldown and quarantine last up to that interval. It returns 0
// when no timer is running.
func nextTimer(spec *remediationv1alpha1.IPPoolSpec, blocks []remediationv1alpha1.NodeBlock, now time.Time) time.Duration {
	quarantine := time.Duration(spec.QuarantineSeconds) * time.Second
	cooldown := time.Duration(spec.ShrinkCooldownSeconds) * time.Second
	var best time.Duration
	consider := func(at time.Time) {
		if d := at.Sub(now); d > 0 && (best == 0 || d < best) {
			best = d
		}
	}
	byNode := map[string][]remediationv1alpha1.NodeBlock{}
	active := map[string]int{}
	for i := range blocks {
		b := &blocks[i]
		switch blockPhase(b) {
		case remediationv1alpha1.NodeBlockReleased:
			continue
		case remediationv1alpha1.NodeBlockQuarantined:
			consider(transitionTime(b).Add(quarantine))
		case remediationv1alpha1.NodeBlockActive:
			if !b.Spec.Drain {
				active[b.Spec.NodeName]++
			}
		}
		byNode[b.Spec.NodeName] = append(byNode[b.Spec.NodeName], *b)
	}
	if !spec.DisableShrink {
		for node, owned := range byNode {
			if active[node] < 2 {
				continue // a single block is never drained
			}
			consider(latestChange(owned).Add(cooldown))
			for i := range owned {
				consider(transitionTime(&owned[i]).Add(cooldown))
			}
		}
	}
	return best
}

func latestChange(owned []remediationv1alpha1.NodeBlock) time.Time {
	var t time.Time
	for i := range owned {
		if tt := transitionTime(&owned[i]); tt.After(t) {
			t = tt
		}
	}
	return t
}

// changedNodes lists the nodes whose blocks this plan changes.
func (plan *poolPlan) changedNodes(blocks []remediationv1alpha1.NodeBlock) map[string]bool {
	owner := map[string]string{}
	for i := range blocks {
		owner[blocks[i].Name] = blocks[i].Spec.NodeName
	}
	out := map[string]bool{}
	for _, b := range plan.New {
		out[b.NodeName] = true
	}
	for _, list := range [][]string{plan.Drain, plan.Undrain, plan.Quarantine, plan.Delete, plan.Unrelease, plan.Split} {
		for _, name := range list {
			if n := owner[name]; n != "" {
				out[n] = true
			}
		}
	}
	for _, h := range plan.Release {
		if n := owner[h.Name]; n != "" {
			out[n] = true
		}
	}
	return out
}

// starveGrace is how long after a change to a node's blocks a "no IP
// addresses" failure is still blamed on the old state. During a burst, pod
// sandboxes that read the CNI config before a new block went Active keep
// failing for a moment after it; they succeed on retry and must not count as a
// new shortage. Seen live: 21 such failures one second after a block went
// Active grew a node by a /26 it did not need.
const starveGrace = 5 * time.Second

// starved reports whether a failure at time t is newer than every change to the
// node's blocks, by more than starveGrace. lastChange is the latest change the
// pool status remembers, which includes released blocks.
func starved(t time.Time, owned []remediationv1alpha1.NodeBlock, lastChange time.Time) bool {
	if t.IsZero() {
		return false
	}
	if !lastChange.IsZero() && t.Before(lastChange.Add(starveGrace)) {
		return false
	}
	for i := range owned {
		if t.Before(transitionTime(&owned[i]).Add(starveGrace)) {
			return false
		}
	}
	return true
}

func transitionTime(b *remediationv1alpha1.NodeBlock) time.Time {
	if b.Status.LastTransitionTime != nil {
		return b.Status.LastTransitionTime.Time
	}
	return b.CreationTimestamp.Time
}

// shrinkCandidate picks one Active block to drain, or "". The node must keep at
// least its initial capacity, and after the drain it must sit at or under
// ShrinkThresholdPercent with the grow trigger still clear, so a drain is never
// followed straight by a grow. The emptiest block goes first because it
// finishes draining soonest. A block younger than the cooldown is left alone.
func shrinkCandidate(spec *remediationv1alpha1.IPPoolSpec, pods, used int, active []*remediationv1alpha1.NodeBlock, first string, now time.Time) string {
	if len(active) < 2 {
		return ""
	}
	cooldown := time.Duration(spec.ShrinkCooldownSeconds) * time.Second
	floor := usableAddresses(initialBlockSize(spec, pods))

	cands := append([]*remediationv1alpha1.NodeBlock(nil), active...)
	slices.SortFunc(cands, func(a, b *remediationv1alpha1.NodeBlock) int {
		if a.Status.UsedAddresses != b.Status.UsedAddresses {
			return a.Status.UsedAddresses - b.Status.UsedAddresses
		}
		if sa, sb := blockAddresses(a.Spec.CIDR), blockAddresses(b.Spec.CIDR); sa != sb {
			return sa - sb
		}
		return strings.Compare(a.Spec.CIDR, b.Spec.CIDR)
	})
	for _, c := range cands {
		if c.Name == first || now.Sub(transitionTime(c)) < cooldown {
			continue
		}
		rest := blocksCapacity(slices.DeleteFunc(slices.Clone(active), func(b *remediationv1alpha1.NodeBlock) bool { return b == c }))
		if rest < floor || used*100 > spec.ShrinkThresholdPercent*rest || needsGrowth(spec, rest, used) {
			continue
		}
		return c.Name
	}
	return ""
}

// splitCandidate picks a grown block with an empty half to give back, and that
// half: the largest such half first, the upper one when both are empty
// (host-local hands out addresses from the bottom). The same limits as a whole
// drain apply to what the node keeps. The starting block is never split, and
// nothing below the minimum block size.
func splitCandidate(spec *remediationv1alpha1.IPPoolSpec, pods, used int, active []*remediationv1alpha1.NodeBlock, first string, minAddrs int, now time.Time) (string, string) {
	cooldown := time.Duration(spec.ShrinkCooldownSeconds) * time.Second
	floor := usableAddresses(initialBlockSize(spec, pods))

	cands := append([]*remediationv1alpha1.NodeBlock(nil), active...)
	slices.SortFunc(cands, func(a, b *remediationv1alpha1.NodeBlock) int {
		if sa, sb := blockAddresses(a.Spec.CIDR), blockAddresses(b.Spec.CIDR); sa != sb {
			return sb - sa
		}
		return strings.Compare(a.Spec.CIDR, b.Spec.CIDR)
	})
	for _, c := range cands {
		if c.Name == first || c.Spec.Release != "" || blockAddresses(c.Spec.CIDR) <= minAddrs ||
			len(c.Status.HalfUsage) != 2 || now.Sub(transitionTime(c)) < cooldown {
			continue
		}
		lower, upper, err := buddy.Halves(c.Spec.CIDR)
		if err != nil {
			continue
		}
		half, keep := "", ""
		switch {
		case c.Status.HalfUsage[1] == 0:
			half, keep = upper, lower
		case c.Status.HalfUsage[0] == 0:
			half, keep = lower, upper
		default:
			continue
		}
		cidrs := []string{keep}
		for _, b := range active {
			if b != c {
				cidrs = append(cidrs, servedCIDR(b))
			}
		}
		rest := capacityOf(cidrs)
		if rest < floor || used*100 > spec.ShrinkThresholdPercent*rest || needsGrowth(spec, rest, used) {
			continue
		}
		return c.Name, half
	}
	return "", ""
}

// startingBlock names the node's starting block, which is never drained or
// split. It is the block marked Initial; on blocks from before that field, the
// oldest block the node still uses.
func startingBlock(owned []remediationv1alpha1.NodeBlock) string {
	oldest := -1
	for i := range owned {
		b := &owned[i]
		if b.Spec.Initial {
			return b.Name
		}
		switch blockPhase(b) {
		case remediationv1alpha1.NodeBlockQuarantined, remediationv1alpha1.NodeBlockReleased:
			continue
		}
		if b.Spec.Drain {
			continue
		}
		if oldest < 0 || b.CreationTimestamp.Before(&owned[oldest].CreationTimestamp) ||
			(b.CreationTimestamp.Equal(&owned[oldest].CreationTimestamp) && b.Name < owned[oldest].Name) {
			oldest = i
		}
	}
	if oldest < 0 {
		return ""
	}
	return owned[oldest].Name
}

// servedCIDR is the part of a block its owner hands out: the whole block, or
// the kept half while the other half is being given back. The agent applies
// the same rule to its CNI config.
func servedCIDR(b *remediationv1alpha1.NodeBlock) string {
	if b.Spec.Release == "" {
		return b.Spec.CIDR
	}
	keep, err := buddy.OtherHalf(b.Spec.CIDR, b.Spec.Release)
	if err != nil {
		return b.Spec.CIDR
	}
	return keep
}

// cidrUsage reports, per valid pool CIDR, how much has been handed out.
func cidrUsage(spec *remediationv1alpha1.IPPoolSpec, nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock) []remediationv1alpha1.CIDRUsage {
	cidrs, _ := validCIDRs(spec, nodes)
	out := make([]remediationv1alpha1.CIDRUsage, 0, len(cidrs))
	for _, c := range cidrs {
		_, pn, _ := net.ParseCIDR(c)
		u := remediationv1alpha1.CIDRUsage{CIDR: c, Total: int64(blockAddresses(c))}
		for i := range blocks {
			if blockPhase(&blocks[i]) == remediationv1alpha1.NodeBlockReleased {
				continue
			}
			if ip, _, err := net.ParseCIDR(blocks[i].Spec.CIDR); err == nil && pn.Contains(ip) {
				u.Allocated += int64(blockAddresses(blocks[i].Spec.CIDR))
			}
		}
		switch {
		case u.Allocated == 0:
			u.State = "Standby"
		case u.Allocated >= u.Total:
			u.State = "Full"
		default:
			u.State = "Active"
		}
		out = append(out, u)
	}
	return out
}

func lowWater(spec *remediationv1alpha1.IPPoolSpec, u []remediationv1alpha1.CIDRUsage) bool {
	var total, alloc int64
	for _, c := range u {
		total += c.Total
		alloc += c.Allocated
	}
	return total > 0 && (total-alloc)*100 < int64(spec.LowWaterPercent)*total
}

func joinProblems(p []string) string {
	return strings.Join(p, "; ")
}
