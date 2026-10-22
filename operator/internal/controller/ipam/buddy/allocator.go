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

// Package buddy implements a binary buddy allocator for IP CIDR blocks.
//
// The buddy allocator is the core algorithm behind Problem 1. It manages a tree
// of CIDR blocks for each pool CIDR. When a node needs IP space, the allocator
// finds the smallest free block that fits the request, splitting larger blocks
// as needed. When blocks are freed, adjacent "buddies" are merged back together
// to reduce fragmentation.
//
// Key invariant: every block is aligned to its size, so each block is a single
// routable prefix. This preserves route aggregation.
//
// Reference: Linux kernel mm/page_alloc.c buddy allocator.
package buddy

import (
	"fmt"
	"math"
	"net"
	"sync"
)

// BlockState represents the current state of a block in the buddy tree.
type BlockState int

const (
	// BlockFree means the block is available for allocation.
	BlockFree BlockState = iota
	// BlockAllocated means the block is assigned to a node.
	BlockAllocated
	// BlockSplit means the block has been split into two smaller buddies.
	BlockSplit
)

func (s BlockState) String() string {
	switch s {
	case BlockFree:
		return "Free"
	case BlockAllocated:
		return "Allocated"
	case BlockSplit:
		return "Split"
	default:
		return "Unknown"
	}
}

// Block represents a single CIDR block in the buddy tree.
type Block struct {
	// CIDR is the network in CIDR notation (e.g. "10.244.0.0/24").
	CIDR string

	// Network is the parsed IP network.
	Network *net.IPNet

	// PrefixLen is the prefix length (e.g. 24 for a /24).
	PrefixLen int

	// State is the current state of this block.
	State BlockState

	// NodeName is the node this block is assigned to (empty if not allocated).
	NodeName string

	// Left is the first child after a split (smaller address).
	Left *Block

	// Right is the second child after a split (larger address).
	Right *Block

	// Parent is the parent block (nil for root).
	Parent *Block
}

// Addresses returns the number of host addresses in this block.
func (b *Block) Addresses() int {
	return int(math.Pow(2, float64(32-b.PrefixLen)))
}

// MergeEvent is returned by Defragment when two buddies are merged.
type MergeEvent struct {
	// MergedBlock is the resulting larger block.
	MergedBlock *Block

	// LeftChild was the left buddy before merging.
	LeftChild *Block

	// RightChild was the right buddy before merging.
	RightChild *Block
}

// BuddyAllocator manages a single CIDR range as a buddy tree.
type BuddyAllocator struct {
	mu sync.RWMutex

	// root is the top-level block representing the entire CIDR.
	root *Block

	// minPrefixLen is the smallest block we will allocate (e.g. 28 = /28 = 16 addrs).
	// Larger number = smaller block.
	minPrefixLen int

	// maxPrefixLen is the largest block a single allocation can get (e.g. 22 = /22 = 1024 addrs).
	// Smaller number = larger block.
	maxPrefixLen int

	// freeBlocks indexes free blocks by prefix length for O(1) lookup.
	// Key: prefix length, Value: set of free blocks at that level.
	freeBlocks map[int]map[*Block]struct{}
}

// NewBuddyAllocator creates a new allocator for the given CIDR range.
//
// Parameters:
//   - cidr: the CIDR to manage (e.g. "10.244.0.0/16")
//   - minPrefixLen: smallest allocatable block prefix (e.g. 28 for /28)
//   - maxPrefixLen: largest allocatable block prefix (e.g. 22 for /22)
//
// TODO: Implement this function.
//   1. Parse the CIDR string into a *net.IPNet.
//   2. Create the root Block with State = BlockFree.
//   3. Initialize the freeBlocks index with the root at its prefix level.
//   4. Validate that minPrefixLen >= root prefix and maxPrefixLen <= minPrefixLen.
func NewBuddyAllocator(cidr string, minPrefixLen, maxPrefixLen int) (*BuddyAllocator, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}

	rootPrefix, _ := network.Mask.Size()

	if maxPrefixLen < rootPrefix {
		return nil, fmt.Errorf("maxPrefixLen /%d is larger than the pool /%d", maxPrefixLen, rootPrefix)
	}
	if minPrefixLen < maxPrefixLen {
		return nil, fmt.Errorf("minPrefixLen /%d must be >= maxPrefixLen /%d", minPrefixLen, maxPrefixLen)
	}

	root := &Block{
		CIDR:      network.String(),
		Network:   network,
		PrefixLen: rootPrefix,
		State:     BlockFree,
	}

	freeBlocks := make(map[int]map[*Block]struct{})
	freeBlocks[rootPrefix] = map[*Block]struct{}{root: {}}

	return &BuddyAllocator{
		root:         root,
		minPrefixLen: minPrefixLen,
		maxPrefixLen: maxPrefixLen,
		freeBlocks:   freeBlocks,
	}, nil
}

