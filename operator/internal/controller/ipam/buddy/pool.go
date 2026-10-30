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
// CIDRs are filled in the order they were added, so later CIDRs stay untouched
// (and returnable) until the earlier ones are full.
package buddy

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Pool manages multiple buddy allocators across discontiguous CIDR ranges.
// It provides a single interface for the IPAM controller to allocate and free
// blocks without worrying about which underlying CIDR they come from.
type Pool struct {
	mu         sync.RWMutex
	allocators []*BuddyAllocator
	quarantine time.Duration
	now        func() time.Time

	// minPrefix and maxPrefix are the configured block limits, remembered from
	// the first AddCIDR. A CIDR smaller than the largest block is clamped
	// individually, but demand is still sized with the configured limits.
	minPrefix, maxPrefix int
}

// NewPool creates an empty Pool. Add CIDRs with AddCIDR.
func NewPool() *Pool {
	return &Pool{
		allocators: make([]*BuddyAllocator, 0),
		now:        time.Now,
	}
}

// SetQuarantine sets the quarantine applied to every current and future CIDR.
func (p *Pool) SetQuarantine(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quarantine = d
	for _, a := range p.allocators {
		a.SetQuarantine(d)
	}
}

// SetClock replaces the time source on every current and future CIDR.
func (p *Pool) SetClock(now func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
	for _, a := range p.allocators {
		a.SetClock(now)
	}
}

// HasCIDR reports whether the exact CIDR is already in the pool.
func (p *Pool) HasCIDR(cidr string) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.allocators {
		if a.root.CIDR == n.String() {
			return true
		}
	}
	return false
}

// AddCIDR adds a new CIDR range to the pool and creates a BuddyAllocator for it.
// It fails if the range overlaps one already in the pool.
func (p *Pool) AddCIDR(cidr string, minPrefixLen, maxPrefixLen int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	effMax := maxPrefixLen
	if _, n, perr := net.ParseCIDR(cidr); perr == nil {
		if ones, _ := n.Mask.Size(); effMax < ones {
			effMax = ones
		}
	}
	alloc, err := NewBuddyAllocator(cidr, minPrefixLen, effMax)
	if err != nil {
		return fmt.Errorf("failed to create allocator for %s: %w", cidr, err)
	}
	for _, existing := range p.allocators {
		if overlaps(existing.root.Network, alloc.root.Network) {
			return fmt.Errorf("CIDR %s overlaps %s already in the pool", alloc.root.CIDR, existing.root.CIDR)
		}
	}
	alloc.SetQuarantine(p.quarantine)
	alloc.SetClock(p.now)
	if len(p.allocators) == 0 {
		p.minPrefix, p.maxPrefix = minPrefixLen, maxPrefixLen
	}
	p.allocators = append(p.allocators, alloc)
	return nil
}

func overlaps(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// Allocate tries each CIDR's allocator in order and returns the first success,
// so the first CIDR fills before the next is touched.
func (p *Pool) Allocate(requestedAddrs int, nodeName string) (*Block, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var lastErr error
	for _, a := range p.allocators {
		b, err := a.Allocate(requestedAddrs, nodeName)
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		return nil, fmt.Errorf("%w: pool has no CIDRs", ErrNoSpace)
	}
	return nil, fmt.Errorf("pool exhausted: %w", lastErr)
}

// AllocateSpread is Allocate that leaves the block's buddy free where it can;
// see BuddyAllocator.AllocateSpread. CIDRs are still tried in order, so the first
// fills first. A CIDR with no larger free block gives up a block whose buddy is
// taken before the next CIDR is touched.
func (p *Pool) AllocateSpread(requestedAddrs int, nodeName string) (*Block, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var lastErr error
	for _, a := range p.allocators {
		b, err := a.AllocateSpread(requestedAddrs, nodeName)
		if err != nil {
			b, err = a.Allocate(requestedAddrs, nodeName)
		}
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		return nil, fmt.Errorf("%w: pool has no CIDRs", ErrNoSpace)
	}
	return nil, fmt.Errorf("pool exhausted: %w", lastErr)
}

// AllocateDemand gives nodeName enough blocks to hold requestedAddrs addresses.
// The demand is rounded up to a multiple of the minimum block and split by its
// binary representation, so waste stays below one minimum block (e.g. 100 pods
// becomes /26 + /27 + /28). If a block of the right size is not available it
// falls back to two blocks of the next smaller size. On failure nothing stays
// allocated.
func (p *Pool) AllocateDemand(requestedAddrs int, nodeName string) ([]*Block, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.allocators) == 0 {
		return nil, fmt.Errorf("%w: pool has no CIDRs", ErrNoSpace)
	}
	minP, maxP := p.minPrefix, p.maxPrefix
	var got []*Block
	for _, prefix := range SplitDemand(requestedAddrs, minP, maxP) {
		blocks, err := p.allocatePrefixOrSplit(prefix, minP, nodeName)
		got = append(got, blocks...)
		if err != nil {
			for _, b := range got {
				_ = p.freeNowLocked(b)
			}
			return nil, err
		}
	}
	return got, nil
}

