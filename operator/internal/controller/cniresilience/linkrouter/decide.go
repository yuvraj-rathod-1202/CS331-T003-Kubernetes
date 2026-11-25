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

// Package linkrouter decides, for one node, which path each peer is reached
// over (primary, backup, relay or none) from the probe reports of the cluster.
package linkrouter

import (
	"sort"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/healthagent"
)

// Choice is the path picked for one peer.
type Choice struct {
	// Path is one of the v1alpha1.LinkPath* values.
	Path string `json:"path"`
	// Via is the relay node, set only when Path is relay.
	Via string `json:"via,omitempty"`
}

// Peer describes another node as far as routing is concerned.
type Peer struct {
	Name         string
	InternalIP   string   // Calico IPIP next hop when used as a relay
	FlannelAddr  string   // network address of spec.podCIDR, "" if unknown
	CalicoBlocks []string // confirmed BlockAffinity CIDRs
}

// Inputs is everything Decide needs.
type Inputs struct {
	Self         string
	Peers        map[string]Peer               // every other node, keyed by name
	Reports      map[string]healthagent.Report // fresh reports only (self included), keyed by node
	LinkFailover bool
	LinkRelay    bool
	Current      map[string]Choice // last published choices (for relay stickiness)
}

// Decide picks a path for every peer in in.Peers. It assumes
// in.Reports[in.Self] exists; the caller handles a stale or missing own report.
func Decide(in Inputs) map[string]Choice {
	out := make(map[string]Choice, len(in.Peers))
	own := in.Reports[in.Self]
	for name := range in.Peers {
		out[name] = decidePeer(in, own, name)
	}
	return out
}

func decidePeer(in Inputs, own healthagent.Report, b string) Choice {
	primary := Choice{Path: remediationv1alpha1.LinkPathPrimary}
	if !in.LinkFailover {
		return primary
	}
	res, probed := own.Peers[b]
	if !probed || res.Primary {
		return primary
	}
	if res.Backup != nil && *res.Backup {
		return Choice{Path: remediationv1alpha1.LinkPathBackup}
	}
	if in.LinkRelay {
		if via := pickRelay(in, own, b); via != "" {
			return Choice{Path: remediationv1alpha1.LinkPathRelay, Via: via}
		}
	}
	return Choice{Path: remediationv1alpha1.LinkPathNone}
}

// pickRelay returns the relay for b, or "" if there is none. The currently
// used relay is kept while it remains a candidate, else the lowest name wins.
func pickRelay(in Inputs, own healthagent.Report, b string) string {
	var cands []string
	for c := range in.Peers {
		if c != in.Self && c != b && isCandidate(in, own, c, b) {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Strings(cands)
	if cur, ok := in.Current[b]; ok && cur.Path == remediationv1alpha1.LinkPathRelay {
		for _, c := range cands {
			if c == cur.Via {
				return c
			}
		}
	}
	return cands[0]
}

// isCandidate reports whether c can relay to b: c has a fresh report, self
// reaches c over the primary (Calico) path, and c reaches b directly.
func isCandidate(in Inputs, own healthagent.Report, c, b string) bool {
	rc, ok := in.Reports[c]
	if !ok || !own.Peers[c].Primary {
		return false
	}
	cb := rc.Peers[b]
	return cb.Primary || (cb.Backup != nil && *cb.Backup)
}
