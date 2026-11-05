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
	"strings"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam/buddy"
)

const (
	// blockOverhead is what host-local reserves in every block: the network
	// address, the gateway and the broadcast address (verified in Phase 0).
	blockOverhead = 3

	// minInitialAddresses is the floor for a node's first block.
	minInitialAddresses = 16

	// defaultAllocatablePods is used until a node reports allocatable pods.
	defaultAllocatablePods = 110
)

// blockName derives a NodeBlock name from its CIDR. Names are unique in the API
// server, so two NodeBlocks can never claim the same block.
func blockName(cidr string) string {
	return strings.NewReplacer(".", "-", "/", "-").Replace(cidr)
}

// usableAddresses is how many addresses of a block host-local can hand to pods.
func usableAddresses(size int) int {
	if size <= blockOverhead {
		return 0
	}
	return size - blockOverhead
}

// capacityOf is how many pod addresses a set of blocks gives. Buddy blocks the
// node owns are merged into one host-local range (the agent renders them that
// way), and each range loses blockOverhead once, so two /27 buddies hold 61 pods,
// not 58.
func capacityOf(cidrs []string) int {
	agg, err := buddy.Aggregate(cidrs)
	if err != nil {
		agg = cidrs
	}
	total := 0
	for _, c := range agg {
		total += usableAddresses(blockAddresses(c))
	}
	return total
}

func blocksCapacity(blocks []*remediationv1alpha1.NodeBlock) int {
	cidrs := make([]string, len(blocks))
	for i, b := range blocks {
		cidrs[i] = servedCIDR(b)
	}
	return capacityOf(cidrs)
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// initialBlockSize is the address count of a new node's first block: the
// smallest power of two >= max(16, InitialBlockPercent% of allocatable pods).
func initialBlockSize(spec *remediationv1alpha1.IPPoolSpec, allocatablePods int) int {
	want := (allocatablePods*spec.InitialBlockPercent + 99) / 100
	return nextPow2(max(want, minInitialAddresses))
}

// capacityCap is the most usable addresses a node may own in total.
func capacityCap(spec *remediationv1alpha1.IPPoolSpec, allocatablePods int) int {
	return allocatablePods * spec.CapacityCapPercent / 100
}

// needsGrowth reports whether free addresses fell below
// max(TriggerFloor, (100 - ExpandThresholdPercent)% of capacity).
func needsGrowth(spec *remediationv1alpha1.IPPoolSpec, capacity, used int) bool {
	threshold := max(spec.TriggerFloor, capacity*(100-spec.ExpandThresholdPercent)/100)
	return capacity-used < threshold
}

// requiredCapacity is the smallest capacity that does not trigger a grow for
// the given demand: the first capacity at which needsGrowth is false. It is
// searched, not computed, so it agrees with needsGrowth to the last address
// (whose threshold rounds down).
func requiredCapacity(spec *remediationv1alpha1.IPPoolSpec, demand int) int {
	c := demand + spec.TriggerFloor
	for limit := 2*demand + spec.TriggerFloor + 8; c < limit && needsGrowth(spec, c, demand); c++ {
	}
	return c
}

// growthNeed is the address count to add to a node: what its demand is short of
// (see requiredCapacity), plus the 3 addresses a new range loses, rounded up to
// a whole minimum block. Nothing more: a node is given what it needs, not a
// percentage of what it already has. It is then cut back until its usable
// size fits in the headroom left under the node's capacity cap, and is 0 when
// not even a minimum block fits.
func growthNeed(spec *remediationv1alpha1.IPPoolSpec, capacity, demand, headroom, minBlock int) int {
	short := max(requiredCapacity(spec, demand)-capacity, 1)
	need := (short + blockOverhead + minBlock - 1) / minBlock * minBlock
	for need >= minBlock && usableAddresses(need) > headroom {
		need -= minBlock
	}
	if need < minBlock {
		return 0
	}
	return need
}
