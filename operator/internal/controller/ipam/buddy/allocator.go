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
// Freed blocks can be held in quarantine for a configurable time before they
// merge and become allocatable again, so stale routes or in-flight packets
// cannot be delivered to a new owner.
//
// Reference: Linux kernel mm/page_alloc.c buddy allocator.
package buddy

import (
	"cmp"
	"errors"
	"fmt"
	"math/bits"
	"net"
	"slices"
	"sync"
	"time"
)

var (
	// ErrNoSpace is returned when no free block can satisfy a request.
	ErrNoSpace = errors.New("no free block available")
	// ErrBuddyUnavailable is returned by AllocateBuddyOf when the buddy is not free.
	ErrBuddyUnavailable = errors.New("buddy block is not free")
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
	// BlockQuarantined means the block was freed but is held back until its
	// quarantine expires. It does not merge and cannot be allocated.
	BlockQuarantined
)

func (s BlockState) String() string {
	switch s {
	case BlockFree:
		return "Free"
	case BlockAllocated:
		return "Allocated"
	case BlockSplit:
		return "Split"
	case BlockQuarantined:
		return "Quarantined"
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

	base      uint32
	releaseAt time.Time
}

// Addresses returns the number of addresses in this block.
func (b *Block) Addresses() int {
	return 1 << (32 - b.PrefixLen)
}

func newBlock(base uint32, prefix int, parent *Block) *Block {
	ip := uint32ToIP(base)
	mask := net.CIDRMask(prefix, 32)
	n := &net.IPNet{IP: ip, Mask: mask}
	return &Block{
		CIDR:      n.String(),
		Network:   n,
		PrefixLen: prefix,
		Parent:    parent,
		base:      base,
	}
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

	// freeBlocks indexes free blocks by prefix length.
	freeBlocks map[int]map[*Block]struct{}

	// quarantined holds freed blocks waiting for their quarantine to expire.
	quarantined map[*Block]struct{}

	quarantine time.Duration
	now        func() time.Time
}

// NewBuddyAllocator creates a new allocator for the given CIDR range.
//
// Parameters:
//   - cidr: the CIDR to manage (e.g. "10.244.0.0/16")
//   - minPrefixLen: smallest allocatable block prefix (e.g. 28 for /28)
//   - maxPrefixLen: largest allocatable block prefix (e.g. 22 for /22)
func NewBuddyAllocator(cidr string, minPrefixLen, maxPrefixLen int) (*BuddyAllocator, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	if network.IP.To4() == nil {
		return nil, fmt.Errorf("CIDR %q is not IPv4", cidr)
	}

	rootPrefix, _ := network.Mask.Size()

	if maxPrefixLen < rootPrefix {
		return nil, fmt.Errorf("maxPrefixLen /%d is larger than the pool /%d", maxPrefixLen, rootPrefix)
	}
	if minPrefixLen < maxPrefixLen {
		return nil, fmt.Errorf("minPrefixLen /%d must be >= maxPrefixLen /%d", minPrefixLen, maxPrefixLen)
	}
	if minPrefixLen > 32 {
		return nil, fmt.Errorf("minPrefixLen /%d is out of range", minPrefixLen)
	}

	root := newBlock(ipToUint32(network.IP), rootPrefix, nil)
	a := &BuddyAllocator{
		root:         root,
		minPrefixLen: minPrefixLen,
		maxPrefixLen: maxPrefixLen,
		freeBlocks:   make(map[int]map[*Block]struct{}),
		quarantined:  make(map[*Block]struct{}),
		now:          time.Now,
	}
	a.addFree(root)
	return a, nil
}

// SetQuarantine sets how long freed blocks are held before they can be reused.
// Zero (the default) releases blocks immediately.
func (a *BuddyAllocator) SetQuarantine(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.quarantine = d
}

// SetClock replaces the time source. Used by tests and the simulator.
func (a *BuddyAllocator) SetClock(now func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = now
}

// Contains reports whether the allocator's root CIDR contains the block.
func (a *BuddyAllocator) Contains(cidr string) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	p, _ := n.Mask.Size()
	return p >= a.root.PrefixLen && a.root.Network.Contains(n.IP)
}

