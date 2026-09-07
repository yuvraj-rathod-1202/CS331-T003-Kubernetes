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

// CoreDNSSpec configures the CoreDNS health monitoring module.
type CoreDNSSpec struct {
	// Enabled toggles the CoreDNS monitoring module.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// ExpectedReplicas defines the desired number of CoreDNS replicas (default: 2).
	// +kubebuilder:default=2
	// +optional
	ExpectedReplicas int32 `json:"expectedReplicas,omitempty"`

	// LatencyThresholdMs defines the maximum acceptable DNS latency in milliseconds (default: 150).
	// +kubebuilder:default=150
	// +optional
	LatencyThresholdMs int64 `json:"latencyThresholdMs,omitempty"`

	// UpstreamDNSValidation toggles validation of the CoreDNS ConfigMap upstream forward directive.
	// +kubebuilder:default=true
	// +optional
	UpstreamDNSValidation bool `json:"upstreamDnsValidation,omitempty"`

	// AutoHealReplicas automatically restores deployment replicas if scaled down or missing.
	// +kubebuilder:default=true
	// +optional
	AutoHealReplicas bool `json:"autoHealReplicas,omitempty"`

	// AutoHealConfigMap automatically repairs corrupted upstream resolvers in the CoreDNS ConfigMap.
	// +kubebuilder:default=true
	// +optional
	AutoHealConfigMap bool `json:"autoHealConfigMap,omitempty"`

	// FallbackUpstreamServers are the fallback upstream DNS resolvers to inject if upstream is corrupted.
	// +optional
	FallbackUpstreamServers []string `json:"fallbackUpstreamServers,omitempty"`
}
