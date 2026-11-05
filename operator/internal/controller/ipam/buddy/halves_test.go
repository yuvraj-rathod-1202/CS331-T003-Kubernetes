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

func TestHalves(t *testing.T) {
	lo, hi, err := Halves("10.244.0.192/27")
	if err != nil || lo != "10.244.0.192/28" || hi != "10.244.0.208/28" {
		t.Fatalf("Halves(/27) = %s %s %v", lo, hi, err)
	}
	if _, _, err := Halves("10.0.0.1/32"); err == nil {
		t.Error("a /32 has no halves")
	}
	if o, err := OtherHalf("10.244.0.192/27", "10.244.0.208/28"); err != nil || o != "10.244.0.192/28" {
		t.Errorf("OtherHalf = %s %v", o, err)
	}
	if _, err := OtherHalf("10.244.0.192/27", "10.244.0.0/28"); err == nil {
		t.Error("a CIDR outside the block is not a half")
	}
}

func TestAllocateFromBuddyTakesOnlyWhatIsNeeded(t *testing.T) {
	a, err := NewBuddyAllocator("10.244.0.0/24", 28, 24)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.Reserve("10.244.0.96/27", "n1")
	if err != nil {
		t.Fatal(err)
	}
	// The buddy .64/27 is free; 16 addresses come from its half next to .96/27.
	got, err := a.AllocateFromBuddy(first, 16, "n1")
	if err != nil || got.CIDR != "10.244.0.80/28" {
		t.Fatalf("got %v %v, want 10.244.0.80/28", got, err)
	}
	if _, err := a.Reserve("10.244.0.64/28", "n1"); err != nil {
		t.Fatalf("the other half must stay free: %v", err)
	}
	// A request as large as the buddy takes it whole.
	second, _ := a.Reserve("10.244.0.0/27", "n2")
	if got, err := a.AllocateFromBuddy(second, 64, "n2"); err != nil || got.CIDR != "10.244.0.32/27" {
		t.Fatalf("got %v %v, want the whole buddy 10.244.0.32/27", got, err)
	}
}

func TestAllocateFromBuddyGrowsTowardsTheMergedRange(t *testing.T) {
	a, err := NewBuddyAllocator("10.244.0.0/24", 28, 24)
	if err != nil {
		t.Fatal(err)
	}
	// n1 owns .0/27 and the two /28s of .32/27: together one /26 range. The
	// buddy of a single /28 is taken, so only the /26's buddy .64/26 is free.
	first, _ := a.Reserve("10.244.0.0/27", "n1")
	if _, err := a.Reserve("10.244.0.32/28", "n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve("10.244.0.48/28", "n1"); err != nil {
		t.Fatal(err)
	}
	got, err := a.AllocateFromBuddy(first, 16, "n1")
	if err != nil || got.CIDR != "10.244.0.64/28" {
		t.Fatalf("got %v %v, want 10.244.0.64/28 next to the merged /26", got, err)
	}
	// Another node's block next to the range is not crossed.
	if _, err := a.Reserve("10.244.0.128/26", "n2"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve("10.244.0.80/28", "n1"); err != nil {
		t.Fatal(err)
	}
}
