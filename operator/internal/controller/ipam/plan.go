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
	// CIDRs is the per-CIDR usage for the pool status.
	CIDRs []remediationv1alpha1.CIDRUsage
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
	def(&s.GrowthPercent, 50)
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
	ones, bits := n.Mask.Size()
	return 1 << (bits - ones)
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
		inFlight := false
		used := 0
		var active []*buddy.Block
		var activeBlocks, draining []*remediationv1alpha1.NodeBlock
		for i := range owned {
			b := &owned[i]
			switch blockPhase(b) {
			case remediationv1alpha1.NodeBlockActive:
				if b.Spec.Drain {
					draining = append(draining, b)
					continue
				}
				used += b.Status.UsedAddresses
				activeBlocks = append(activeBlocks, b)
				if blk := p.Find(b.Spec.CIDR); blk != nil {
					active = append(active, blk)
				}
			case remediationv1alpha1.NodeBlockPending:
				inFlight = true
			case remediationv1alpha1.NodeBlockDraining:
				if b.Spec.Drain {
					draining = append(draining, b)
				} else {
					inFlight = true
				}
			}
		}
		if inFlight {
			plan.Pending = true
			continue
		}
		capacity := blocksCapacity(activeBlocks)

		if needsGrowth(spec, capacity, used) || starved(in.Starved[n.Name], owned) {
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
			plan.growNode(spec, p, n.Name, pods, capacity, minAddrs, active)
			continue
		}

		// Shrink: drain as many blocks as the node can spare. The pods still in
		// draining blocks will be replaced inside the remaining ones, so they
		// count towards the node's demand.
		if !spec.DisableShrink {
			demand := used
			for _, d := range draining {
				demand += d.Status.UsedAddresses
			}
			for {
				name := shrinkCandidate(spec, pods, demand, activeBlocks, in.Now)
				if name == "" {
					break
				}
				plan.Drain = append(plan.Drain, name)
				activeBlocks = slices.DeleteFunc(activeBlocks, func(b *remediationv1alpha1.NodeBlock) bool { return b.Name == name })
			}
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

// growNode plans more capacity for a node: the buddy of a block it owns if that
// does not overshoot, otherwise a new block (or several).
func (plan *poolPlan) growNode(spec *remediationv1alpha1.IPPoolSpec, p *buddy.Pool, node string, pods, capacity, minAddrs int, active []*buddy.Block) {
	headroom := capacityCap(spec, pods) - capacity
	target := growthTarget(spec, capacity, headroom, minAddrs)
	if target == 0 {
		plan.Problems = append(plan.Problems, fmt.Sprintf("node %s reached its capacity cap (%d%% of %d pods)", node, spec.CapacityCapPercent, pods))
		return
	}

	// Prefer taking the buddy of a block the node already owns: the two halves
	// then aggregate into one route. Largest block first, and only if the buddy
	// does not overshoot the target.
	slices.SortFunc(active, func(a, b *buddy.Block) int { return b.Addresses() - a.Addresses() })
	for _, blk := range active {
		if blk.Addresses() > 2*target || usableAddresses(blk.Addresses()) > headroom {
			continue
		}
		if bd, err := p.AllocateBuddyOf(blk, node); err == nil {
			plan.New = append(plan.New, plannedBlock{NodeName: node, CIDR: bd.CIDR, Reason: reasonGrowInPlace})
			plan.Pending = true
			return
		}
	}

	got, err := p.AllocateDemand(target, node)
	if err != nil {
		plan.Exhausted = true
		plan.Problems = append(plan.Problems, fmt.Sprintf("cannot grow node %s: %v", node, err))
		return
	}
	for _, b := range got {
		plan.New = append(plan.New, plannedBlock{NodeName: node, CIDR: b.CIDR, Reason: reasonGrow})
	}
	plan.Pending = true
}

// starved reports whether a failure at time t is newer than every change to the
// node's blocks.
func starved(t time.Time, owned []remediationv1alpha1.NodeBlock) bool {
	if t.IsZero() {
		return false
	}
	for i := range owned {
		if !t.After(transitionTime(&owned[i])) {
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
func shrinkCandidate(spec *remediationv1alpha1.IPPoolSpec, pods, used int, active []*remediationv1alpha1.NodeBlock, now time.Time) string {
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
		if now.Sub(transitionTime(c)) < cooldown {
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
