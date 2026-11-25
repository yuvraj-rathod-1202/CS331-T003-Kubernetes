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
	"errors"
	"reflect"
	"sort"
	"testing"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

// fakeHost is an in-memory Host. Routes and rules it lists are the owned ones;
// foreignRoutes and foreignRules hold items the real Host would filter out, so
// tests can check they are never touched. calls records every mutation.
type fakeHost struct {
	routes routeSet
	rules  []Rule

	foreignRoutes []string
	foreignRules  []Rule

	calls []string

	// failOn makes the named call (e.g. "ReplaceRoute") return an error.
	failOn map[string]error
}

// routeSet is the owned route set, keyed by Dst.
type routeSet map[string]Route

func newFakeHost() *fakeHost {
	return &fakeHost{routes: routeSet{}, failOn: map[string]error{}}
}

func (f *fakeHost) record(call string) error {
	f.calls = append(f.calls, call)
	return f.failOn[call]
}

func (f *fakeHost) Routes() ([]Route, error) {
	out := make([]Route, 0, len(f.routes))
	for _, r := range f.routes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dst < out[j].Dst })
	return out, nil
}

func (f *fakeHost) ReplaceRoute(r Route) error {
	if err := f.record("ReplaceRoute"); err != nil {
		return err
	}
	f.routes[r.Dst] = r
	return nil
}

func (f *fakeHost) DeleteRoute(r Route) error {
	if err := f.record("DeleteRoute"); err != nil {
		return err
	}
	delete(f.routes, r.Dst)
	return nil
}

func (f *fakeHost) Rules() ([]Rule, error) {
	return append([]Rule(nil), f.rules...), nil
}

func (f *fakeHost) AddRule(r Rule) error {
	if err := f.record("AddRule"); err != nil {
		return err
	}
	f.rules = append(f.rules, r)
	return nil
}

func (f *fakeHost) DeleteRule(r Rule) error {
	if err := f.record("DeleteRule"); err != nil {
		return err
	}
	for i, x := range f.rules {
		if x == r {
			f.rules = append(f.rules[:i], f.rules[i+1:]...)
			break
		}
	}
	return nil
}

func wantRules() []Rule {
	return []Rule{
		{Priority: 90, Table: 254, IPProto: 6, Dport: healthagent.Port},
		{Priority: 91, Table: 254, IPProto: 6, Sport: healthagent.Port},
		{Priority: 100, Table: 200},
	}
}

func TestBuildRoutesPerChoice(t *testing.T) {
	peers := map[string]Peer{
		"b": {Name: "b", InternalIP: "192.168.1.11", FlannelAddr: "10.244.1.0",
			CalicoBlocks: []string{"172.16.25.128/26", "172.16.30.0/26"}},
		"c": {Name: "c", InternalIP: "192.168.1.12", FlannelAddr: "10.244.2.0",
			CalicoBlocks: []string{"172.16.40.0/26"}},
		"d": {Name: "d", InternalIP: "192.168.1.13", FlannelAddr: "10.244.3.0",
			CalicoBlocks: []string{"172.16.50.0/26"}},
	}
	choices := map[string]Choice{
		"b": {Path: remediationv1alpha1.LinkPathBackup},
		"c": {Path: remediationv1alpha1.LinkPathRelay, Via: "b"},
		"d": {Path: remediationv1alpha1.LinkPathPrimary},
	}
	got := Build(choices, peers, DefaultConfig())

	wantRoutes := []Route{
		{Dst: "172.16.25.128/26", Gateway: "10.244.1.0", Device: "flannel.1"},
		{Dst: "172.16.30.0/26", Gateway: "10.244.1.0", Device: "flannel.1"},
		{Dst: "172.16.40.0/26", Gateway: "192.168.1.11", Device: "tunl0"},
	}
	if !reflect.DeepEqual(got.Routes, wantRoutes) {
		t.Errorf("routes = %+v, want %+v", got.Routes, wantRoutes)
	}
	if !reflect.DeepEqual(got.Rules, wantRules()) {
		t.Errorf("rules = %+v, want %+v", got.Rules, wantRules())
	}
}

func TestBuildSkipsIncompletePeers(t *testing.T) {
	peers := map[string]Peer{
		"b": {Name: "b", InternalIP: "192.168.1.11", CalicoBlocks: []string{"172.16.25.0/26"}}, // no FlannelAddr
		"c": {Name: "c", InternalIP: "192.168.1.12", FlannelAddr: "10.244.2.0"},                // no blocks
		"d": {Name: "d", InternalIP: "192.168.1.13", CalicoBlocks: []string{"172.16.50.0/26"}},
		"e": {Name: "e", CalicoBlocks: []string{"172.16.60.0/26"}}, // relay without InternalIP
	}
	choices := map[string]Choice{
		"b": {Path: remediationv1alpha1.LinkPathBackup},
		"c": {Path: remediationv1alpha1.LinkPathBackup},
		"d": {Path: remediationv1alpha1.LinkPathRelay, Via: "missing"},
		"x": {Path: remediationv1alpha1.LinkPathRelay, Via: "e"},
	}
	// peer d relays via an unknown node; peer x is not in peers.
	got := Build(choices, peers, DefaultConfig())
	if len(got.Routes) != 0 {
		t.Errorf("routes = %+v, want none", got.Routes)
	}
	if !reflect.DeepEqual(got.Rules, wantRules()) {
		t.Errorf("rules = %+v, want %+v", got.Rules, wantRules())
	}

	peers["f"] = Peer{Name: "f", CalicoBlocks: []string{"172.16.70.0/26"}}
	got = Build(map[string]Choice{"f": {Path: remediationv1alpha1.LinkPathRelay, Via: "e"}}, peers, DefaultConfig())
	if len(got.Routes) != 0 {
		t.Errorf("relay via peer without InternalIP produced %+v", got.Routes)
	}
}

