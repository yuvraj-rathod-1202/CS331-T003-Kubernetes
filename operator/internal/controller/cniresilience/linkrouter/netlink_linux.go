//go:build linux

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
	"github.com/vishvananda/netlink"
)

// NetlinkHost is the Host backed by the kernel through netlink. It needs
// CAP_NET_ADMIN in the host network namespace.
type NetlinkHost struct{ Config Config }

// Routes lists the routes in Config.Table with Config.Protocol.
func (h NetlinkHost) Routes() ([]Route, error) {
	filter := &netlink.Route{Table: h.Config.Table, Protocol: netlink.RouteProtocol(h.Config.Protocol)}
	rs, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(rs))
	for _, r := range rs {
		if !ownedRoute(r, h.Config) {
			continue
		}
		name := ""
		if l, err := netlink.LinkByIndex(r.LinkIndex); err == nil {
			name = l.Attrs().Name
		}
		if rt, ok := routeFrom(r, name); ok {
			out = append(out, rt)
		}
	}
	return out, nil
}

func (h NetlinkHost) route(r Route) (*netlink.Route, error) {
	l, err := netlink.LinkByName(r.Device)
	if err != nil {
		return nil, err
	}
	return toNetlinkRoute(r, l.Attrs().Index, h.Config)
}

// ReplaceRoute installs or updates a steering route.
func (h NetlinkHost) ReplaceRoute(r Route) error {
	nr, err := h.route(r)
	if err != nil {
		return err
	}
	return netlink.RouteReplace(nr)
}

// DeleteRoute removes a steering route. The device may be gone already, so the
// route is deleted by destination, table and protocol when the link is missing.
func (h NetlinkHost) DeleteRoute(r Route) error {
	nr, err := h.route(r)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return err
		}
		nr, err = toNetlinkRoute(r, 0, h.Config)
		if err != nil {
			return err
		}
	}
	return netlink.RouteDel(nr)
}

// Rules lists the rules at the three owned priorities.
func (h NetlinkHost) Rules() ([]Rule, error) {
	rs, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	var out []Rule
	for _, r := range rs {
		if ownedRule(r, h.Config) {
			out = append(out, ruleFrom(r))
		}
	}
	return out, nil
}

// AddRule adds a rule.
func (h NetlinkHost) AddRule(r Rule) error { return netlink.RuleAdd(toNetlinkRule(r)) }

// DeleteRule removes a rule.
func (h NetlinkHost) DeleteRule(r Rule) error { return netlink.RuleDel(toNetlinkRule(r)) }

var _ Host = NetlinkHost{}
