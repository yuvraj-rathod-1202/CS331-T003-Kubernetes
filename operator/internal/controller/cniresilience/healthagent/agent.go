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

// Package healthagent holds both halves of dual-CNI health checking.
//
// A DaemonSet agent runs on every node as a normal pod, so its probes cross the
// real pod data path (daemon.go). It writes what it saw to an annotation on its own
// Node. The operator only reads those annotations (this file), so it needs no route
// to the nodes and no raw sockets, and can run anywhere.
//
// The probe pattern is the rotating directed ring from deep-dive Part L: each round,
// node i probes node (i+r) mod N, so one round costs exactly N probes.
package healthagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ReportAnnotation is the Node annotation an agent writes its results to.
	ReportAnnotation = "cniresilience.cn-operator/path-report"

	// HeartbeatSeconds is how often an agent rewrites an unchanged report, so
	// that silence can be told apart from "nothing changed".
	HeartbeatSeconds = 10

	// SilentAfterSeconds is the report age after which a node counts as silent.
	SilentAfterSeconds = 3 * HeartbeatSeconds
)

// PeerResult is an agent's current verdict on one peer.
type PeerResult struct {
	// Primary is true if the eth0 path to the peer works.
	Primary bool `json:"primary"`

	// Backup is the net1 path result, or nil if there is no backup path to test.
	Backup *bool `json:"backup,omitempty"`
}

// Report is the content of ReportAnnotation.
type Report struct {
	// Time is when the agent wrote this report, in Unix seconds.
	Time int64 `json:"time"`

	// Peers maps peer node name to the latest result for it.
	Peers map[string]PeerResult `json:"peers"`
}

// Edge is one directed probe result.
type Edge struct {
	From string
	To   string
	OK   bool
}

// Verdict is what a set of probe results proves.
type Verdict struct {
	// BrokenNodes are nodes proven broken.
	BrokenNodes []string

	// BrokenLinks are pairs of healthy nodes that cannot reach each other.
	BrokenLinks [][2]string

	// Ambiguous is true if some failure has more than one explanation. Part L4
	// rule: never guess, take no action and wait for the next round.
	Ambiguous bool
}

// Classify applies the Part L3 patterns. A node is broken only if a probe into it
// failed, it failed outward too (or went silent), none of its probes worked, and
// some probe between other nodes did work. A remaining failure between two nodes
// that are each seen working elsewhere is a broken link. Anything else is ambiguous.
func Classify(nodes []string, edges []Edge, silent map[string]bool) Verdict {
	touching := func(node string) (in, out []Edge) {
		for _, e := range edges {
			if e.To == node {
				in = append(in, e)
			}
			if e.From == node {
				out = append(out, e)
			}
		}
		return in, out
	}
	allFailed := func(list []Edge) bool {
		return !slices.ContainsFunc(list, func(e Edge) bool { return e.OK })
	}
	otherWorks := func(node string) bool {
		return slices.ContainsFunc(edges, func(e Edge) bool {
			return e.OK && e.From != node && e.To != node
		})
	}

	var v Verdict
	for _, node := range nodes {
		in, out := touching(node)
		failedOut := len(out) > 0 && allFailed(out)
		if len(in) > 0 && allFailed(in) && (failedOut || silent[node]) && otherWorks(node) {
			v.BrokenNodes = append(v.BrokenNodes, node)
		}
	}

	seenWorking := func(node string) bool {
		return slices.ContainsFunc(edges, func(e Edge) bool {
			return e.OK && (e.From == node || e.To == node)
		})
	}
	for _, e := range edges {
		if e.OK || slices.Contains(v.BrokenNodes, e.From) || slices.Contains(v.BrokenNodes, e.To) {
			continue
		}
		if !seenWorking(e.From) || !seenWorking(e.To) {
			v.Ambiguous = true
			continue
		}
		link := [2]string{min(e.From, e.To), max(e.From, e.To)}
		if !slices.Contains(v.BrokenLinks, link) {
			v.BrokenLinks = append(v.BrokenLinks, link)
		}
	}
	return v
}

// NodeHealthStatus holds the health of both paths on a single node.
type NodeHealthStatus struct {
	// PrimaryHealthy is false only if the node is proven broken on the primary path.
	PrimaryHealthy bool

	// Ambiguous is true if the failures seen have more than one explanation.
	Ambiguous bool

	// Working is true only if every probe touching this node worked. It is stricter
	// than PrimaryHealthy: "not proven broken" must not undo a repair.
	Working bool

	// BackupHealthy is false only if the node is proven broken on the backup path.
	BackupHealthy bool

	// BackupMeasured is true if any agent has a backup result involving this node.
	BackupMeasured bool

	// Measured is true if this node took part in at least one primary probe.
	Measured bool

	// Note explains anything short of a clear verdict.
	Note string
}

// ReadNodeHealth turns the agents' reports into per-node health.
func ReadNodeHealth(ctx context.Context, c client.Client, now time.Time) (map[string]*NodeHealthStatus, error) {
	nodeList := &corev1.NodeList{}
	if err := c.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	names := make([]string, 0, len(nodeList.Items))
	silent := make(map[string]bool)
	var primary, backup []Edge
	for _, node := range nodeList.Items {
		names = append(names, node.Name)
		raw, ok := node.Annotations[ReportAnnotation]
		if !ok {
			silent[node.Name] = true
			continue
		}
		var report Report
		if err := json.Unmarshal([]byte(raw), &report); err != nil {
			return nil, fmt.Errorf("parse report on node %s: %w", node.Name, err)
		}
		if now.Unix()-report.Time > SilentAfterSeconds {
			silent[node.Name] = true
			continue
		}
		for peer, result := range report.Peers {
			primary = append(primary, Edge{From: node.Name, To: peer, OK: result.Primary})
			if result.Backup != nil {
				backup = append(backup, Edge{From: node.Name, To: peer, OK: *result.Backup})
			}
		}
	}

	primaryVerdict := Classify(names, primary, silent)
	backupVerdict := Classify(names, backup, silent)

	takesPart := func(edges []Edge, node string) bool {
		return slices.ContainsFunc(edges, func(e Edge) bool { return e.From == node || e.To == node })
	}
	result := make(map[string]*NodeHealthStatus, len(names))
	for _, name := range names {
		status := &NodeHealthStatus{
			PrimaryHealthy: !slices.Contains(primaryVerdict.BrokenNodes, name),
			BackupHealthy:  !slices.Contains(backupVerdict.BrokenNodes, name),
			BackupMeasured: takesPart(backup, name),
			Measured:       takesPart(primary, name),
			Ambiguous:      primaryVerdict.Ambiguous,
		}
		status.Working = status.Measured && !primaryVerdict.Ambiguous &&
			!slices.ContainsFunc(primary, func(e Edge) bool {
				return !e.OK && (e.From == name || e.To == name)
			})
		var notes []string
		for _, link := range primaryVerdict.BrokenLinks {
			if link[0] == name || link[1] == name {
				notes = append(notes, fmt.Sprintf("primary link broken: %s to %s", link[0], link[1]))
			}
		}
		if primaryVerdict.Ambiguous {
			notes = append(notes, "failure pattern is ambiguous; waiting for the next round")
		}
		if !status.Measured {
			notes = append(notes, "no agent report for this node")
		}
		status.Note = strings.Join(notes, "; ")
		result[name] = status
	}
	return result, nil
}
