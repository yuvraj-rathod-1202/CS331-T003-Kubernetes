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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// NetworkRemediationSpec defines the desired state of NetworkRemediation.
// It contains configuration for all four remediation modules.
// Each module can be independently enabled/disabled and configured in its respective types file:
// - CNI: cni_types.go
// - CoreDNS: coredns_types.go
// - NetworkPolicy: networkpolicy_types.go
// - PodConnectivity: podconnectivity_types.go
type NetworkRemediationSpec struct {
	// CNI configures CNI plugin health monitoring and remediation.
	// +optional
	CNI CNISpec `json:"cni,omitempty"`

	// CoreDNS configures DNS health monitoring and remediation.
	// +optional
	CoreDNS CoreDNSSpec `json:"coreDNS,omitempty"`

	// NetworkPolicy configures NetworkPolicy drift detection and enforcement verification.
	// +optional
	NetworkPolicy NetworkPolicySpec `json:"networkPolicy,omitempty"`

	// PodConnectivity configures pod-to-pod and internode connectivity probing.
	// +optional
	PodConnectivity PodConnectivitySpec `json:"podConnectivity,omitempty"`
}

// NetworkRemediationStatus defines the observed state of NetworkRemediation.
type NetworkRemediationStatus struct {
	// Phase is the overall phase of the operator (e.g., "Running", "Degraded", "Error").
	// +optional
	Phase string `json:"phase,omitempty"`

	// CNI is the observed state of the CNI module.
	// +optional
	CNI ModuleStatus `json:"cni,omitempty"`

	// CoreDNS is the observed state of the CoreDNS module.
	// +optional
	CoreDNS ModuleStatus `json:"coreDNS,omitempty"`

	// NetworkPolicy is the observed state of the NetworkPolicy module.
	// +optional
	NetworkPolicy ModuleStatus `json:"networkPolicy,omitempty"`

	// PodConnectivity is the observed state of the PodConnectivity module.
	// +optional
	PodConnectivity ModuleStatus `json:"podConnectivity,omitempty"`

	// LastReconcileTime is the timestamp of the last successful reconcile.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// ModuleStatus is a simple, shared status struct for each module.
// Team members should extend this or add module-specific status fields as needed.
type ModuleStatus struct {
	// Healthy indicates whether the module's checks are passing.
	Healthy bool `json:"healthy"`

	// Enabled indicates whether this module is currently active.
	Enabled bool `json:"enabled"`

	// Message is a human-readable message about the module's current state.
	// +optional
	Message string `json:"message,omitempty"`

	// LastChecked is the timestamp of the last health check.
	// +optional
	LastChecked *metav1.Time `json:"lastChecked,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// NetworkRemediation is the Schema for the networkremediations API.
// It defines the configuration for the network remediation operator,
// which monitors and auto-heals networking failures in a Kubernetes cluster.
type NetworkRemediation struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NetworkRemediation
	// +required
	Spec NetworkRemediationSpec `json:"spec"`

	// status defines the observed state of NetworkRemediation
	// +optional
	Status NetworkRemediationStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NetworkRemediationList contains a list of NetworkRemediation
type NetworkRemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NetworkRemediation `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &NetworkRemediation{}, &NetworkRemediationList{})
		return nil
	})
}
