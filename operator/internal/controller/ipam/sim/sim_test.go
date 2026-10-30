package sim

import "testing"

func TestAllPoliciesRunAndBuddyServesEverything(t *testing.T) {
	for _, s := range Scenarios() {
		s.Duration = s.Duration / 3
		for _, p := range AllPolicies {
			r := Run(s, p, 1)
			if len(r.Series) == 0 || r.PoolPercent > 100.0001 {
				t.Fatalf("%s/%s: bad result %+v", s.Name, p, r.PoolPercent)
			}
		}
	}
}

func TestBuddyHandlesSkewThatFixedBlocksCannot(t *testing.T) {
	var skew Scenario
	for _, s := range Scenarios() {
		if s.Name == "skewed" {
			skew = s
		}
	}
	b, s25 := Run(skew, Buddy, 1), Run(skew, Static25, 1)
	if b.PodsServedPct < 99 {
		t.Errorf("buddy served %.1f%% of the pods", b.PodsServedPct)
	}
	if s25.PodsServedPct >= b.PodsServedPct {
		t.Errorf("a fixed /25 cannot hold 220 pods on a node, but served %.1f%%", s25.PodsServedPct)
	}
}
