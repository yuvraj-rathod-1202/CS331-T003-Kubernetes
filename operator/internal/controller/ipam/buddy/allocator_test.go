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
	"testing"
)

// =============================================================================
// Unit tests for the buddy allocator. Cover these scenarios:
//
// 1. Basic allocation and free
// 2. Splitting: request smaller than the root block
// 3. Merging: free both buddies and verify they merge
// 4. Best-fit: request 100 addrs → gets /25 (128), not /24 (256)
// 5. Exhaustion: fill the entire CIDR and verify error
// 6. Defragmentation: free scattered blocks and verify merging
// 7. Multi-CIDR pool: allocate across two CIDRs
// 8. Alignment: every allocated block is aligned to its size
// =============================================================================

func TestNewBuddyAllocator(t *testing.T) {
	// TODO: Test that the allocator initializes correctly.
	// - Valid CIDR parses without error.
	// - Invalid CIDR returns error.
	// - minPrefixLen < maxPrefixLen returns error.

	_, err := NewBuddyAllocator("10.244.0.0/16", 28, 22)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = NewBuddyAllocator("invalid", 28, 22)
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestAllocateBasic(t *testing.T) {
	// TODO: Test basic allocation.
	// 1. Create a /24 allocator (256 addresses).
	// 2. Allocate a block for 64 addresses → expect a /26.
	// 3. Verify block.State == BlockAllocated.
	// 4. Verify block.PrefixLen == 26.
	// 5. Verify block.NodeName is set.

	t.Skip("TODO: implement after Allocate is done")
}

func TestAllocateBestFit(t *testing.T) {
	// TODO: Test the professor's example.
	// Node A needs 100 pods → should get /25 (128 addrs), not /24 (256).
	// Node B needs 500 pods → should get /23 (512 addrs).

	t.Skip("TODO: implement after Allocate is done")
}

func TestAllocateSplitting(t *testing.T) {
	// TODO: Test that a /24 splits into two /25s when a /25 is requested.
	// Then allocate the other /25 and verify both are allocated.

	t.Skip("TODO: implement after Allocate is done")
}

func TestFreeAndMerge(t *testing.T) {
	// TODO: Test buddy merging.
	// 1. Allocate two /25 blocks from a /24.
	// 2. Free one → it stays as a free /25.
	// 3. Free the other → both merge back into a free /24.

	t.Skip("TODO: implement after Free is done")
}

func TestExhaustion(t *testing.T) {
	// TODO: Test pool exhaustion.
	// 1. Create a small /28 allocator (16 addresses, minBlock = /28).
	// 2. Allocate the entire block.
	// 3. Try to allocate again → expect error.

	t.Skip("TODO: implement after Allocate is done")
}

func TestDefragment(t *testing.T) {
	// TODO: Test defragmentation.
	// 1. Allocate several small blocks.
	// 2. Free them in a scattered order.
	// 3. Call Defragment() and verify merge events.

	t.Skip("TODO: implement after Defragment is done")
}

func TestPoolMultiCIDR(t *testing.T) {
	// TODO: Test multi-CIDR pool.
	// 1. Create a pool with two CIDRs.
	// 2. Fill the first CIDR.
	// 3. Allocate again → should come from the second CIDR.

	t.Skip("TODO: implement after Pool.Allocate is done")
}

func TestBlockAlignment(t *testing.T) {
	// TODO: Verify every allocated block is aligned to its size.
	// A /26 (64 addrs) must start at a multiple of 64.

	t.Skip("TODO: implement after Allocate is done")
}
