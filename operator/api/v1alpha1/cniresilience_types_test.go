package v1alpha1

import "testing"

func TestLinkSwitchesDefaultToOn(t *testing.T) {
	off := false
	cases := []struct {
		spec            CNIResilienceSpec
		failover, relay bool
	}{
		{CNIResilienceSpec{}, true, true},
		{CNIResilienceSpec{LinkFailover: &off}, false, true},
		{CNIResilienceSpec{LinkRelay: &off}, true, false},
	}
	for i, c := range cases {
		if c.spec.LinkFailoverEnabled() != c.failover || c.spec.LinkRelayEnabled() != c.relay {
			t.Errorf("case %d: got %v/%v", i, c.spec.LinkFailoverEnabled(), c.spec.LinkRelayEnabled())
		}
	}
}