func (a *BuddyAllocator) addFree(b *Block) {
	set := a.freeBlocks[b.PrefixLen]
	if set == nil {
		set = make(map[*Block]struct{})
		a.freeBlocks[b.PrefixLen] = set
	}
	set[b] = struct{}{}
}

func (a *BuddyAllocator) removeFree(b *Block) {
	delete(a.freeBlocks[b.PrefixLen], b)
}

// lowestFree returns the free block with the lowest address at a level.
func (a *BuddyAllocator) lowestFree(prefix int) *Block {
	var best *Block
	for b := range a.freeBlocks[prefix] {
		if best == nil || b.base < best.base {
			best = b
		}
	}
	return best
}

// split turns a free block into two free children.
func (a *BuddyAllocator) split(b *Block) (*Block, *Block) {
	a.removeFree(b)
	b.State = BlockSplit
	half := uint32(1) << (31 - b.PrefixLen)
	b.Left = newBlock(b.base, b.PrefixLen+1, b)
	b.Right = newBlock(b.base+half, b.PrefixLen+1, b)
	a.addFree(b.Left)
	a.addFree(b.Right)
	return b.Left, b.Right
}

func (a *BuddyAllocator) prefixFor(requestedAddrs int) (int, error) {
	if requestedAddrs < 1 {
		requestedAddrs = 1
	}
	prefix := min(32-bits.Len(uint(requestedAddrs-1)), a.minPrefixLen)
	if prefix < a.maxPrefixLen {
		return 0, fmt.Errorf("request for %d addresses exceeds the largest block /%d", requestedAddrs, a.maxPrefixLen)
	}
	return prefix, nil
}

// Allocate finds the smallest free block that can hold requestedAddrs addresses
// and returns it. Ties are broken by lowest address. A larger free block is
// split only when no block of the right size is free.
func (a *BuddyAllocator) Allocate(requestedAddrs int, nodeName string) (*Block, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()

	prefix, err := a.prefixFor(requestedAddrs)
	if err != nil {
		return nil, err
	}
	return a.allocatePrefixLocked(prefix, nodeName)
}

func (a *BuddyAllocator) allocatePrefixLocked(prefix int, nodeName string) (*Block, error) {
	for p := prefix; p >= a.root.PrefixLen; p-- {
		b := a.lowestFree(p)
		if b == nil {
			continue
		}
		for b.PrefixLen < prefix {
			b, _ = a.split(b)
		}
		a.removeFree(b)
		b.State = BlockAllocated
		b.NodeName = nodeName
		return b, nil
	}
	return nil, fmt.Errorf("%w for /%d in %s", ErrNoSpace, prefix, a.root.CIDR)
}

// AllocateSpread is Allocate for a node's first block. It prefers to split the
// smallest larger free block, which leaves the new block's buddy free, so the
// node can later double in place and its two halves aggregate into one route.
// Allocate would take an exact-size free block first, which is always some other
// node's buddy. It returns ErrNoSpace when no larger block is free.
func (a *BuddyAllocator) AllocateSpread(requestedAddrs int, nodeName string) (*Block, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()

	prefix, err := a.prefixFor(requestedAddrs)
	if err != nil {
		return nil, err
	}
	for p := prefix - 1; p >= a.root.PrefixLen; p-- {
		b := a.lowestFree(p)
		if b == nil {
			continue
		}
		for b.PrefixLen < prefix {
			b, _ = a.split(b)
		}
		a.removeFree(b)
		b.State = BlockAllocated
		b.NodeName = nodeName
		return b, nil
	}
	return nil, fmt.Errorf("%w: no larger free block for /%d in %s", ErrNoSpace, prefix, a.root.CIDR)
}

// AllocatePrefix allocates a block of exactly the given prefix length.
func (a *BuddyAllocator) AllocatePrefix(prefix int, nodeName string) (*Block, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()
	if prefix > a.minPrefixLen || prefix < a.maxPrefixLen {
		return nil, fmt.Errorf("prefix /%d outside allowed range /%d../%d", prefix, a.maxPrefixLen, a.minPrefixLen)
	}
	return a.allocatePrefixLocked(prefix, nodeName)
}

// Buddy returns the sibling of a block, or nil for the root.
func (b *Block) Buddy() *Block {
	if b.Parent == nil {
		return nil
	}
	if b.Parent.Left == b {
		return b.Parent.Right
	}
	return b.Parent.Left
}

