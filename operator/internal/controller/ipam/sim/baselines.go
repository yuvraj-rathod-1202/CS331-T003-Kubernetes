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
	"math/rand"
	"net"
)

func cidrSize(c string) int {
	_, n, err := net.ParseCIDR(c)
	if err != nil {
		return 0
	}
	ones, bits := n.Mask.Size()
	return 1 << (bits - ones)
}

func poolTotal(cidrs []string) int {
	t := 0
	for _, c := range cidrs {
		t += cidrSize(c)
	}
	return t
}

// static gives every node a fixed block of perNode addresses, in the order the
// nodes join, until the pool has no more blocks of that size. It models the
// kube-controller-manager node IPAM: /24 by default, or a tuned /25.
type static struct {
	name     string
	rng      *rand.Rand
	perNode  int
	usable   int
	admitted int
	pods     []int
	nodes    int
}

func newStatic(name string, rng *rand.Rand, nodes, perNode int, pool []string) *static {
	adm := 0
	for _, c := range pool {
		adm += cidrSize(c) / perNode
	}
	return &static{name: name, rng: rng, perNode: perNode, usable: perNode - 3,
		admitted: min(adm, nodes), pods: make([]int, nodes), nodes: nodes}
}

func (s *static) Name() string { return s.name }
func (s *static) Place(n, k int) int {
	if n >= s.admitted {
		return 0
	}
	got := min(k, s.usable-s.pods[n])
	s.pods[n] += got
	return got
}
func (s *static) Remove(n, k int) { s.pods[n] = max(0, s.pods[n]-k) }
func (s *static) Churn(p float64) {
	for n := range s.pods {
		s.pods[n] -= binomial(s.rng, s.pods[n], p)
	}
}
func (s *static) Starved(int)    {}
func (s *static) Tick(float64)   {}
func (s *static) Pods(n int) int { return s.pods[n] }
func (s *static) Allocated() int { return s.admitted * s.perNode }
func (s *static) Routes() int    { return s.admitted }
func (s *static) Ops() int       { return 0 }
func (s *static) Admitted() int  { return s.admitted }

// calico models block-affinity IPAM: a node takes a 64-address block when it
// has no free address and the pool still has a block; a block goes back to the
// pool when its last pod ends. No address is lost to overhead.
type calico struct {
	rng      *rand.Rand
	blocks   [][]int // per node, pods per block
	free     int     // free blocks in the pool
	ops      int
	everUsed []bool
}

const calicoBlock = 64

func newCalico(rng *rand.Rand, nodes int, pool []string) *calico {
	return &calico{rng: rng, blocks: make([][]int, nodes), free: poolTotal(pool) / calicoBlock, everUsed: make([]bool, nodes)}
}

func (c *calico) Name() string { return "calico-64" }
func (c *calico) Place(n, k int) int {
	placed := 0
	for placed < k {
		w := make([]int, len(c.blocks[n]))
		for i, u := range c.blocks[n] {
			w[i] = calicoBlock - u
		}
		i := pickWeighted(c.rng, w)
		if i < 0 {
			if c.free == 0 {
				break
			}
			c.free--
			c.ops++
			c.blocks[n] = append(c.blocks[n], 0)
			c.everUsed[n] = true
			i = len(c.blocks[n]) - 1
		}
		c.blocks[n][i]++
		placed++
	}
	return placed
}

func (c *calico) release(n int) {
	kept := c.blocks[n][:0]
	for _, u := range c.blocks[n] {
		if u == 0 {
			c.free++
			c.ops++
			continue
		}
		kept = append(kept, u)
	}
	c.blocks[n] = kept
}

func (c *calico) Remove(n, k int) {
	for range k {
		i := pickWeighted(c.rng, c.blocks[n])
		if i < 0 {
			break
		}
		c.blocks[n][i]--
	}
	c.release(n)
}

func (c *calico) Churn(p float64) {
	for n := range c.blocks {
		for i, u := range c.blocks[n] {
			c.blocks[n][i] -= binomial(c.rng, u, p)
		}
		c.release(n)
	}
}

func (c *calico) Starved(int)  {}
func (c *calico) Tick(float64) {}
func (c *calico) Pods(n int) int {
	t := 0
	for _, u := range c.blocks[n] {
		t += u
	}
	return t
}
func (c *calico) Allocated() int {
	t := 0
	for _, b := range c.blocks {
		t += len(b) * calicoBlock
	}
	return t
}
func (c *calico) Routes() int {
	t := 0
	for _, b := range c.blocks {
		t += len(b)
	}
	return t
}
func (c *calico) Ops() int { return c.ops }
func (c *calico) Admitted() int {
	t := 0
	for _, u := range c.everUsed {
		if u {
			t++
		}
	}
	return t
}
