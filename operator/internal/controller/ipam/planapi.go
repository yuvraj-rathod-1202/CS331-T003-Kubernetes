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

package ipam

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

// Plan is the outcome of one planning pass: the blocks to create, drain,
// restore or delete.
type Plan = poolPlan

// PlanPool runs the controller's planning step on the given state, with the
// clock and the starved nodes supplied by the caller. The simulator uses it so
// that it exercises the same code the controller runs, not a copy.
func PlanPool(spec *remediationv1alpha1.IPPoolSpec, nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock,
	now time.Time, starved map[string]time.Time) *Plan {
	return planPool(spec, nodes, blocks, planInput{Now: now, Starved: starved})
}

// WithDefaults fills unset IPPool spec fields with their defaults.
func WithDefaults(in remediationv1alpha1.IPPoolSpec) remediationv1alpha1.IPPoolSpec {
	return withDefaults(in)
}

// BlockName is the NodeBlock name for a CIDR.
func BlockName(cidr string) string { return blockName(cidr) }

// UsableAddresses is how many addresses of a block of the given size pods can use.
func UsableAddresses(size int) int { return usableAddresses(size) }
