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

package linkrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

const (
	// ReportAnnotation is the Node annotation the router publishes its choices to.
	ReportAnnotation = "cniresilience.cn-operator/link-routes"

	// HeartbeatSeconds is how often the router rewrites an unchanged report, so
	// that silence can be told apart from "nothing changed".
	HeartbeatSeconds = 10

	// debounce merges a burst of watch events into one pass.
	debounce = 300 * time.Millisecond

	blockStateConfirmed = "confirmed"
)

// BlockAffinityGVK is the Calico BlockAffinity kind, read as unstructured.
var BlockAffinityGVK = schema.GroupVersionKind{Group: "crd.projectcalico.org", Version: "v1", Kind: "BlockAffinity"}

// Report is the content of ReportAnnotation: the path chosen for each peer.
type Report struct {
	// Time is when the router wrote this report, in Unix seconds.
	Time int64 `json:"time"`

	// Links maps peer node name to the path chosen for it.
	Links map[string]Choice `json:"links"`
}

// parseReport decodes the annotation without any freshness check.
func parseReport(node *corev1.Node) (Report, bool) {
	raw, ok := node.Annotations[ReportAnnotation]
	if !ok {
		return Report{}, false
	}
	var report Report
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return Report{}, false
	}
	return report, true
}

// ParseLinkReport returns the router report on node. It returns false if the
// annotation is missing, malformed, or older than healthagent.SilentAfterSeconds.
func ParseLinkReport(node *corev1.Node, now time.Time) (Report, bool) {
	report, ok := parseReport(node)
	if !ok || now.Unix()-report.Time > healthagent.SilentAfterSeconds {
		return Report{}, false
	}
	return report, true
}

// FlannelAddr returns the network address of podCIDR, which is the address of
// flannel.1 on that node ("10.244.1.0/24" gives "10.244.1.0"), or "" if podCIDR
// is not a CIDR.
func FlannelAddr(podCIDR string) string {
	_, network, err := net.ParseCIDR(podCIDR)
	if err != nil {
		return ""
	}
	return network.IP.String()
}

// Router keeps one node's steering routes in line with the probe reports.
type Router struct {
	// Reader serves the node, BlockAffinity and CNIResilience lists (a cache in
	// production).
	Reader client.Reader
	// Writer patches this node's annotation.
	Writer   client.Client
	NodeName string
	Host     Host
	Config   Config

	current  map[string]Choice
	loaded   bool
	stale    bool              // own probe report is stale, routes are kept
	noRoute  map[string]string // peer to the logged "path/reason" of a path without a route
	lastSent string
	lastTime time.Time
}

// Run calls Sync until ctx ends: whenever trigger fires (nil is fine, bursts are
// merged) and at least every interval. Errors are logged and never fatal. On
// return the host is left as it is, so routes survive a restart.
func (r *Router) Run(ctx context.Context, interval time.Duration, trigger <-chan struct{}) {
	logger := logf.FromContext(ctx).WithName("link-router")
	for {
		if err := r.Sync(ctx, time.Now()); err != nil {
			logger.Error(err, "Sync failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			select {
			case <-ctx.Done():
				return
			case <-time.After(debounce):
			}
		case <-time.After(interval):
		}
	}
}

// Sync performs one level-triggered pass.
func (r *Router) Sync(ctx context.Context, now time.Time) error {
	logger := logf.FromContext(ctx).WithName("link-router")

	var nodes corev1.NodeList
	if err := r.Reader.List(ctx, &nodes); err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	blocks, err := r.confirmedBlocks(ctx)
	if err != nil {
		return err
	}
	var crs remediationv1alpha1.CNIResilienceList
	if err := r.Reader.List(ctx, &crs); err != nil {
		return fmt.Errorf("list cniresiliences: %w", err)
	}

	var self *corev1.Node
	for i := range nodes.Items {
		if nodes.Items[i].Name == r.NodeName {
			self = &nodes.Items[i]
		}
	}
	if self == nil {
		return fmt.Errorf("own node %q not found", r.NodeName)
	}
	if !r.loaded {
		r.current = map[string]Choice{}
		if report, ok := parseReport(self); ok && report.Links != nil {
			r.current = report.Links
		}
		r.loaded = true
	}

	peers := make(map[string]Peer, len(nodes.Items))
	reports := make(map[string]healthagent.Report, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if report, ok := healthagent.ParseReport(n, now); ok {
			reports[n.Name] = report
		}
		if n.Name == r.NodeName {
			continue
		}
		p := Peer{Name: n.Name, FlannelAddr: FlannelAddr(n.Spec.PodCIDR), CalicoBlocks: blocks[n.Name]}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				p.InternalIP = a.Address
				break
			}
		}
		peers[n.Name] = p
	}

	var choices map[string]Choice
	if len(crs.Items) == 0 {
		choices = make(map[string]Choice, len(peers))
		for name := range peers {
			choices[name] = Choice{Path: remediationv1alpha1.LinkPathPrimary}
		}
	} else {
		if _, ok := reports[r.NodeName]; !ok {
			if !r.stale {
				logger.Info("Own probe report is stale, keeping routes")
				r.stale = true
			}
			return nil
		}
		if r.stale {
			logger.Info("Own probe report is fresh again")
			r.stale = false
		}
		spec := crs.Items[0].Spec
		choices = Decide(Inputs{
			Self:         r.NodeName,
			Peers:        peers,
			Reports:      reports,
			LinkFailover: spec.LinkFailoverEnabled(),
			LinkRelay:    spec.LinkRelayEnabled(),
			Current:      r.current,
		})
	}

	if err := Apply(r.Host, Build(choices, peers, r.Config)); err != nil {
		return fmt.Errorf("apply routes: %w", err)
	}
	r.logChanges(logger, choices, peers)
	r.current = choices
	return r.publish(ctx, choices, now)
}

