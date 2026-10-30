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

package sim

import (
	"fmt"
	"math/rand"
	"net"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam"
	"CS331-CN-Project-1/operator/internal/controller/ipam/buddy"
)

// BuddyConfig sets the agent timings the model uses.
type BuddyConfig struct {
	Spec remediationv1alpha1.IPPoolSpec
	// PromoteDelay is how long a Pending block takes to become Active: route
	// install and acknowledgement on every node.
	PromoteDelay time.Duration
	// DrainSettle is the agent's settle time before it reports a drained block empty.
	DrainSettle time.Duration
	// NoShrink, NoStarve switch off draining and the emergency grow, for ablations.
	NoStarve bool
}

type buddyPolicy struct {
	cfg     BuddyConfig
	rng     *rand.Rand
	spec    remediationv1alpha1.IPPoolSpec
	nodes   []corev1.Node
	blocks  map[string]*remediationv1alpha1.NodeBlock
	starved map[string]time.Time
	now     time.Time
	ops     int
	name    string
}

var epoch = time.Unix(1_700_000_000, 0)

func newBuddy(name string, rng *rand.Rand, nodes, maxPods int, cfg BuddyConfig) *buddyPolicy {
	ns := make([]corev1.Node, nodes)
	for i := range ns {
		ns[i] = corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName(i)}}
		ns[i].Status.Allocatable = corev1.ResourceList{corev1.ResourcePods: *resource.NewQuantity(int64(maxPods), resource.DecimalSI)}
		ns[i].Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: fmt.Sprintf("192.168.%d.%d", i/250, i%250+1)}}
	}
	return &buddyPolicy{cfg: cfg, rng: rng, spec: ipam.WithDefaults(cfg.Spec), nodes: ns, name: name,
		blocks: map[string]*remediationv1alpha1.NodeBlock{}, starved: map[string]time.Time{}, now: epoch}
}

func nodeName(i int) string { return fmt.Sprintf("n%04d", i) }

func (b *buddyPolicy) Name() string { return b.name }

// mine returns the node's blocks in a stable order.
func (b *buddyPolicy) mine(n int) []*remediationv1alpha1.NodeBlock {
	var out []*remediationv1alpha1.NodeBlock
	name := nodeName(n)
	for _, blk := range b.blocks {
		if blk.Spec.NodeName == name {
			out = append(out, blk)
		}
	}
	slices.SortFunc(out, func(x, y *remediationv1alpha1.NodeBlock) int {
		if x.Spec.CIDR < y.Spec.CIDR {
			return -1
		}
		return 1
	})
	return out
}

func size(blk *remediationv1alpha1.NodeBlock) int {
	return 1 << (32 - prefixLen(blk.Spec.CIDR))
}

func prefixLen(c string) int {
	var p int
	for i := range c {
		if c[i] == '/' {
			_, _ = fmt.Sscanf(c[i+1:], "%d", &p)
		}
	}
	return p
}

func placeable(blk *remediationv1alpha1.NodeBlock) bool {
	return blk.Status.Phase == remediationv1alpha1.NodeBlockActive && !blk.Spec.Drain
}

// group is a host-local range: the node's placeable buddy blocks, merged.
type group struct {
	cidr    string
	members []*remediationv1alpha1.NodeBlock
}

func (b *buddyPolicy) groups(blks []*remediationv1alpha1.NodeBlock) []group {
	var cidrs []string
	for _, blk := range blks {
		if placeable(blk) {
			cidrs = append(cidrs, blk.Spec.CIDR)
		}
	}
	agg, err := buddy.Aggregate(cidrs)
	if err != nil {
		return nil
	}
	out := make([]group, len(agg))
	for i, c := range agg {
		out[i].cidr = c
		_, n, _ := net.ParseCIDR(c)
		for _, blk := range blks {
			if ip, _, err := net.ParseCIDR(blk.Spec.CIDR); err == nil && placeable(blk) && n.Contains(ip) {
				out[i].members = append(out[i].members, blk)
			}
		}
	}
	return out
}

func (b *buddyPolicy) Place(n, k int) int {
	gs := b.groups(b.mine(n))
	free := make([]int, len(gs))
	for i, g := range gs {
		used := 0
		for _, m := range g.members {
			used += m.Status.UsedAddresses
		}
		free[i] = max(0, ipam.UsableAddresses(cidrSize(g.cidr))-used)
	}
	placed := 0
	for placed < k {
		i := pickWeighted(b.rng, free)
		if i < 0 {
			break
		}
		w := make([]int, len(gs[i].members))
		for j, m := range gs[i].members {
			w[j] = size(m)
		}
		gs[i].members[pickWeighted(b.rng, w)].Status.UsedAddresses++
		free[i]--
		placed++
	}
	return placed
}

