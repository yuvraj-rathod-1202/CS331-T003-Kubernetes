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

import "testing"

// IPv4-mapped IPv6 and IPv6 CIDRs are rejected instead of panicking.
func TestNonIPv4CIDRsAreRejected(t *testing.T) {
	a, err := NewBuddyAllocator("10.244.0.0/16", 28, 22)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"::ffff:10.244.0.0/120", "fd00::/64"} {
		if a.Contains(c) {
			t.Errorf("Contains(%s) = true", c)
		}
		if _, err := a.Reserve(c, "n1"); err == nil {
			t.Errorf("Reserve(%s) succeeded", c)
		}
		if a.Find(c) != nil {
			t.Errorf("Find(%s) found a block", c)
		}
		if _, err := Aggregate([]string{c}); err == nil {
			t.Errorf("Aggregate(%s) succeeded", c)
		}
	}
	if _, err := NewBuddyAllocator("::ffff:10.244.0.0/112", 28, 22); err == nil {
		t.Error("NewBuddyAllocator accepted an IPv4-mapped IPv6 CIDR")
	}
}

// A demand that cannot be met leaves the pool as free as before, even when it
// fails inside a nested split (/26 -> /27 -> /28 where only one /28 is free).
func TestFailedDemandLeaksNothing(t *testing.T) {
	p := NewPool()
	if err := p.AddCIDR("10.0.0.0/26", 28, 26); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := p.AllocatePrefix(28); err != nil {
			t.Fatal(err)
		}
	}
	before := p.PoolStats().FreeAddresses
	if _, err := p.AllocateDemand(64, "n1"); err == nil {
		t.Fatal("expected the demand to fail")
	}
	if after := p.PoolStats().FreeAddresses; after != before {
		t.Fatalf("free addresses %d -> %d after a failed demand", before, after)
	}
}