// Allocate finds the smallest free block that can hold `requestedAddrs` addresses
// and returns it. If no single block fits, it splits a larger block.
//
// Parameters:
//   - requestedAddrs: number of addresses needed (e.g. 100)
//   - nodeName: the node this block will be assigned to
//
// Returns the allocated Block, or an error if no space is available.
//
// TODO: Implement the allocation algorithm:
//   1. Round requestedAddrs up to the next power of 2.
//   2. Convert to a prefix length: prefixLen = 32 - log2(roundedSize).
//   3. Clamp to [maxPrefixLen, minPrefixLen].
//   4. Look in freeBlocks[prefixLen]. If a block exists, allocate it.
//   5. If not, look at prefixLen-1 (larger block), split it, and recurse.
//   6. Set block.State = BlockAllocated, block.NodeName = nodeName.
//   7. Update the freeBlocks index.
func (a *BuddyAllocator) Allocate(requestedAddrs int, nodeName string) (*Block, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// TODO: Implement allocation logic here.
	// Step 1: Calculate required prefix length from requestedAddrs.
	// Step 2: Search freeBlocks for the best fit.
	// Step 3: Split larger blocks if needed.
	// Step 4: Mark as allocated and return.

	return nil, fmt.Errorf("TODO: Allocate not yet implemented")
}

// Free returns a block to the allocator and attempts to merge it with its buddy.
//
// TODO: Implement the free algorithm:
//   1. Validate the block is currently allocated.
//   2. Set block.State = BlockFree, clear NodeName.
//   3. Check if the buddy (sibling) is also free.
//   4. If both buddies are free, merge them into the parent and recurse upward.
//   5. Update the freeBlocks index at each step.
func (a *BuddyAllocator) Free(block *Block) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// TODO: Implement free logic here.
	// Step 1: Mark block as free.
	// Step 2: Try to merge with buddy.
	// Step 3: Update freeBlocks index.

	return fmt.Errorf("TODO: Free not yet implemented")
}

// Defragment scans the tree for pairs of free buddies that can be merged.
// Returns a list of merge events that were performed.
//
// TODO: Implement defragmentation:
//   1. Walk the tree bottom-up.
//   2. For each split node where both children are free, merge them.
//   3. Record each merge as a MergeEvent.
func (a *BuddyAllocator) Defragment() []MergeEvent {
	a.mu.Lock()
	defer a.mu.Unlock()

	// TODO: Implement defragmentation here.

	return nil
}

// Stats returns current allocation statistics.
func (a *BuddyAllocator) Stats() AllocatorStats {
	a.mu.RLock()
	defer a.mu.RUnlock()

	stats := AllocatorStats{
		RootCIDR: a.root.CIDR,
	}

	// TODO: Walk the tree and compute stats.

	return stats
}

// AllocatorStats holds statistics about the allocator's current state.
type AllocatorStats struct {
	RootCIDR           string
	TotalAddresses     int
	AllocatedAddresses int
	FreeAddresses      int
	FragmentedBlocks   int // Number of free blocks that could be merged
	AllocatedBlocks    int
	FreeBlocksByLevel  map[int]int // prefix length → count of free blocks
}

// GetAllocatedBlocks returns all currently allocated blocks.
//
// TODO: Walk the tree and collect allocated blocks.
func (a *BuddyAllocator) GetAllocatedBlocks() []*Block {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// TODO: Implement this.

	return nil
}

// GetBlocksForNode returns all blocks allocated to a specific node.
//
// TODO: Filter GetAllocatedBlocks by NodeName.
func (a *BuddyAllocator) GetBlocksForNode(nodeName string) []*Block {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// TODO: Implement this.

	return nil
}