func (b *buddyPolicy) Remove(n, k int) {
	blks := b.mine(n)
	for range k {
		w := make([]int, len(blks))
		for i, blk := range blks {
			w[i] = blk.Status.UsedAddresses
		}
		i := pickWeighted(b.rng, w)
		if i < 0 {
			return
		}
		blks[i].Status.UsedAddresses--
	}
}

func (b *buddyPolicy) Churn(p float64) {
	for _, blk := range b.blocks {
		blk.Status.UsedAddresses -= binomial(b.rng, blk.Status.UsedAddresses, p)
	}
}

func (b *buddyPolicy) Starved(n int) {
	if !b.cfg.NoStarve {
		b.starved[nodeName(n)] = b.now
	}
}

func (b *buddyPolicy) setPhase(blk *remediationv1alpha1.NodeBlock, ph string) {
	t := metav1.NewTime(b.now)
	blk.Status.Phase, blk.Status.LastTransitionTime = ph, &t
}

// agents models what the node agents do between two controller passes.
func (b *buddyPolicy) agents() {
	for _, blk := range b.blocks {
		since := b.now.Sub(blk.Status.LastTransitionTime.Time)
		switch blk.Status.Phase {
		case remediationv1alpha1.NodeBlockPending:
			if since >= b.cfg.PromoteDelay {
				b.setPhase(blk, remediationv1alpha1.NodeBlockActive)
			}
		case remediationv1alpha1.NodeBlockActive:
			if blk.Spec.Drain {
				b.setPhase(blk, remediationv1alpha1.NodeBlockDraining)
			}
		case remediationv1alpha1.NodeBlockDraining:
			switch {
			case !blk.Spec.Drain:
				b.setPhase(blk, remediationv1alpha1.NodeBlockActive)
			case blk.Status.UsedAddresses == 0 && since >= b.cfg.DrainSettle:
				b.setPhase(blk, remediationv1alpha1.NodeBlockQuarantined)
			}
		}
	}
}

func (b *buddyPolicy) Tick(t float64) {
	b.now = epoch.Add(time.Duration(t * float64(time.Second)))
	b.agents()

	items := make([]remediationv1alpha1.NodeBlock, 0, len(b.blocks))
	for _, blk := range b.blocks {
		items = append(items, *blk)
	}
	plan := ipam.PlanPool(&b.spec, b.nodes, items, b.now, b.starved)

	for _, pb := range plan.New {
		ts := metav1.NewTime(b.now)
		name := ipam.BlockName(pb.CIDR)
		b.blocks[name] = &remediationv1alpha1.NodeBlock{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: ts},
			Spec:       remediationv1alpha1.NodeBlockSpec{NodeName: pb.NodeName, CIDR: pb.CIDR},
			Status:     remediationv1alpha1.NodeBlockStatus{Phase: remediationv1alpha1.NodeBlockPending, LastTransitionTime: &ts},
		}
		b.ops++
	}
	for _, n := range plan.Drain {
		b.blocks[n].Spec.Drain = true
		b.ops++
	}
	for _, n := range plan.Undrain {
		b.blocks[n].Spec.Drain = false
	}
	for _, n := range plan.Delete {
		delete(b.blocks, n)
		b.ops++
	}
}

func (b *buddyPolicy) Pods(n int) int {
	t := 0
	for _, blk := range b.mine(n) {
		t += blk.Status.UsedAddresses
	}
	return t
}

func (b *buddyPolicy) Allocated() int {
	t := 0
	for _, blk := range b.blocks {
		t += size(blk)
	}
	return t
}

func (b *buddyPolicy) Routes() int {
	byOwner := map[string][]string{}
	for _, blk := range b.blocks {
		switch blk.Status.Phase {
		case remediationv1alpha1.NodeBlockQuarantined, remediationv1alpha1.NodeBlockReleased:
		default:
			byOwner[blk.Spec.NodeName] = append(byOwner[blk.Spec.NodeName], blk.Spec.CIDR)
		}
	}
	total := 0
	for _, cidrs := range byOwner {
		if agg, err := buddy.Aggregate(cidrs); err == nil {
			total += len(agg)
		}
	}
	return total
}

func (b *buddyPolicy) Ops() int { return b.ops }

func (b *buddyPolicy) Admitted() int {
	seen := map[string]bool{}
	for _, blk := range b.blocks {
		seen[blk.Spec.NodeName] = true
	}
	return len(seen)
}
