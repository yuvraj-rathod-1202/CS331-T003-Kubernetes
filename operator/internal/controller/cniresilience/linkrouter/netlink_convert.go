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
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// rtnhFOnlink is RTNH_F_ONLINK. netlink.FLAG_ONLINK exists only on Linux, and
// these helpers compile everywhere so their tests run on any development host.
const rtnhFOnlink = 0x4

// The helpers below hold all the filtering and conversion between netlink
// objects and the package types. NetlinkHost lists only what they call owned,
// so nothing else on the host is ever touched.

// ownedRoute reports whether r is a steering route: Config.Table and Config.Protocol.
func ownedRoute(r netlink.Route, cfg Config) bool {
	return r.Table == cfg.Table && int(r.Protocol) == cfg.Protocol
}

// routeFrom converts r, which lives on the interface named linkName. Routes
// without a destination or gateway are not ones we install and report false.
func routeFrom(r netlink.Route, linkName string) (Route, bool) {
	if r.Dst == nil || r.Gw == nil {
		return Route{}, false
	}
	return Route{Dst: r.Dst.String(), Gateway: r.Gw.String(), Device: linkName}, true
}

// toNetlinkRoute builds the onlink route in the owned table and protocol.
func toNetlinkRoute(r Route, linkIndex int, cfg Config) (*netlink.Route, error) {
	_, dst, err := net.ParseCIDR(r.Dst)
	if err != nil {
		return nil, fmt.Errorf("invalid route destination %q: %w", r.Dst, err)
	}
	gw := net.ParseIP(r.Gateway)
	if gw == nil {
		return nil, fmt.Errorf("invalid gateway %q", r.Gateway)
	}
	return &netlink.Route{
		Dst:       dst,
		Gw:        gw,
		LinkIndex: linkIndex,
		Table:     cfg.Table,
		Protocol:  netlink.RouteProtocol(cfg.Protocol),
		Flags:     rtnhFOnlink,
	}, nil
}

// ownedRule reports whether r sits at one of the three owned priorities.
func ownedRule(r netlink.Rule, cfg Config) bool {
	switch r.Priority {
	case cfg.PrefExcludeDst, cfg.PrefExcludeSrc, cfg.PrefSteer:
		return true
	}
	return false
}

// ruleFrom converts r so that it compares equal to the Rule that was added.
func ruleFrom(r netlink.Rule) Rule {
	out := Rule{Priority: r.Priority, Table: r.Table, IPProto: r.IPProto}
	if r.Dport != nil {
		out.Dport = int(r.Dport.Start)
	}
	if r.Sport != nil {
		out.Sport = int(r.Sport.Start)
	}
	return out
}

// toNetlinkRule is the inverse of ruleFrom.
func toNetlinkRule(r Rule) *netlink.Rule {
	nr := netlink.NewRule()
	nr.Priority = r.Priority
	nr.Table = r.Table
	nr.IPProto = r.IPProto
	if r.Dport != 0 {
		nr.Dport = netlink.NewRulePortRange(uint16(r.Dport), uint16(r.Dport))
	}
	if r.Sport != 0 {
		nr.Sport = netlink.NewRulePortRange(uint16(r.Sport), uint16(r.Sport))
	}
	return nr
}
