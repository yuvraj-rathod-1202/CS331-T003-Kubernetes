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

package cniresilience

import (
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/linkrouter"
)

// linkStatuses merges the routers' reports into the list of non-primary links, sorted
// by From then To. Since is carried over from previous when the path is unchanged.
// entered and left are the pairs that became or stopped being "none" against previous.
// A node whose report is missing, stale or malformed is unknown, not changed: its
// previous links are kept as they were and cause no transitions.
func linkStatuses(
	nodes []corev1.Node,
	now time.Time,
	previous []remediationv1alpha1.LinkStatus,
) (links []remediationv1alpha1.LinkStatus, entered, left [][2]string) {
	prev := make(map[[2]string]remediationv1alpha1.LinkStatus, len(previous))
	for _, l := range previous {
		prev[[2]string{l.From, l.To}] = l
	}
	stamp := metav1.NewTime(now)
	fresh := make(map[string]bool, len(nodes))
	nowNone := map[[2]string]bool{}

	for i := range nodes {
		node := &nodes[i]
		rep, ok := linkrouter.ParseLinkReport(node, now)
		if !ok {
			continue
		}
		fresh[node.Name] = true
		for peer, choice := range rep.Links {
			if choice.Path == remediationv1alpha1.LinkPathPrimary {
				continue
			}
			key := [2]string{node.Name, peer}
			link := remediationv1alpha1.LinkStatus{From: node.Name, To: peer, Path: choice.Path, Via: choice.Via}
			if old, had := prev[key]; had && old.Path == link.Path && old.Via == link.Via && old.Since != nil {
				link.Since = old.Since
			} else {
				since := stamp
				link.Since = &since
			}
			links = append(links, link)
			if choice.Path == remediationv1alpha1.LinkPathNone {
				nowNone[key] = true
				if prev[key].Path != remediationv1alpha1.LinkPathNone {
					entered = append(entered, key)
				}
			}
		}
	}

	known := make(map[string]bool, len(nodes))
	for i := range nodes {
		known[nodes[i].Name] = true
	}
	for key, old := range prev {
		switch {
		case !known[key[0]]:
			// The node is gone, and so are its links.
		case !fresh[key[0]]:
			links = append(links, old)
		case old.Path == remediationv1alpha1.LinkPathNone && !nowNone[key]:
			left = append(left, key)
		}
	}

	sort.Slice(links, func(i, j int) bool {
		if links[i].From != links[j].From {
			return links[i].From < links[j].From
		}
		return links[i].To < links[j].To
	})
	sortPairs(entered)
	sortPairs(left)
	return links, entered, left
}

func sortPairs(p [][2]string) {
	sort.Slice(p, func(i, j int) bool {
		if p[i][0] != p[j][0] {
			return p[i][0] < p[j][0]
		}
		return p[i][1] < p[j][1]
	})
}

// noPathPairs returns the "from→to" text of every link with no path, in link order.
func noPathPairs(links []remediationv1alpha1.LinkStatus) []string {
	var pairs []string
	for _, l := range links {
		if l.Path == remediationv1alpha1.LinkPathNone {
			pairs = append(pairs, l.From+"→"+l.To)
		}
	}
	return pairs
}

// linksCondition builds the LinksReachable condition for links.
func linksCondition(links []remediationv1alpha1.LinkStatus, generation int64) metav1.Condition {
	cond := metav1.Condition{
		Type:               remediationv1alpha1.ConditionLinksReachable,
		Status:             metav1.ConditionTrue,
		Reason:             "AllLinksReachable",
		Message:            "All links have a path",
		ObservedGeneration: generation,
	}
	if pairs := noPathPairs(links); len(pairs) > 0 {
		cond.Status = metav1.ConditionFalse
		cond.Reason = remediationv1alpha1.ReasonNoPath
		cond.Message = "No path: " + strings.Join(pairs, ", ")
	}
	return cond
}