// logChanges logs each path that differs from the previous pass. A peer with no
// previous choice logs only when its path is not primary. It also logs, once per
// change, a non-primary path that installs no route, and why.
func (r *Router) logChanges(logger logr.Logger, choices map[string]Choice, peers map[string]Peer) {
	names := make([]string, 0, len(choices))
	for name := range choices {
		names = append(names, name)
	}
	sort.Strings(names)
	if r.noRoute == nil {
		r.noRoute = map[string]string{}
	}
	for _, name := range names {
		now := choices[name]
		old, known := r.current[name]
		if known && old != now || !known && now.Path != remediationv1alpha1.LinkPathPrimary {
			logger.Info("Changed path", "peer", name, "from", old.Path, "to", now.Path, "via", now.Via)
		}

		reason := noRouteReason(now, peers[name], peers)
		if reason == "" {
			delete(r.noRoute, name)
			continue
		}
		key := now.Path + "/" + now.Via + "/" + reason
		if r.noRoute[name] != key {
			logger.Info("Path has no route", "peer", name, "path", now.Path, "reason", reason)
			r.noRoute[name] = key
		}
	}
	for name := range r.noRoute {
		if _, ok := choices[name]; !ok {
			delete(r.noRoute, name)
		}
	}
}

// noRouteReason says why a backup or relay choice installs no route, or "".
func noRouteReason(ch Choice, peer Peer, peers map[string]Peer) string {
	switch ch.Path {
	case remediationv1alpha1.LinkPathBackup:
		if peer.FlannelAddr == "" {
			return "peer has no flannel address"
		}
	case remediationv1alpha1.LinkPathRelay:
		if peers[ch.Via].InternalIP == "" {
			return "relay has no internal IP"
		}
	default:
		return ""
	}
	if len(peer.CalicoBlocks) == 0 {
		return "peer has no Calico blocks"
	}
	return ""
}

// confirmedBlocks returns the confirmed Calico block CIDRs per node. A cluster
// without the BlockAffinity CRD has none.
func (r *Router) confirmedBlocks(ctx context.Context) (map[string][]string, error) {
	list := &unstructured.UnstructuredList{}
	gvk := BlockAffinityGVK
	gvk.Kind += "List"
	list.SetGroupVersionKind(gvk)
	if err := r.Reader.List(ctx, list); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list blockaffinities: %w", err)
	}
	out := map[string][]string{}
	for i := range list.Items {
		obj := list.Items[i].Object
		node, _, _ := unstructured.NestedString(obj, "spec", "node")
		cidr, _, _ := unstructured.NestedString(obj, "spec", "cidr")
		state, _, _ := unstructured.NestedString(obj, "spec", "state")
		if state == blockStateConfirmed && node != "" && cidr != "" {
			out[node] = append(out[node], cidr)
		}
	}
	return out, nil
}

// publish writes the report only when the choices changed or the heartbeat is due.
func (r *Router) publish(ctx context.Context, choices map[string]Choice, now time.Time) error {
	content, err := json.Marshal(choices)
	if err != nil {
		return err
	}
	if string(content) == r.lastSent && now.Sub(r.lastTime) < HeartbeatSeconds*time.Second {
		return nil
	}
	value, err := json.Marshal(Report{Time: now.Unix(), Links: choices})
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{ReportAnnotation: string(value)}},
	})
	if err != nil {
		return err
	}
	node := &corev1.Node{}
	node.Name = r.NodeName
	if err := r.Writer.Patch(ctx, node, client.RawPatch("application/merge-patch+json", patch)); err != nil {
		return fmt.Errorf("publish link report: %w", err)
	}
	r.lastSent, r.lastTime = string(content), now
	return nil
}
