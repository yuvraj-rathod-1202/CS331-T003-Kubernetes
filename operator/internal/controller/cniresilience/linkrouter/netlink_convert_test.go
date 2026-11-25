package linkrouter

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestOwnedRoute(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name  string
		table int
		proto int
		want  bool
	}{
		{"ours", 200, 100, true},
		{"other table", 254, 100, false},
		{"ipam agent protocol", 200, 99, false},
		{"main table proto 99", 254, 99, false},
	}
	for _, tc := range tests {
		r := netlink.Route{Table: tc.table, Protocol: netlink.RouteProtocol(tc.proto)}
		if got := ownedRoute(r, cfg); got != tc.want {
			t.Errorf("%s: ownedRoute = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRouteFrom(t *testing.T) {
	_, dst, _ := net.ParseCIDR("10.244.1.0/26")
	r := netlink.Route{Dst: dst, Gw: net.ParseIP("10.10.1.0")}
	got, ok := routeFrom(r, "flannel.1")
	if !ok || got != (Route{Dst: "10.244.1.0/26", Gateway: "10.10.1.0", Device: "flannel.1"}) {
		t.Errorf("routeFrom = %+v, %v", got, ok)
	}
	if _, ok := routeFrom(netlink.Route{Dst: dst}, "x"); ok {
		t.Error("route without gateway should be skipped")
	}
	if _, ok := routeFrom(netlink.Route{Gw: net.ParseIP("1.1.1.1")}, "x"); ok {
		t.Error("route without destination should be skipped")
	}
}

func TestToNetlinkRoute(t *testing.T) {
	cfg := DefaultConfig()
	nr, err := toNetlinkRoute(Route{Dst: "10.244.1.0/26", Gateway: "10.10.1.0"}, 7, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if nr.Table != 200 || int(nr.Protocol) != 100 || nr.Flags != rtnhFOnlink || nr.LinkIndex != 7 {
		t.Errorf("unexpected route %+v", nr)
	}
	if !ownedRoute(*nr, cfg) {
		t.Error("route we build must be owned")
	}
	if _, err := toNetlinkRoute(Route{Dst: "bad", Gateway: "10.0.0.1"}, 1, cfg); err == nil {
		t.Error("expected error for bad destination")
	}
	if _, err := toNetlinkRoute(Route{Dst: "10.0.0.0/24", Gateway: "bad"}, 1, cfg); err == nil {
		t.Error("expected error for bad gateway")
	}
}

func TestOwnedRule(t *testing.T) {
	cfg := DefaultConfig()
	for _, p := range []int{90, 91, 100} {
		if !ownedRule(netlink.Rule{Priority: p}, cfg) {
			t.Errorf("pref %d should be owned", p)
		}
	}
	for _, p := range []int{0, 95, 32766, 32767} {
		if ownedRule(netlink.Rule{Priority: p}, cfg) {
			t.Errorf("pref %d should not be owned", p)
		}
	}
}

func TestRuleRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	for _, r := range Build(nil, nil, cfg).Rules {
		nr := toNetlinkRule(r)
		if !ownedRule(*nr, cfg) {
			t.Errorf("rule %+v not owned after conversion", r)
		}
		if got := ruleFrom(*nr); got != r {
			t.Errorf("round trip: got %+v, want %+v", got, r)
		}
	}
}
