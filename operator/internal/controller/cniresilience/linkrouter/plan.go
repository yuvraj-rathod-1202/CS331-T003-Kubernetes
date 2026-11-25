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
	"sort"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

const (
	tableMain = 254 // the main routing table
	protoTCP  = 6   // IP protocol number of TCP
)

// Config holds the identifiers of the routing objects the router owns.
type Config struct {
	Table          int // routing table holding the steering routes
	Protocol       int // route protocol marking routes as owned
	ProbePort      int // TCP port of the probe, kept off the steering table
	PrefExcludeDst int // rule priority: probe dport goes to the main table
	PrefExcludeSrc int // rule priority: probe sport goes to the main table
	PrefSteer      int // rule priority: everything else looks up Table
	FlannelDevice  string
	CalicoDevice   string
}

// DefaultConfig returns the values the design fixes.
func DefaultConfig() Config {
	return Config{
		Table:          200,
		Protocol:       100,
		ProbePort:      healthagent.Port,
		PrefExcludeDst: 90,
		PrefExcludeSrc: 91,
		PrefSteer:      100,
		FlannelDevice:  "flannel.1",
		CalicoDevice:   "tunl0",
	}
}

// Route is one steering route. It always lives in Config.Table with protocol
// Config.Protocol and is onlink.
type Route struct {
	Dst     string // destination CIDR, the identity of the route
	Gateway string
	Device  string
}

// Rule is one policy routing rule. Its identity is the whole struct.
type Rule struct {
	Priority int
	Table    int // 254 for the exclusions, Config.Table for steering
	IPProto  int // 6 (tcp) for the exclusions, 0 for steering
	Dport    int
	Sport    int
}

// Desired is the host state the router wants.
type Desired struct {
	Routes []Route // sorted by Dst
	Rules  []Rule  // sorted by Priority
}

// Host abstracts the kernel routing state, so Apply can be tested without
// netlink. Implementations list only what the router owns: Routes returns only
// routes in Config.Table with Config.Protocol, Rules only rules at the three
// owned priorities. Anything else on the host is never listed, and therefore
// never deleted by Apply or Cleanup.
type Host interface {
	Routes() ([]Route, error)
	ReplaceRoute(Route) error
	DeleteRoute(Route) error
	Rules() ([]Rule, error)
	AddRule(Rule) error
	DeleteRule(Rule) error
}

// Build turns the per-peer choices into the desired host state. The three
// rules are always present. A backup peer gets a route per Calico block via its
// flannel address; a relay peer gets a route per block via the relay's internal
// IP. Peers lacking the data for their path, and relays that are unknown or
// have no internal IP, get no route.
func Build(choices map[string]Choice, peers map[string]Peer, cfg Config) Desired {
	d := Desired{Rules: []Rule{
		{Priority: cfg.PrefExcludeDst, Table: tableMain, IPProto: protoTCP, Dport: cfg.ProbePort},
		{Priority: cfg.PrefExcludeSrc, Table: tableMain, IPProto: protoTCP, Sport: cfg.ProbePort},
		{Priority: cfg.PrefSteer, Table: cfg.Table},
	}}
	for name, ch := range choices {
		peer, ok := peers[name]
		if !ok {
			continue
		}
		var gateway, device string
		switch ch.Path {
		case remediationv1alpha1.LinkPathBackup:
			gateway, device = peer.FlannelAddr, cfg.FlannelDevice
		case remediationv1alpha1.LinkPathRelay:
			gateway, device = peers[ch.Via].InternalIP, cfg.CalicoDevice
		default:
			continue
		}
		if gateway == "" {
			continue
		}
		for _, block := range peer.CalicoBlocks {
			d.Routes = append(d.Routes, Route{Dst: block, Gateway: gateway, Device: device})
		}
	}
	sort.Slice(d.Routes, func(i, j int) bool { return d.Routes[i].Dst < d.Routes[j].Dst })
	sort.Slice(d.Rules, func(i, j int) bool { return d.Rules[i].Priority < d.Rules[j].Priority })
	return d
}

// Apply makes the host match want. Routes are diffed by Dst and replaced when
// the gateway or device differ; rules are diffed as whole structs. Apply
// attempts every operation even when some fail, and returns the failures
// combined with errors.Join.
func Apply(h Host, want Desired) error {
	var errs []error

	haveRoutes, err := h.Routes()
	if err != nil {
		errs = append(errs, err)
	} else {
		have := make(map[string]Route, len(haveRoutes))
		for _, r := range haveRoutes {
			have[r.Dst] = r
		}
		wanted := make(map[string]bool, len(want.Routes))
		for _, r := range want.Routes {
			wanted[r.Dst] = true
			if cur, ok := have[r.Dst]; ok && cur == r {
				continue
			}
			errs = append(errs, h.ReplaceRoute(r))
		}
		for _, r := range haveRoutes {
			if !wanted[r.Dst] {
				errs = append(errs, h.DeleteRoute(r))
			}
		}
	}

	haveRules, err := h.Rules()
	if err != nil {
		errs = append(errs, err)
	} else {
		have := make(map[Rule]bool, len(haveRules))
		for _, r := range haveRules {
			have[r] = true
		}
		wanted := make(map[Rule]bool, len(want.Rules))
		for _, r := range want.Rules {
			wanted[r] = true
			if !have[r] {
				errs = append(errs, h.AddRule(r))
			}
		}
		for _, r := range haveRules {
			if !wanted[r] {
				errs = append(errs, h.DeleteRule(r))
			}
		}
	}
	return errors.Join(errs...)
}

// Cleanup deletes every route and rule the host lists as owned. Like Apply it
// attempts everything and returns the combined error.
func Cleanup(h Host) error {
	var errs []error
	routes, err := h.Routes()
	if err != nil {
		errs = append(errs, err)
	}
	for _, r := range routes {
		errs = append(errs, h.DeleteRoute(r))
	}
	rules, err := h.Rules()
	if err != nil {
		errs = append(errs, err)
	}
	for _, r := range rules {
		errs = append(errs, h.DeleteRule(r))
	}
	return errors.Join(errs...)
}
