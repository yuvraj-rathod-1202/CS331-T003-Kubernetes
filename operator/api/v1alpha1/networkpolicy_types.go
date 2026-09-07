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

// NetworkPolicySpec configures the NetworkPolicy drift-detection and
// enforcement-verification module.
//
// The module protects the NetworkPolicies that carry ProtectedLabel=true by
// snapshotting their spec into a baseline ConfigMap. It then heals three
// failures Kubernetes ignores: a protected policy being deleted, a protected
// policy drifting from its baseline, and the Calico enforcement agent (felix)
// going unhealthy (which leaves policy enforcement stale).
type NetworkPolicySpec struct {
	// Enabled toggles the NetworkPolicy monitoring module.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// ProtectedLabel is the label key a NetworkPolicy must carry (with value
	// "true") to be watched and auto-healed by this module.
	// +kubebuilder:default="remediation.cn-operator.yuvraj-rathod-1202.github.io/protected"
	// +optional
	ProtectedLabel string `json:"protectedLabel,omitempty"`

	// SnapshotNamespace is the namespace that holds the baseline ConfigMap.
	// +kubebuilder:default="kube-system"
	// +optional
	SnapshotNamespace string `json:"snapshotNamespace,omitempty"`

	// SnapshotConfigMapName is the name of the ConfigMap used to store the
	// baseline (desired) spec of each protected NetworkPolicy.
	// +kubebuilder:default="networkpolicy-baseline"
	// +optional
	SnapshotConfigMapName string `json:"snapshotConfigMapName,omitempty"`

	// AutoRestore automatically recreates deleted policies and reverts drifted
	// policies back to their baseline. When false, drift is only reported.
	// +kubebuilder:default=true
	// +optional
	AutoRestore bool `json:"autoRestore,omitempty"`

	// VerifyEnforcement enables checking the Calico enforcement agent
	// (calico-node / felix) health, since a crashed agent leaves policy
	// enforcement stale even while Kubernetes reports the pod as fine.
	// +kubebuilder:default=true
	// +optional
	VerifyEnforcement bool `json:"verifyEnforcement,omitempty"`
}
