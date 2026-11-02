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
		cidrs[i] = b.Spec.CIDR
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

// growthTarget is the address count to add: GrowthPercent% of the current
// capacity, rounded up to a power of two, and at least one minimum block. It is
// then halved until its usable size fits in the headroom left under the node's
// capacity cap. It returns 0 when not even a minimum block fits.
func growthTarget(spec *remediationv1alpha1.IPPoolSpec, capacity, headroom, minBlock int) int {
	want := max((capacity*spec.GrowthPercent+99)/100, minBlock)
	target := nextPow2(want)
	for target > minBlock && usableAddresses(target) > headroom {
		target >>= 1
	}
	if usableAddresses(target) > headroom {
		return 0
	}
	return target
}
