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

package sim

import (
	"math"
	"math/rand"
	"time"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

// Tick is the simulated time step.
const Tick = 5 * time.Second

// podLifetime is the mean life of a pod that is not scaled down explicitly; the
// churn that makes host-local's round-robin leave blocks half empty.
const podLifetime = 10 * time.Minute

// warmup is excluded from the averages, so the cold start does not count.
const warmup = 2 * time.Minute

// Scenario is a workload: how many pods each node should run over time.
type Scenario struct {
	Name     string
	Nodes    int
	MaxPods  int
	Pool     []string
	Duration time.Duration
	// Demand returns the target pod count of node n at time t. It is built per
	// seed so random scenarios are repeatable.
	Demand func(rng *rand.Rand) func(n int, t time.Duration) int
}

// PolicyName selects an allocation policy.
type PolicyName string

// The policies compared.
const (
	Buddy    PolicyName = "buddy"
	Static24 PolicyName = "static-/24"
	Static25 PolicyName = "static-/25"
	Calico   PolicyName = "calico-64"
	// BuddyNoStarve is buddy without the emergency grow, an ablation.
	BuddyNoStarve PolicyName = "buddy-no-emergency"
	// BuddyNoShrink is buddy without draining, an ablation.
	BuddyNoShrink PolicyName = "buddy-no-shrink"
)

// AllPolicies is the main comparison.
var AllPolicies = []PolicyName{Buddy, Static24, Static25, Calico}

func newPolicy(p PolicyName, s Scenario, rng *rand.Rand) Policy {
	spec := remediationv1alpha1.IPPoolSpec{CIDRs: s.Pool}
	cfg := BuddyConfig{Spec: spec, PromoteDelay: 10 * time.Second, DrainSettle: 10 * time.Second}
	switch p {
	case Static24:
		return newStatic(string(p), rng, s.Nodes, 256, s.Pool)
	case Static25:
		return newStatic(string(p), rng, s.Nodes, 128, s.Pool)
	case Calico:
		return newCalico(rng, s.Nodes, s.Pool)
	case BuddyNoStarve:
		cfg.NoStarve = true
	case BuddyNoShrink:
		cfg.Spec.DisableShrink = true
	}
	return newBuddy(string(p), rng, s.Nodes, s.MaxPods, cfg)
}

// Point is one sample of the run.
type Point struct {
	Minutes   float64
	Pods      int
	Wanted    int
	Allocated int
	Unmet     int
	Routes    int
}

// Result is one policy on one scenario and seed.
type Result struct {
	Scenario string
	Policy   string
	Seed     int64
	Series   []Point
	// Averages over the run after warmup.
	Utilization   float64 // pods / allocated addresses
	AllocPerNode  float64 // addresses held per node
	UnmetPodSecs  float64 // pod-seconds of demand that got no address
	PeakUnmet     int
	PoolPercent   float64 // peak share of the pool handed out
	PeakRoutes    int
	Ops           int
	Admitted      int
	PodsServedPct float64 // pods run / pods wanted, over the run
}

// Run simulates one policy on one scenario.
func Run(s Scenario, name PolicyName, seed int64) Result {
	rng := rand.New(rand.NewSource(seed))
	demand := s.Demand(rng)
	// A separate stream for the policy keeps the workload identical across policies.
	pol := newPolicy(name, s, rand.New(rand.NewSource(seed+1)))
	deathP := 1 - math.Exp(-Tick.Seconds()/podLifetime.Seconds())
	pool := float64(poolTotal(s.Pool))

	r := Result{Scenario: s.Name, Policy: string(name), Seed: seed}
	var utilSum, allocSum float64
	var samples int
	var served, wanted float64

	// The controller runs before the first pods arrive, as it does for a node
	// that has just joined.
	for t := -6 * Tick; t < 0; t += Tick {
		pol.Tick(t.Seconds())
	}
	for t := time.Duration(0); t <= s.Duration; t += Tick {
		pol.Churn(deathP)
		unmet, pods, want := 0, 0, 0
		for n := 0; n < s.Nodes; n++ {
			d := demand(n, t)
			have := pol.Pods(n)
			switch {
			case d > have:
				got := pol.Place(n, d-have)
				if got < d-have {
					unmet += d - have - got
					pol.Starved(n)
				}
			case d < have:
				pol.Remove(n, have-d)
			}
			pods += pol.Pods(n)
			want += d
		}
		pol.Tick(t.Seconds())

		alloc := pol.Allocated()
		routes := pol.Routes()
		r.Series = append(r.Series, Point{Minutes: t.Minutes(), Pods: pods, Wanted: want, Allocated: alloc, Unmet: unmet, Routes: routes})
		r.UnmetPodSecs += float64(unmet) * Tick.Seconds()
		r.PeakUnmet = max(r.PeakUnmet, unmet)
		r.PeakRoutes = max(r.PeakRoutes, routes)
		r.PoolPercent = max(r.PoolPercent, 100*float64(alloc)/pool)
		if t >= warmup {
			if alloc > 0 {
				utilSum += float64(pods) / float64(alloc)
			}
			allocSum += float64(alloc) / float64(s.Nodes)
			samples++
			served += float64(pods)
			wanted += float64(want)
		}
	}
	if samples > 0 {
		r.Utilization = 100 * utilSum / float64(samples)
		r.AllocPerNode = allocSum / float64(samples)
	}
	if wanted > 0 {
		r.PodsServedPct = 100 * served / wanted
	}
	r.Ops = pol.Ops()
	r.Admitted = pol.Admitted()
	return r
}
