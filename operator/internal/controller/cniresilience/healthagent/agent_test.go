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

package healthagent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("n%02d", i)
	}
	return out
}

// ringEdges gives one round of results: broken(from, to) says if that probe fails.
func ringEdges(nodes []string, round int64, broken func(from, to string) bool) []Edge {
	var edges []Edge
	for _, node := range nodes {
		target := RingTarget(nodes, node, round)
		edges = append(edges, Edge{From: node, To: target, OK: !broken(node, target)})
	}
	return edges
}

func TestRingCostsExactlyOneProbePerNode(t *testing.T) {
	for n := 3; n <= 30; n++ {
		nodes := names(n)
		for round := range int64(len(RingOffsets(n))) {
			seen := map[string]bool{}
			for _, node := range nodes {
				target := RingTarget(nodes, node, round)
				if target == node || seen[target] {
					t.Fatalf("n=%d round=%d: bad target %s", n, round, target)
				}
				seen[target] = true
			}
		}
	}
}

// A broken node is found in any single round with 2r != n, with or without its own report.
func TestBrokenNodeFoundInOneRound(t *testing.T) {
	for n := 3; n <= 30; n++ {
		nodes := names(n)
		for round, offset := range RingOffsets(n) {
			if 2*offset == n {
				continue
			}
			for _, bad := range nodes {
				edges := ringEdges(nodes, int64(round), func(a, b string) bool { return a == bad || b == bad })
				got := Classify(nodes, edges, nil)
				if !slices.Equal(got.BrokenNodes, []string{bad}) || len(got.BrokenLinks) != 0 || got.Ambiguous {
					t.Fatalf("n=%d round=%d bad=%s: %+v", n, round, bad, got)
				}

				withoutOwn := slices.DeleteFunc(slices.Clone(edges), func(e Edge) bool { return e.From == bad })
				got = Classify(nodes, withoutOwn, map[string]bool{bad: true})
				if !slices.Equal(got.BrokenNodes, []string{bad}) || got.Ambiguous {
					t.Fatalf("silent n=%d round=%d bad=%s: %+v", n, round, bad, got)
				}
			}
		}
	}
}

// One full sweep sees every link, on odd and even rings, and never blames a whole node.
func TestEveryBrokenLinkIsFoundInOneSweep(t *testing.T) {
	for n := 3; n <= 30; n++ {
		nodes := names(n)
		for i, a := range nodes {
			for _, b := range nodes[i+1:] {
				broken := func(x, y string) bool { return (x == a && y == b) || (x == b && y == a) }
				var sweep []Edge
				for round := range int64(len(RingOffsets(n))) {
					sweep = append(sweep, ringEdges(nodes, round, broken)...)
				}
				got := Classify(nodes, sweep, nil)
				if len(got.BrokenNodes) != 0 || got.Ambiguous || !slices.Equal(got.BrokenLinks, [][2]string{{a, b}}) {
					t.Fatalf("n=%d %s-%s: %+v", n, a, b, got)
				}
			}
		}
	}
}

func TestNeverGuessesWhenEverythingFails(t *testing.T) {
	nodes := names(3)
	got := Classify(nodes, ringEdges(nodes, 0, func(_, _ string) bool { return true }), nil)
	if len(got.BrokenNodes) != 0 || !got.Ambiguous {
		t.Fatalf("%+v", got)
	}
	two := names(2)
	got = Classify(two, []Edge{{two[0], two[1], false}, {two[1], two[0], false}}, nil)
	if len(got.BrokenNodes) != 0 || !got.Ambiguous {
		t.Fatalf("2 nodes: %+v", got)
	}
}

func TestStreakNeedsThresholdBothWays(t *testing.T) {
	s := streak{healthy: true}
	for i, ok := range []bool{false, false, true, false, false, false, true, true, true} {
		s.observe(ok, 3)
		want := i < 5 || i == 8
		if s.healthy != want {
			t.Fatalf("step %d: healthy=%v", i, s.healthy)
		}
	}
}

// Every request must use a new connection: a reused one survives faults that stop new
// connections, which hid a broken node in the lab on 2026-10-31.
func TestProbeOpensNewConnectionEachRequest(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			_, _ = w.Write(make([]byte, largeBodyBytes))
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(Port))
	if err != nil {
		t.Skipf("port %d busy: %v", Port, err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()

	for range 2 {
		if !probe(context.Background(), "127.0.0.1", time.Second) {
			t.Fatal("probe failed")
		}
	}
	if got := connections.Load(); got != 4 {
		t.Fatalf("2 probes of 2 requests used %d connections, want 4", got)
	}
}
