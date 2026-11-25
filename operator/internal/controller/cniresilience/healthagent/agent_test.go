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
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("n%02d", i)
	}
	return out
}

// allEdges gives one edge per ordered pair: broken(from, to) says if that probe fails.
func allEdges(nodes []string, broken func(from, to string) bool) []Edge {
	var edges []Edge
	for _, from := range nodes {
		for _, to := range nodes {
			if from != to {
				edges = append(edges, Edge{From: from, To: to, OK: !broken(from, to)})
			}
		}
	}
	return edges
}

// A broken node is found in any single round with 2r != n, with or without its own report.
func TestBrokenNodeFoundInOneRound(t *testing.T) {
	for n := 3; n <= 30; n++ {
		nodes := names(n)
		for _, bad := range nodes {
			edges := allEdges(nodes, func(a, b string) bool { return a == bad || b == bad })
			got := Classify(nodes, edges, nil)
			if !slices.Equal(got.BrokenNodes, []string{bad}) || len(got.BrokenLinks) != 0 || got.Ambiguous {
				t.Fatalf("n=%d bad=%s: %+v", n, bad, got)
			}

			withoutOwn := slices.DeleteFunc(slices.Clone(edges), func(e Edge) bool { return e.From == bad })
			got = Classify(nodes, withoutOwn, map[string]bool{bad: true})
			if !slices.Equal(got.BrokenNodes, []string{bad}) || got.Ambiguous {
				t.Fatalf("silent n=%d bad=%s: %+v", n, bad, got)
			}
		}
	}
}

// One round sees every link, on odd and even rings, and never blames a whole node.
func TestEveryBrokenLinkIsFoundInOneSweep(t *testing.T) {
	for n := 3; n <= 30; n++ {
		nodes := names(n)
		for i, a := range nodes {
			for _, b := range nodes[i+1:] {
				broken := func(x, y string) bool { return (x == a && y == b) || (x == b && y == a) }
				got := Classify(nodes, allEdges(nodes, broken), nil)
				if len(got.BrokenNodes) != 0 || got.Ambiguous || !slices.Equal(got.BrokenLinks, [][2]string{{a, b}}) {
					t.Fatalf("n=%d %s-%s: %+v", n, a, b, got)
				}
			}
		}
	}
}

func TestNeverGuessesWhenEverythingFails(t *testing.T) {
	nodes := names(3)
	got := Classify(nodes, allEdges(nodes, func(_, _ string) bool { return true }), nil)
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

func agentPod(node, ip, backup string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agent-" + node, Namespace: "kube-system",
			Labels: map[string]string{"app": agentLabel},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
	}
	if backup != "" {
		pod.Annotations = map[string]string{
			multusNetworkStatusAnnotation: `[{"interface":"eth0","ips":["` + ip + `"]},{"interface":"net1","ips":["` + backup + `"]}]`,
		}
	}
	return pod
}

func nodeWith(name, report string) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if report != "" {
		node.Annotations = map[string]string{ReportAnnotation: report}
	}
	return node
}

func publishedReport(t *testing.T, c client.Client, name string) Report {
	t.Helper()
	node := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, node); err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal([]byte(node.Annotations[ReportAnnotation]), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func allOK(context.Context, string, time.Duration) bool { return true }

func TestRoundProbesEveryPeerOnBothPaths(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(
		agentPod("a", "10.0.0.1", ""), agentPod("b", "10.0.0.2", "10.1.0.2"), agentPod("c", "10.0.0.3", "10.1.0.3"),
		nodeWith("a", ""), nodeWith("b", ""), nodeWith("c", ""),
	).Build()
	var calls atomic.Int32
	d := &Daemon{Client: c, NodeName: "a", Probe: func(context.Context, string, time.Duration) bool {
		calls.Add(1)
		return true
	}}
	d.init()
	if err := d.round(context.Background(), time.Second, 3); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("%d probes, want 4", got)
	}
	report := publishedReport(t, c, "a")
	for _, peer := range []string{"b", "c"} {
		r, ok := report.Peers[peer]
		if !ok || !r.Primary || r.Backup == nil || !*r.Backup {
			t.Fatalf("peer %s: %+v (present %v)", peer, r, ok)
		}
	}
	if len(report.Peers) != 2 {
		t.Fatalf("peers: %+v", report.Peers)
	}
}

func seedScenario(t *testing.T, age time.Duration) PeerResult {
	t.Helper()
	backup := true
	raw, _ := json.Marshal(Report{
		Time:  time.Now().Add(-age).Unix(),
		Peers: map[string]PeerResult{"b": {Primary: false, Backup: &backup}},
	})
	c := fake.NewClientBuilder().WithObjects(
		agentPod("a", "10.0.0.1", ""), agentPod("b", "10.0.0.2", "10.1.0.2"),
		nodeWith("a", string(raw)), nodeWith("b", ""),
	).Build()
	d := &Daemon{Client: c, NodeName: "a", Probe: allOK}
	d.init()
	d.seed(context.Background())
	if err := d.round(context.Background(), time.Second, 3); err != nil {
		t.Fatal(err)
	}
	return publishedReport(t, c, "a").Peers["b"]
}

func TestSeedKeepsDownPeerDownAfterOneSuccess(t *testing.T) {
	r := seedScenario(t, 0)
	if r.Primary || r.Backup == nil || !*r.Backup {
		t.Fatalf("%+v", r)
	}
}

func TestSeedIgnoresStaleReport(t *testing.T) {
	r := seedScenario(t, 31*time.Second)
	if !r.Primary {
		t.Fatalf("%+v", r)
	}
}

func TestParseReport(t *testing.T) {
	now := time.Now()
	fresh, _ := json.Marshal(Report{Time: now.Unix(), Peers: map[string]PeerResult{"b": {Primary: true}}})
	stale, _ := json.Marshal(Report{Time: now.Add(-31 * time.Second).Unix()})
	for name, tc := range map[string]struct {
		node *corev1.Node
		want bool
	}{
		"missing":   {nodeWith("a", ""), false},
		"malformed": {nodeWith("a", "{"), false},
		"stale":     {nodeWith("a", string(stale)), false},
		"fresh":     {nodeWith("a", string(fresh)), true},
	} {
		report, ok := ParseReport(tc.node, now)
		if ok != tc.want {
			t.Fatalf("%s: ok=%v", name, ok)
		}
		if ok && !report.Peers["b"].Primary {
			t.Fatalf("%s: peers lost: %+v", name, report)
		}
	}
}
