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
	"cmp"
	"fmt"
	"net"
	"slices"
)

// Aggregate merges CIDRs that are buddies into their parent prefix, repeatedly,
// and returns the minimal sorted set of prefixes. A node that owns both halves
// of a block needs one route for it instead of two.
func Aggregate(cidrs []string) ([]string, error) {
	type pfx struct {
		base uint32
		len  int
	}
	set := make(map[pfx]struct{}, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", c, err)
		}
		l, _ := n.Mask.Size()
		set[pfx{ipToUint32(n.IP), l}] = struct{}{}
	}
	for changed := true; changed; {
		changed = false
		for p := range set {
			if p.len == 0 {
				continue
			}
			size := uint32(1) << (32 - p.len)
			sibling := pfx{p.base ^ size, p.len}
			if _, ok := set[sibling]; ok {
				delete(set, p)
				delete(set, sibling)
				set[pfx{p.base &^ size, p.len - 1}] = struct{}{}
				changed = true
				break
			}
		}
	}
	out := make([]pfx, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b pfx) int {
		if c := cmp.Compare(a.base, b.base); c != 0 {
			return c
		}
		return cmp.Compare(a.len, b.len)
	})
	res := make([]string, len(out))
	for i, p := range out {
		res[i] = (&net.IPNet{IP: uint32ToIP(p.base), Mask: net.CIDRMask(p.len, 32)}).String()
	}
	return res, nil
}