// AllocateBuddyOf gives nodeName the buddy of block when that buddy is free.
// The node then owns both halves, which aggregate into the parent prefix
// (one route instead of two).
func (a *BuddyAllocator) AllocateBuddyOf(block *Block, nodeName string) (*Block, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()

	buddy := block.Buddy()
	if buddy == nil || buddy.State != BlockFree {
		return nil, ErrBuddyUnavailable
	}
	a.removeFree(buddy)
	buddy.State = BlockAllocated
	buddy.NodeName = nodeName
	return buddy, nil
}

// Reserve allocates the exact block named by cidr for nodeName. It is used to
// rebuild allocator state from persisted NodeBlock objects after a restart.
func (a *BuddyAllocator) Reserve(cidr, nodeName string) (*Block, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	prefix, _ := n.Mask.Size()
	base := ipToUint32(n.IP)
	if !a.Contains(cidr) {
		return nil, fmt.Errorf("%s is outside %s", cidr, a.root.CIDR)
	}

	// Dry walk first so a failure leaves the tree untouched.
	cur := a.root
	for cur.State == BlockSplit && cur.PrefixLen < prefix {
		cur = a.childFor(cur, base)
	}
	if cur.State != BlockFree {
		return nil, fmt.Errorf("%s overlaps an allocated or quarantined block (%s %s)", cidr, cur.CIDR, cur.State)
	}
	if cur.PrefixLen > prefix {
		return nil, fmt.Errorf("%s overlaps an existing block %s", cidr, cur.CIDR)
	}

	for cur.PrefixLen < prefix {
		l, r := a.split(cur)
		cur = l
		if base >= r.base {
			cur = r
		}
	}
	a.removeFree(cur)
	cur.State = BlockAllocated
	cur.NodeName = nodeName
	return cur, nil
}

func (a *BuddyAllocator) childFor(b *Block, base uint32) *Block {
	if base >= b.Right.base {
		return b.Right
	}
	return b.Left
}

// Find returns the allocated or quarantined block with exactly this CIDR.
func (a *BuddyAllocator) Find(cidr string) *Block {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil || !a.Contains(cidr) {
		return nil
	}
	prefix, _ := n.Mask.Size()
	base := ipToUint32(n.IP)
	cur := a.root
	for cur.State == BlockSplit && cur.PrefixLen < prefix {
		cur = a.childFor(cur, base)
	}
	if cur.PrefixLen == prefix && cur.base == base && cur.State != BlockFree && cur.State != BlockSplit {
		return cur
	}
	return nil
}

// Free returns a block to the allocator. With a quarantine configured the
// block is held back; otherwise it is released and merged with its buddy
// immediately.
func (a *BuddyAllocator) Free(block *Block) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkOwned(block); err != nil {
		return err
	}
	if a.quarantine > 0 {
		block.State = BlockQuarantined
		block.NodeName = ""
		block.releaseAt = a.now().Add(a.quarantine)
		a.quarantined[block] = struct{}{}
		return nil
	}
	a.releaseLocked(block)
	return nil
}

// FreeNow releases a block immediately, ignoring quarantine. It is meant for
// rolling back an allocation that was never handed out.
func (a *BuddyAllocator) FreeNow(block *Block) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkOwned(block); err != nil {
		return err
	}
	a.releaseLocked(block)
	return nil
}

func (a *BuddyAllocator) checkOwned(block *Block) error {
	if block == nil {
		return errors.New("nil block")
	}
	if block.State != BlockAllocated {
		return fmt.Errorf("block %s is %s, not Allocated", block.CIDR, block.State)
	}
	top := block
	for top.Parent != nil {
		top = top.Parent
	}
	if top != a.root {
		return fmt.Errorf("block %s does not belong to %s", block.CIDR, a.root.CIDR)
	}
	return nil
}

// releaseLocked marks b free and merges it upward while its buddy is free.
func (a *BuddyAllocator) releaseLocked(b *Block) {
	delete(a.quarantined, b)
	b.State = BlockFree
	b.NodeName = ""
	a.addFree(b)
	for b.Parent != nil {
		buddy := b.Buddy()
		if buddy == nil || buddy.State != BlockFree {
			break
		}
		p := b.Parent
		a.removeFree(p.Left)
		a.removeFree(p.Right)
		p.Left, p.Right = nil, nil
		p.State = BlockFree
		a.addFree(p)
		b = p
	}
}

