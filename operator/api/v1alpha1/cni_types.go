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

package v1alpha1

// CNISpec configures the CNI plugin health monitoring module.
type CNISpec struct {
	// Enabled toggles the CNI monitoring module.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// CalicoNamespace is the namespace where calico-node DaemonSet lives.
	// +kubebuilder:default="kube-system"
	// +optional
	CalicoNamespace string `json:"calicoNamespace,omitempty"`

	// CalicoDaemonSetName is the name of the Calico CNI DaemonSet.
	// +kubebuilder:default="calico-node"
	// +optional
	CalicoDaemonSetName string `json:"calicoDaemonSetName,omitempty"`

	// StuckPodThresholdSeconds is how long (in seconds) a pod may sit in
	// ContainerCreating with no pod IP before it is considered CNI-stuck.
	// +kubebuilder:default=60
	// +optional
	StuckPodThresholdSeconds int `json:"stuckPodThresholdSeconds,omitempty"`

	// IPAMUsageThresholdPercent triggers an alert when Calico's IP pool
	// utilisation exceeds this percentage (0–100).
	// +kubebuilder:default=80
	// +optional
	IPAMUsageThresholdPercent int `json:"ipamUsageThresholdPercent,omitempty"`

	// EvictionCooldownSeconds is the cooldown period (in seconds) during which the
	// operator suppresses repeated evictions for the same workload to prevent churn.
	// +kubebuilder:default=180
	// +optional
	EvictionCooldownSeconds int `json:"evictionCooldownSeconds,omitempty"`
}
