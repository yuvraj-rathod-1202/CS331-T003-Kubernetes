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

package linkrouter

import (
	"reflect"
	"strings"
	"testing"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

// rep builds a report for node from compact edges "peer:p+,b-" (primary up,
// backup down). An omitted backup term leaves the backup result nil.
func rep(edges ...string) healthagent.Report {
	r := healthagent.Report{Peers: map[string]healthagent.PeerResult{}}
	for _, e := range edges {
		name, terms, _ := strings.Cut(e, ":")
		var pr healthagent.PeerResult
		for _, t := range strings.Split(terms, ",") {
			up := strings.HasSuffix(t, "+")
			switch t[0] {
			case 'p':
				pr.Primary = up
			case 'b':
				pr.Backup = &up
			}
		}
		r.Peers[name] = pr
	}
	return r
}

func in(failover, relay bool, reports map[string]healthagent.Report, cur map[string]Choice, peers ...string) Inputs {
	ps := map[string]Peer{}
	for _, p := range peers {
		ps[p] = Peer{Name: p}
	}
	return Inputs{Self: "a", Peers: ps, Reports: reports, LinkFailover: failover, LinkRelay: relay, Current: cur}
}

func TestDecide(t *testing.T) {
	P := remediationv1alpha1.LinkPathPrimary
	B := remediationv1alpha1.LinkPathBackup
	R := remediationv1alpha1.LinkPathRelay
	N := remediationv1alpha1.LinkPathNone
	type m = map[string]healthagent.Report
	tests := []struct {
		name string
		in   Inputs
		want map[string]Choice
	}{
		{"D1 all ok", in(true, true, m{"a": rep("b:p+,b+", "c:p+")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: P}, "c": {Path: P}}},
		{"D2 backup", in(true, true, m{"a": rep("b:p-,b+")}, nil, "b"),
			map[string]Choice{"b": {Path: B}}},
		{"D3 relay", in(true, true, m{"a": rep("b:p-,b-", "c:p+"), "c": rep("b:p+")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: R, Via: "c"}, "c": {Path: P}}},
		{"D4 relay not reachable over calico", in(true, true, m{"a": rep("b:p-,b-", "c:p-,b+"), "c": rep("b:p+")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: N}, "c": {Path: B}}},
		{"D5 relay cannot reach b", in(true, true, m{"a": rep("b:p-,b-", "c:p+"), "c": rep("b:p-,b-")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: N}}},
		{"D6 lowest name", in(true, true, m{"a": rep("b:p-,b-", "c:p+", "c2:p+"), "c": rep("b:p+"), "c2": rep("b:p+")}, nil, "b", "c", "c2"),
			map[string]Choice{"b": {Path: R, Via: "c"}}},
		{"D7 sticky", in(true, true, m{"a": rep("b:p-,b-", "c:p+", "c2:p+"), "c": rep("b:p+"), "c2": rep("b:p+")},
			map[string]Choice{"b": {Path: R, Via: "c2"}}, "b", "c", "c2"),
			map[string]Choice{"b": {Path: R, Via: "c2"}}},
		{"D8 relay report stale", in(true, true, m{"a": rep("b:p-,b-", "c:p+")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: N}}},
		{"D10 failover off", in(false, true, m{"a": rep("b:p-,b+")}, nil, "b"),
			map[string]Choice{"b": {Path: P}}},
		{"D11 relay off", in(true, false, m{"a": rep("b:p-,b-", "c:p+"), "c": rep("b:p+")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: N}}},
		{"D12 no entry for peer", in(true, true, m{"a": rep("c:p+")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: P}}},
		{"D13 nil backup, no relay", in(true, true, m{"a": rep("b:p-")}, nil, "b"),
			map[string]Choice{"b": {Path: N}}},
		{"D13 nil backup, relay", in(true, true, m{"a": rep("b:p-", "c:p+"), "c": rep("b:p+")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: R, Via: "c"}}},
		{"D14 relay itself needs relay", in(true, true, m{"a": rep("b:p-,b-", "c:p+"), "c": rep("b:p-,b-")}, nil, "b", "c"),
			map[string]Choice{"b": {Path: N}}},
		{"sticky ignored when no longer candidate", in(true, true, m{"a": rep("b:p-,b-", "c:p+", "c2:p+"), "c": rep("b:p+"), "c2": rep("b:p-,b-")},
			map[string]Choice{"b": {Path: R, Via: "c2"}}, "b", "c", "c2"),
			map[string]Choice{"b": {Path: R, Via: "c"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.in)
			if len(got) != len(tt.in.Peers) {
				t.Fatalf("got %d entries, want %d: %v", len(got), len(tt.in.Peers), got)
			}
			for peer, w := range tt.want {
				if !reflect.DeepEqual(got[peer], w) {
					t.Errorf("peer %s: got %+v, want %+v", peer, got[peer], w)
				}
			}
		})
	}
}