func (p *Pool) allocatePrefixOrSplit(prefix, minP int, nodeName string) ([]*Block, error) {
	lastErr := ErrNoSpace
	for _, a := range p.allocators {
		b, err := a.AllocatePrefix(prefix, nodeName)
		if err == nil {
			return []*Block{b}, nil
		}
		lastErr = err
	}
	if prefix >= minP {
		return nil, fmt.Errorf("pool exhausted: %w", lastErr)
	}
	first, err := p.allocatePrefixOrSplit(prefix+1, minP, nodeName)
	if err != nil {
		return nil, err
	}
	second, err := p.allocatePrefixOrSplit(prefix+1, minP, nodeName)
	return append(first, second...), err
}

// SplitDemand returns the prefix lengths, largest block first, that cover
// requestedAddrs with at most one minimum block of waste. Blocks are capped at
// maxPrefixLen, so very large demand yields several maximum-size blocks.
func SplitDemand(requestedAddrs, minPrefixLen, maxPrefixLen int) []int {
	unit := 1 << (32 - minPrefixLen)
	units := max((requestedAddrs+unit-1)/unit, 1)
	maxUnits := 1 << (minPrefixLen - maxPrefixLen)
	var out []int
	for ; units >= maxUnits; units -= maxUnits {
		out = append(out, maxPrefixLen)
	}
	for bit := maxUnits >> 1; bit >= 1; bit >>= 1 {
		if units&bit != 0 {
			level := 0
			for v := bit; v > 1; v >>= 1 {
				level++
			}
			out = append(out, minPrefixLen-level)
		}
	}
	return out
}

// AllocateBuddyOf gives nodeName the buddy of block if it is free, so the
// node's two blocks aggregate into one route.
func (p *Pool) AllocateBuddyOf(block *Block, nodeName string) (*Block, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	a := p.ownerLocked(block.CIDR)
	if a == nil {
		return nil, fmt.Errorf("block %s is not in the pool", block.CIDR)
	}
	return a.AllocateBuddyOf(block, nodeName)
}

// Reserve allocates the exact block cidr for nodeName. The controller uses it to
// rebuild the trees from NodeBlock objects on startup.
func (p *Pool) Reserve(cidr, nodeName string) (*Block, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	a := p.ownerLocked(cidr)
	if a == nil {
		return nil, fmt.Errorf("CIDR %s is not inside any pool CIDR", cidr)
	}
	return a.Reserve(cidr, nodeName)
}

// Find returns the allocated or quarantined block with exactly this CIDR.
func (p *Pool) Find(cidr string) *Block {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if a := p.ownerLocked(cidr); a != nil {
		return a.Find(cidr)
	}
	return nil
}

func (p *Pool) ownerLocked(cidr string) *BuddyAllocator {
	for _, a := range p.allocators {
		if a.Contains(cidr) {
			return a
		}
	}
	return nil
}

// Free returns a block to the allocator that owns it.
func (p *Pool) Free(block *Block) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if block == nil {
		return errors.New("nil block")
	}
	a := p.ownerLocked(block.CIDR)
	if a == nil {
		return fmt.Errorf("block %s is not in the pool", block.CIDR)
	}
	return a.Free(block)
}

func (p *Pool) freeNowLocked(block *Block) error {
	if a := p.ownerLocked(block.CIDR); a != nil {
		return a.FreeNow(block)
	}
	return fmt.Errorf("block %s is not in the pool", block.CIDR)
}

// Sweep releases expired quarantined blocks in every CIDR.
func (p *Pool) Sweep() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, a := range p.allocators {
		n += a.Sweep()
	}
	return n
}

// Defragment runs defragmentation on all allocators in the pool.
func (p *Pool) Defragment() []MergeEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var allEvents []MergeEvent
	for _, alloc := range p.allocators {
		allEvents = append(allEvents, alloc.Defragment()...)
	}
	return allEvents
}

// GetBlocksForNode returns every block allocated to nodeName across all CIDRs.
func (p *Pool) GetBlocksForNode(nodeName string) []*Block {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Block, 0, len(p.allocators))
	for _, a := range p.allocators {
		out = append(out, a.GetBlocksForNode(nodeName)...)
	}
	return out
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
		result.QuarantinedAddresses += stats.QuarantinedAddresses
		if stats.LargestFreeBlock > result.LargestFreeBlock {
			result.LargestFreeBlock = stats.LargestFreeBlock
		}
	}
	if result.FreeAddresses > 0 {
		result.ExternalFragmentation = 1 - float64(result.LargestFreeBlock)/float64(result.FreeAddresses)
	}

	return result
}

// PoolStatsResult holds aggregate statistics for the entire pool.
type PoolStatsResult struct {
	TotalAddresses        int
	AllocatedAddresses    int
	FreeAddresses         int
	QuarantinedAddresses  int
	LargestFreeBlock      int
	ExternalFragmentation float64
	CIDRStats             []AllocatorStats
}

// CIDRCount returns the number of CIDR ranges in the pool.
func (p *Pool) CIDRCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.allocators)
}

// AllocatePrefix allocates a block of exactly the given prefix length from the
// first CIDR that can provide it.
func (p *Pool) AllocatePrefix(prefix int) (*Block, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	lastErr := ErrNoSpace
	for _, a := range p.allocators {
		b, err := a.AllocatePrefix(prefix, "")
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
