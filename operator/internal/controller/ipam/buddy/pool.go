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
// Pool manages multiple BuddyAllocators — one per discontiguous CIDR range.
// This supports the multi-CIDR pool requirement: when the first CIDR fills up,
// the controller adds a second CIDR and the pool manages both independently.
package buddy

import (
	"fmt"
	"sync"
)

// Pool manages multiple buddy allocators across discontiguous CIDR ranges.
// It provides a single interface for the IPAM controller to allocate and free
// blocks without worrying about which underlying CIDR they come from.
type Pool struct {
	mu         sync.RWMutex
	allocators []*BuddyAllocator
}

// NewPool creates an empty Pool. Add CIDRs with AddCIDR.
func NewPool() *Pool {
	return &Pool{
		allocators: make([]*BuddyAllocator, 0),
	}
}

// AddCIDR adds a new CIDR range to the pool and creates a BuddyAllocator for it.
//
// TODO: Validate that the new CIDR does not overlap with any existing CIDRs.
func (p *Pool) AddCIDR(cidr string, minPrefixLen, maxPrefixLen int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	alloc, err := NewBuddyAllocator(cidr, minPrefixLen, maxPrefixLen)
	if err != nil {
		return fmt.Errorf("failed to create allocator for %s: %w", cidr, err)
	}

	// TODO: Check for overlap with existing allocators.

	p.allocators = append(p.allocators, alloc)
	return nil
}

// Allocate tries each CIDR's allocator in order until one succeeds.
//
// TODO: Implement allocation strategy:
//   1. Try each allocator in order.
//   2. Return the first successful allocation.
//   3. If all fail, return an error indicating pool exhaustion.
//   4. Consider a smarter strategy: prefer the allocator with the most free space.
func (p *Pool) Allocate(requestedAddrs int, nodeName string) (*Block, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// TODO: Try allocators and return the first success.

	return nil, fmt.Errorf("TODO: Pool.Allocate not yet implemented")
}

// Free returns a block to the correct allocator based on which CIDR contains it.
//
// TODO: Determine which allocator owns the block and delegate Free to it.
func (p *Pool) Free(block *Block) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// TODO: Find the right allocator and free the block.

	return fmt.Errorf("TODO: Pool.Free not yet implemented")
}

// Defragment runs defragmentation on all allocators in the pool.
func (p *Pool) Defragment() []MergeEvent {
	p.mu.Lock()
	defer p.mu.Unlock()

	var allEvents []MergeEvent
	for _, alloc := range p.allocators {
		events := alloc.Defragment()
		allEvents = append(allEvents, events...)
	}
	return allEvents
}

// PoolStats returns aggregate statistics across all CIDRs.
func (p *Pool) PoolStats() PoolStatsResult {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := PoolStatsResult{
		CIDRStats: make([]AllocatorStats, 0, len(p.allocators)),
	}

	for _, alloc := range p.allocators {
		stats := alloc.Stats()
		result.CIDRStats = append(result.CIDRStats, stats)
		result.TotalAddresses += stats.TotalAddresses
		result.AllocatedAddresses += stats.AllocatedAddresses
		result.FreeAddresses += stats.FreeAddresses
	}

	return result
}

// PoolStatsResult holds aggregate statistics for the entire pool.
type PoolStatsResult struct {
	TotalAddresses     int
	AllocatedAddresses int
	FreeAddresses      int
	CIDRStats          []AllocatorStats
}

// CIDRCount returns the number of CIDR ranges in the pool.
func (p *Pool) CIDRCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.allocators)
}
