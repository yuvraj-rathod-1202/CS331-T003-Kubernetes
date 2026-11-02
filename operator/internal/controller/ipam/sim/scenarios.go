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
)

// ramp brings a node from nothing to target over the first two minutes, so the
// run measures steady state and not a step that no scheduler would produce.
func ramp(t time.Duration, target int) int {
	return int(float64(target) * math.Min(1, t.Minutes()/2))
}

// Scenarios returns the evaluation workloads. Pools are /16 unless stated.
func Scenarios() []Scenario {
	pool16 := []string{"10.0.0.0/16"}
	return []Scenario{
		{
			Name: "steady", Nodes: 50, MaxPods: 110, Pool: pool16, Duration: 30 * time.Minute,
			Demand: func(*rand.Rand) func(int, time.Duration) int {
				return func(_ int, t time.Duration) int { return ramp(t, 80) }
			},
		},
		{
			// A tenth of the nodes are hot, the rest nearly idle. Nodes allow 250 pods.
			Name: "skewed", Nodes: 50, MaxPods: 250, Pool: pool16, Duration: 30 * time.Minute,
			Demand: func(rng *rand.Rand) func(int, time.Duration) int {
				hot := map[int]bool{}
				for _, i := range rng.Perm(50)[:5] {
					hot[i] = true
				}
				return func(n int, t time.Duration) int {
					if hot[n] {
						return ramp(t, 220)
					}
					return ramp(t, 30)
				}
			},
		},
		{
			// Every node ramps from 20 to 200 pods, holds, and scales back down.
			Name: "burst", Nodes: 50, MaxPods: 250, Pool: pool16, Duration: 60 * time.Minute,
			Demand: func(*rand.Rand) func(int, time.Duration) int {
				return func(_ int, t time.Duration) int {
					m := t.Minutes()
					switch {
					case m < 10:
						return 20
					case m < 15:
						return 20 + int(180*(m-10)/5)
					case m < 30:
						return 200
					case m < 35:
						return 200 - int(180*(m-30)/5)
					}
					return 20
				}
			},
		},
		{
			// Each node follows its own wave, with random phase and amplitude.
			Name: "wave", Nodes: 50, MaxPods: 250, Pool: pool16, Duration: 120 * time.Minute,
			Demand: func(rng *rand.Rand) func(int, time.Duration) int {
				phase, amp, base := make([]float64, 50), make([]float64, 50), make([]float64, 50)
				for i := range phase {
					phase[i] = rng.Float64() * 2 * math.Pi
					amp[i] = 20 + rng.Float64()*80
					base[i] = 40 + rng.Float64()*60
				}
				return func(n int, t time.Duration) int {
					return max(0, int(base[n]+amp[n]*math.Sin(2*math.Pi*t.Minutes()/40+phase[n])))
				}
			},
		},
		{
			// A small pool (4096 addresses) shared by 40 nodes: four hot, the rest light.
			Name: "tight-pool", Nodes: 40, MaxPods: 250, Pool: []string{"10.0.0.0/20"}, Duration: 30 * time.Minute,
			Demand: func(*rand.Rand) func(int, time.Duration) int {
				return func(n int, t time.Duration) int {
					if n < 4 {
						return ramp(t, 200)
					}
					return ramp(t, 40)
				}
			},
		},
	}
}

// SweepScenario is the node-count sweep: nodes with 60 pods each in a /16.
func SweepScenario(nodes int) Scenario {
	return Scenario{
		Name: "sweep", Nodes: nodes, MaxPods: 110, Pool: []string{"10.0.0.0/16"}, Duration: 10 * time.Minute,
		Demand: func(*rand.Rand) func(int, time.Duration) int {
			return func(_ int, t time.Duration) int { return ramp(t, 60) }
		},
	}
}