// Sweep releases quarantined blocks whose quarantine has expired and returns
// how many were released.
func (a *BuddyAllocator) Sweep() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sweepLocked()
}

func (a *BuddyAllocator) sweepLocked() int {
	if len(a.quarantined) == 0 {
		return 0
	}
	now := a.now()
	var due []*Block
	for b := range a.quarantined {
		if !b.releaseAt.After(now) {
			due = append(due, b)
		}
	}
	slices.SortFunc(due, func(a, b *Block) int { return cmp.Compare(a.base, b.base) })
	for _, b := range due {
		a.releaseLocked(b)
	}
	return len(due)
}

// Defragment merges any free buddy pairs that were left unmerged and returns
// the merges performed. Release already merges eagerly, so this is normally a
// no-op and exists as a consistency check.
func (a *BuddyAllocator) Defragment() []MergeEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()

	var events []MergeEvent
	var walk func(b *Block)
	walk = func(b *Block) {
		if b == nil || b.State != BlockSplit {
			return
		}
		walk(b.Left)
		walk(b.Right)
		if b.Left.State == BlockFree && b.Right.State == BlockFree {
			a.removeFree(b.Left)
			a.removeFree(b.Right)
			events = append(events, MergeEvent{MergedBlock: b, LeftChild: b.Left, RightChild: b.Right})
			b.Left, b.Right = nil, nil
			b.State = BlockFree
			a.addFree(b)
		}
	}
	walk(a.root)
	return events
}

// AllocatorStats holds statistics about the allocator's current state.
type AllocatorStats struct {
	RootCIDR              string
	TotalAddresses        int
	AllocatedAddresses    int
	FreeAddresses         int
	QuarantinedAddresses  int
	FragmentedBlocks      int // Number of free buddy pairs that were not merged
	AllocatedBlocks       int
	LargestFreeBlock      int
	ExternalFragmentation float64     // 1 - largest free block / total free
	FreeBlocksByLevel     map[int]int // prefix length → count of free blocks
}

// Stats returns current allocation statistics.
func (a *BuddyAllocator) Stats() AllocatorStats {
	a.mu.RLock()
	defer a.mu.RUnlock()

	s := AllocatorStats{
		RootCIDR:          a.root.CIDR,
		TotalAddresses:    a.root.Addresses(),
		FreeBlocksByLevel: make(map[int]int),
	}
	var walk func(b *Block)
	walk = func(b *Block) {
		switch b.State {
		case BlockFree:
			s.FreeAddresses += b.Addresses()
			s.FreeBlocksByLevel[b.PrefixLen]++
			if b.Addresses() > s.LargestFreeBlock {
				s.LargestFreeBlock = b.Addresses()
			}
		case BlockAllocated:
			s.AllocatedAddresses += b.Addresses()
			s.AllocatedBlocks++
		case BlockQuarantined:
			s.QuarantinedAddresses += b.Addresses()
		case BlockSplit:
			if b.Left.State == BlockFree && b.Right.State == BlockFree {
				s.FragmentedBlocks++
			}
			walk(b.Left)
			walk(b.Right)
		}
	}
	walk(a.root)
	if s.FreeAddresses > 0 {
		s.ExternalFragmentation = 1 - float64(s.LargestFreeBlock)/float64(s.FreeAddresses)
	}
	return s
}

// GetAllocatedBlocks returns all currently allocated blocks in address order.
func (a *BuddyAllocator) GetAllocatedBlocks() []*Block {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var out []*Block
	var walk func(b *Block)
	walk = func(b *Block) {
		if b == nil {
			return
		}
		if b.State == BlockAllocated {
			out = append(out, b)
			return
		}
		if b.State == BlockSplit {
			walk(b.Left)
			walk(b.Right)
		}
	}
	walk(a.root)
	return out
}

// GetBlocksForNode returns all blocks allocated to a specific node.
func (a *BuddyAllocator) GetBlocksForNode(nodeName string) []*Block {
	var out []*Block
	for _, b := range a.GetAllocatedBlocks() {
		if b.NodeName == nodeName {
			out = append(out, b)
		}
	}
	return out
}

func ipToUint32(ip net.IP) uint32 {
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func uint32ToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4()
}
