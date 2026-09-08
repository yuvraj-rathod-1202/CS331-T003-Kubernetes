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

// NetworkPolicySpec configures the NetworkPolicy enforcement health verification module.
type NetworkPolicySpec struct {
	// Enabled toggles the NetworkPolicy enforcement monitoring module.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// AutoHeal automatically restarts degraded or crashlooping calico-node enforcement
	// agent pods so that felix re-syncs and repopulates kernel packet-filtering rules.
	// When false, enforcement degradation is only reported in status/events.
	// +kubebuilder:default=true
	// +optional
	AutoHeal bool `json:"autoHeal,omitempty"`

	// CooldownSeconds specifies the minimum cooldown period in seconds between
	// enforcement agent pod restarts to avoid restart-thrashing.
	// +kubebuilder:default=60
	// +optional
	CooldownSeconds int32 `json:"cooldownSeconds,omitempty"`
}
