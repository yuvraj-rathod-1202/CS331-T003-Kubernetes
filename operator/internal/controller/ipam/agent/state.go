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

// Package agent implements the per-node IPAM agent.
//
// The agent runs on every node as a privileged hostNetwork DaemonSet. It
// writes only NodeBlock status (the controller writes spec). On each pass it:
//   - installs a route "block via owner node IP" for every block owned by
//     another node, aggregating buddies into one prefix, and acknowledges them;
//   - writes this node's CNI config from the blocks it owns, promoting a new
//     block from Pending to Active only after the routes are in place;
//   - reports usage, counted from host-local's lease files (ground truth,
//     including leaked leases).
//
// Every pass is level-triggered and idempotent, so a crash, restart or reboot
// is repaired by simply running again.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam/buddy"
)

// RouteProtocol marks routes installed by the agent, so it can find and remove
// stale ones without touching routes it does not own.
const RouteProtocol = 99

// Route is "Dst via Gateway".
type Route struct {
	Dst     string
	Gateway string
}

// routable reports whether other nodes need a route for a block in this phase.
// Draining blocks stay routable until the drain finishes; Quarantined and
// Released blocks have had their routes withdrawn.
func routable(phase string) bool {
	switch phase {
	case "", remediationv1alpha1.NodeBlockPending, remediationv1alpha1.NodeBlockActive, remediationv1alpha1.NodeBlockDraining:
		return true
	}
	return false
}

func phaseOf(b *remediationv1alpha1.NodeBlock) string {
	if b.Status.Phase == "" {
		return remediationv1alpha1.NodeBlockPending
	}
	return b.Status.Phase
}

func internalIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

// DesiredRoutes returns the routes this node needs: one per owner node, with
// that node's blocks aggregated. Blocks this node owns need no route, because
// the ptp plugin adds a host route per pod. Owners with no InternalIP yet are
// reported as problems and skipped.
func DesiredRoutes(self string, nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock) ([]Route, []string) {
	ips := map[string]string{}
	for i := range nodes {
		ips[nodes[i].Name] = internalIP(&nodes[i])
	}
	byOwner := map[string][]string{}
	for i := range blocks {
		b := &blocks[i]
		if b.Spec.NodeName == self || !routable(b.Status.Phase) {
			continue
		}
		byOwner[b.Spec.NodeName] = append(byOwner[b.Spec.NodeName], b.Spec.CIDR)
	}

	var routes []Route
	var problems []string
	for owner, cidrs := range byOwner {
		gw := ips[owner]
		if gw == "" {
			problems = append(problems, fmt.Sprintf("no InternalIP for node %s, cannot route %v", owner, cidrs))
			continue
		}
		agg, err := buddy.Aggregate(cidrs)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		for _, c := range agg {
			routes = append(routes, Route{Dst: c, Gateway: gw})
		}
	}
	slices.SortFunc(routes, func(a, b Route) int { return strings.Compare(a.Dst, b.Dst) })
	slices.Sort(problems)
	return routes, problems
}

// AckableBlocks are the blocks owned by other nodes whose routes this node
// installs, so it can acknowledge them once DesiredRoutes has been applied.
func AckableBlocks(self string, blocks []remediationv1alpha1.NodeBlock) []string {
	var out []string
	for i := range blocks {
		b := &blocks[i]
		if b.Spec.NodeName != self && routable(b.Status.Phase) {
			out = append(out, b.Name)
		}
	}
	slices.Sort(out)
	return out
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// ReadyToPromote reports whether a Pending block may become Active: every other
// node that is Ready has acknowledged its route. A node that is down must not
// block a grow, so after timeout the block is promoted anyway; that node
// resyncs its routes on return, before it serves traffic.
func ReadyToPromote(self string, b *remediationv1alpha1.NodeBlock, nodes []corev1.Node, now time.Time, timeout time.Duration) bool {
	acked := map[string]bool{}
	for _, n := range b.Status.RoutesAckedBy {
		acked[n] = true
	}
	all := true
	for i := range nodes {
		n := &nodes[i]
		if n.Name == self || !n.DeletionTimestamp.IsZero() || !nodeReady(n) {
			continue
		}
		if !acked[n.Name] {
			all = false
		}
	}
	if all {
		return true
	}
	t := b.Status.LastTransitionTime
	if t == nil {
		t = &b.CreationTimestamp
	}
	return now.Sub(t.Time) >= timeout
}

type cniRange struct {
	Subnet string `json:"subnet"`
}

type cniIPAM struct {
	Type    string        `json:"type"`
	DataDir string        `json:"dataDir"`
	Ranges  [][]cniRange  `json:"ranges"`
	Routes  []cniRouteDst `json:"routes"`
}

type cniRouteDst struct {
	Dst string `json:"dst"`
}

type cniPlugin struct {
	Type         string         `json:"type"`
	IPMasq       *bool          `json:"ipMasq,omitempty"`
	IPAM         *cniIPAM       `json:"ipam,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
}

type cniConflist struct {
	CNIVersion string      `json:"cniVersion"`
	Name       string      `json:"name"`
	Plugins    []cniPlugin `json:"plugins"`
}

// RenderCNIConfig renders the ptp + host-local conflist for a node's blocks.
// All blocks go in one inner "ranges" array, which host-local treats as
// alternatives (verified in Phase 0); separate outer entries would mean
// dual-stack and take one address from each. ipMasq stays off so pod-to-pod
// traffic across nodes is never NATed.
func RenderCNIConfig(netName, dataDir string, cidrs []string) ([]byte, error) {
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("no blocks to render")
	}
	type parsed struct {
		cidr string
		ip   net.IP
	}
	blocks := make([]parsed, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("invalid block %q: %w", c, err)
		}
		blocks = append(blocks, parsed{cidr: c, ip: n.IP})
	}
	slices.SortFunc(blocks, func(a, b parsed) int { return bytes.Compare(a.ip, b.ip) })
	sorted := make([]string, len(blocks))
	for i, b := range blocks {
		sorted[i] = b.cidr
	}
	// Buddy blocks become one range: the 3 reserved addresses are then paid once
	// for the pair, not twice. Leases are plain per-address files, so addresses
	// already handed out stay valid when blocks merge or split again.
	sorted, err := buddy.Aggregate(sorted)
	if err != nil {
		return nil, err
	}
	ranges := make([]cniRange, len(sorted))
	for i, c := range sorted {
		ranges[i] = cniRange{Subnet: c}
	}
	off := false
	conf := cniConflist{
		CNIVersion: "0.4.0",
		Name:       netName,
		Plugins: []cniPlugin{
			{
				Type:   "ptp",
				IPMasq: &off,
				IPAM: &cniIPAM{
					Type:    "host-local",
					DataDir: dataDir,
					Ranges:  [][]cniRange{ranges},
					Routes:  []cniRouteDst{{Dst: "0.0.0.0/0"}},
				},
			},
			{Type: "portmap", Capabilities: map[string]any{"portMappings": true}},
		},
	}
	return json.MarshalIndent(conf, "", "  ")
}
