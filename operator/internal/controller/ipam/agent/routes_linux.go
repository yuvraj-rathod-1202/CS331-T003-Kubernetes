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

package agent

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// NetlinkRoutes manages routes in the host's main table through netlink. It
// needs CAP_NET_ADMIN and the host network namespace.
type NetlinkRoutes struct{}

func toNetlink(r Route) (*netlink.Route, error) {
	_, dst, err := net.ParseCIDR(r.Dst)
	if err != nil {
		return nil, fmt.Errorf("invalid route destination %q: %w", r.Dst, err)
	}
	gw := net.ParseIP(r.Gateway)
	if gw == nil {
		return nil, fmt.Errorf("invalid gateway %q", r.Gateway)
	}
	return &netlink.Route{Dst: dst, Gw: gw, Protocol: netlink.RouteProtocol(RouteProtocol)}, nil
}

// Managed lists routes whose protocol is RouteProtocol.
func (NetlinkRoutes) Managed() ([]Route, error) {
	filter := &netlink.Route{Protocol: netlink.RouteProtocol(RouteProtocol)}
	rs, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(rs))
	for _, r := range rs {
		if r.Dst == nil || r.Gw == nil {
			continue
		}
		out = append(out, Route{Dst: r.Dst.String(), Gateway: r.Gw.String()})
	}
	return out, nil
}

// Replace installs or updates a route.
func (NetlinkRoutes) Replace(r Route) error {
	nr, err := toNetlink(r)
	if err != nil {
		return err
	}
	return netlink.RouteReplace(nr)
}

// Delete removes a route.
func (NetlinkRoutes) Delete(r Route) error {
	nr, err := toNetlink(r)
	if err != nil {
		return err
	}
	return netlink.RouteDel(nr)
}
