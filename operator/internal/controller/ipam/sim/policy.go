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

// Package sim evaluates pod IP allocation policies on synthetic workloads.
//
// The buddy policy runs the controller's own planning code (ipam.PlanPool) on
// NodeBlock objects, so the evaluation measures the real logic. The baselines
// are the usual alternatives: a fixed /24 per node (kubeadm's default), a tuned
// fixed /25, and Calico-style 64-address blocks handed out on demand.
//
// This is a model, not a cluster: pods are counted, not scheduled, and the node
// agent is a fixed delay. It shows how the policies compare under a workload;
// it does not show real pod start latency.
package sim

import (
	"math"
	"math/rand"
)

// Policy owns the address blocks of every node and the pods placed in them.
type Policy interface {
	Name() string
	// Place tries to start k pods on node n and returns how many got an address.
	Place(n, k int) int
	// Remove stops k pods on node n, chosen uniformly.
	Remove(n, k int)
	// Churn stops each pod on every node independently with probability p.
	Churn(p float64)
	// Starved tells the policy that node n just failed to start pods for lack
	// of addresses (the kubelet's "no IP addresses available" event).
	Starved(n int)
	// Tick runs the policy's control loop at simulated time t (seconds).
	Tick(t float64)
	Pods(n int) int
	// Allocated is the number of addresses handed to nodes, used or not.
	Allocated() int
	// Routes is how many route prefixes a node holds towards the others.
	Routes() int
	// Ops counts block changes: creations, drains and releases.
	Ops() int
	// Admitted is how many nodes have any address space at all.
	Admitted() int
}

// binomial draws from Binomial(n, p): exact for small n, normal approximation
// beyond that.
func binomial(rng *rand.Rand, n int, p float64) int {
	if n <= 0 || p <= 0 {
		return 0
	}
	if p >= 1 {
		return n
	}
	if n < 40 {
		k := 0
		for range n {
			if rng.Float64() < p {
				k++
			}
		}
		return k
	}
	mean, sd := float64(n)*p, math.Sqrt(float64(n)*p*(1-p))
	k := int(math.Round(mean + sd*rng.NormFloat64()))
	return max(0, min(n, k))
}

// pickWeighted returns an index chosen with probability proportional to w, or
// -1 when all weights are zero.
func pickWeighted(rng *rand.Rand, w []int) int {
	total := 0
	for _, x := range w {
		total += x
	}
	if total <= 0 {
		return -1
	}
	r := rng.Intn(total)
	for i, x := range w {
		if r < x {
			return i
		}
		r -= x
	}
	return len(w) - 1
}