func desiredOne() Desired {
	return Desired{
		Routes: []Route{{Dst: "172.16.25.128/26", Gateway: "10.244.1.0", Device: "flannel.1"}},
		Rules:  wantRules(),
	}
}

func TestApplyFromEmptyAddsEverything(t *testing.T) {
	h := newFakeHost()
	want := desiredOne()
	if err := Apply(h, want); err != nil {
		t.Fatal(err)
	}
	gotRoutes, _ := h.Routes()
	if !reflect.DeepEqual(gotRoutes, want.Routes) {
		t.Errorf("routes = %+v, want %+v", gotRoutes, want.Routes)
	}
	gotRules, _ := h.Rules()
	if !reflect.DeepEqual(gotRules, want.Rules) {
		t.Errorf("rules = %+v, want %+v", gotRules, want.Rules)
	}
}

func TestApplyNoopWhenEqual(t *testing.T) {
	h := newFakeHost()
	want := desiredOne()
	if err := Apply(h, want); err != nil {
		t.Fatal(err)
	}
	h.calls = nil
	if err := Apply(h, want); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 {
		t.Errorf("calls = %v, want none", h.calls)
	}
}

func TestApplyRemovesStaleRoute(t *testing.T) {
	h := newFakeHost()
	h.routes["172.16.99.0/26"] = Route{Dst: "172.16.99.0/26", Gateway: "10.244.9.0", Device: "flannel.1"}
	h.rules = wantRules()
	if err := Apply(h, Desired{Rules: wantRules()}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"DeleteRoute"}) {
		t.Errorf("calls = %v, want one DeleteRoute", h.calls)
	}
	if len(h.routes) != 0 {
		t.Errorf("routes = %+v, want none", h.routes)
	}
}

func TestApplyReplacesChangedGateway(t *testing.T) {
	h := newFakeHost()
	h.routes["172.16.25.128/26"] = Route{Dst: "172.16.25.128/26", Gateway: "10.244.7.0", Device: "flannel.1"}
	h.rules = wantRules()
	want := desiredOne()
	if err := Apply(h, want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"ReplaceRoute"}) {
		t.Errorf("calls = %v, want one ReplaceRoute", h.calls)
	}
	if got := h.routes["172.16.25.128/26"]; got != want.Routes[0] {
		t.Errorf("route = %+v, want %+v", got, want.Routes[0])
	}
}

func TestApplyAttemptsAllAndJoinsErrors(t *testing.T) {
	h := newFakeHost()
	boom := errors.New("boom")
	h.failOn["ReplaceRoute"] = boom
	want := Desired{
		Routes: []Route{
			{Dst: "172.16.1.0/26", Gateway: "10.244.1.0", Device: "flannel.1"},
			{Dst: "172.16.2.0/26", Gateway: "10.244.1.0", Device: "flannel.1"},
		},
		Rules: wantRules(),
	}
	err := Apply(h, want)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap boom", err)
	}
	var replaces, adds int
	for _, c := range h.calls {
		switch c {
		case "ReplaceRoute":
			replaces++
		case "AddRule":
			adds++
		}
	}
	if replaces != 2 || adds != 3 {
		t.Errorf("replaces = %d, adds = %d, want 2 and 3", replaces, adds)
	}
}

func TestApplyAndCleanupLeaveForeignItemsAlone(t *testing.T) {
	h := newFakeHost()
	foreignRule := Rule{Priority: 95, Table: 254}
	h.foreignRules = []Rule{foreignRule}
	h.foreignRoutes = []string{"main:10.0.0.0/8", "table200-proto99:172.16.77.0/26"}

	if err := Apply(h, desiredOne()); err != nil {
		t.Fatal(err)
	}
	checkForeign := func(stage string) {
		t.Helper()
		if !reflect.DeepEqual(h.foreignRules, []Rule{foreignRule}) {
			t.Errorf("%s: foreign rules = %+v", stage, h.foreignRules)
		}
		if len(h.foreignRoutes) != 2 {
			t.Errorf("%s: foreign routes = %v", stage, h.foreignRoutes)
		}
		for _, c := range h.calls {
			if c == "DeleteRoute" || c == "DeleteRule" {
				t.Errorf("%s: unexpected %s", stage, c)
			}
		}
	}
	checkForeign("after Apply")

	if err := Cleanup(h); err != nil {
		t.Fatal(err)
	}
	h.calls = nil // Cleanup legitimately deletes owned items
	checkForeign("after Cleanup")
}

func TestCleanupRemovesOwnedItems(t *testing.T) {
	h := newFakeHost()
	if err := Apply(h, desiredOne()); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(h); err != nil {
		t.Fatal(err)
	}
	routes, _ := h.Routes()
	rules, _ := h.Rules()
	if len(routes) != 0 || len(rules) != 0 {
		t.Errorf("left routes = %+v, rules = %+v", routes, rules)
	}
}
