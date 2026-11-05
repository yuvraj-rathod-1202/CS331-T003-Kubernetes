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
	"testing"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

// A live block of another (e.g. deleted or renamed) pool must not be handed out
// again by this pool.
func TestForeignBlocksAreReserved(t *testing.T) {
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}})
	foreign := nblock("n1", "10.244.0.0/24", "Active", 50, t0)
	foreign.Spec.PoolRef = "old-pool"
	p := planPool(&spec, nodes1(), nil, planInput{Now: t0, Foreign: []remediationv1alpha1.NodeBlock{foreign}})
	if len(p.New) != 1 {
		t.Fatalf("new = %v", p.New)
	}
	if strings.HasPrefix(p.New[0].CIDR, "10.244.0.") {
		t.Fatalf("allocated %s inside the foreign block 10.244.0.0/24", p.New[0].CIDR)
	}
}

// A pool CIDR that lies wholly inside a foreign block is not used at all.
func TestPoolCIDRInsideForeignBlockIsSkipped(t *testing.T) {
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{"10.244.0.0/24"}})
	foreign := nblock("n1", "10.244.0.0/22", "Active", 1, t0)
	foreign.Spec.PoolRef = "old-pool"
	p := planPool(&spec, nodes1(), nil, planInput{Now: t0, Foreign: []remediationv1alpha1.NodeBlock{foreign}})
	if len(p.New) != 0 || !p.Exhausted {
		t.Fatalf("new = %v exhausted = %v problems = %v", p.New, p.Exhausted, p.Problems)
	}
}

func TestInvalidReservedCIDRIsReported(t *testing.T) {
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}, ReservedCIDRs: []string{"10.96.0.0/122"}})
	_, problems := validCIDRs(&spec, nil)
	if len(problems) != 1 || !strings.Contains(problems[0], "10.96.0.0/122") {
		t.Fatalf("problems = %v", problems)
	}
}

// Non-IPv4 pool CIDRs and NodeBlocks are reported instead of panicking.
func TestNonIPv4CIDRsDoNotPanic(t *testing.T) {
	spec := withDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR, "fd00::/64"}})
	blocks := []remediationv1alpha1.NodeBlock{
		nblock("n1", "::ffff:10.244.0.0/120", "Active", 0, t0),
		nblock("n1", "fd00::/120", "Active", 0, t0),
	}
	p := planPool(&spec, nodes1(), blocks, planInput{Now: t0})
	if len(p.Problems) < 3 {
		t.Fatalf("problems = %v", p.Problems)
	}
}

// Explicit zeros from the API server are kept; omitted fields are defaulted
// there, and in-process callers still get the defaults from withDefaults.
func TestWithAPIDefaultsKeepsExplicitZeros(t *testing.T) {
	s := withAPIDefaults(remediationv1alpha1.IPPoolSpec{CIDRs: []string{testPoolCIDR}})
	if s.QuarantineSeconds != 0 || s.ShrinkCooldownSeconds != 0 || s.TriggerFloor != 0 || s.LowWaterPercent != 0 {
		t.Fatalf("zeros overwritten: %+v", s)
	}
	if s.MinBlockSize != 28 || s.MaxBlockSize != 22 {
		t.Fatalf("required defaults missing: %+v", s)
	}
	if d := withDefaults(remediationv1alpha1.IPPoolSpec{}); d.QuarantineSeconds != 60 {
		t.Fatalf("withDefaults changed: %+v", d)
	}
}
