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

// =============================================================================
// This file defines the CRD types for Problem 2: CNI Resilience.
//
// CNIResilience is a cluster-scoped resource. One per cluster. It configures
// the dual-CNI health monitoring and holds the observed health of every node's
// primary and backup network paths.
//
// TODO: Adjust fields as needed. Run `make manifests` after changes.
// =============================================================================

// CNIResilienceSpec defines the desired state of CNIResilience.
type CNIResilienceSpec struct {
	// PrimaryCNI describes the primary CNI (e.g. Calico).
	PrimaryCNI CNIConfig `json:"primaryCNI"`

	// BackupCNI describes the backup CNI (e.g. Flannel).
	BackupCNI CNIConfig `json:"backupCNI"`

	// ProbeIntervalSeconds is how often the health agent probes each path.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	ProbeIntervalSeconds int `json:"probeIntervalSeconds,omitempty"`

	// ProbeTimeoutSeconds is the timeout for a single probe.
	// +kubebuilder:default=2
	ProbeTimeoutSeconds int `json:"probeTimeoutSeconds,omitempty"`

	// FailureThreshold is how many consecutive probe failures mark a path as down.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	FailureThreshold int `json:"failureThreshold,omitempty"`

	// AutoRecover controls whether the controller automatically restarts
	// the failed CNI agent and restores routes.
	// +kubebuilder:default=true
	AutoRecover bool `json:"autoRecover,omitempty"`

	// AutoFailover controls whether traffic is automatically switched
	// to the backup path on primary failure.
	// +kubebuilder:default=true
	AutoFailover bool `json:"autoFailover,omitempty"`
}

// CNIConfig describes one CNI in the dual-CNI setup.
type CNIConfig struct {
	// Name is a human-readable name (e.g. "calico", "flannel").
	Name string `json:"name"`

	// Namespace is the namespace where the CNI agent DaemonSet runs.
	Namespace string `json:"namespace"`

	// DaemonSetName is the name of the CNI agent DaemonSet.
	DaemonSetName string `json:"daemonSetName"`

	// HealthEndpoint is the URL the health agent uses to check this CNI's agent.
	// e.g. "http://localhost:9099/liveness" for Calico.
	// +optional
	HealthEndpoint string `json:"healthEndpoint,omitempty"`

	// AddressSpace is the CIDR used by this CNI (e.g. "10.244.0.0/16").
	AddressSpace string `json:"addressSpace"`
}

// CNIResilienceStatus defines the observed state of CNIResilience.
type CNIResilienceStatus struct {
	// Phase is the overall cluster-level state: "Healthy", "Degraded", "FailedOver".
	Phase string `json:"phase,omitempty"`

	// NodeStatuses contains per-node health information.
	NodeStatuses []NodeCNIStatus `json:"nodeStatuses,omitempty"`

	// FailoverActive is true if any node is currently using the backup path.
	FailoverActive bool `json:"failoverActive,omitempty"`

	// LastReconcileTime is when the controller last ran.
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// RecoveryCount is total number of automatic recoveries performed.
	RecoveryCount int `json:"recoveryCount,omitempty"`

	// FailoverCount is total number of failovers performed.
	FailoverCount int `json:"failoverCount,omitempty"`
}

// NodeCNIStatus holds the health of both CNI paths on a single node.
type NodeCNIStatus struct {
	// NodeName is the Kubernetes node name.
	NodeName string `json:"nodeName"`

	// PrimaryHealthy is true if the primary CNI path is working.
	PrimaryHealthy bool `json:"primaryHealthy"`

	// BackupHealthy is true if the backup CNI path is working.
	BackupHealthy bool `json:"backupHealthy"`

	// ActivePath is which path is currently carrying traffic: "primary" or "backup".
	ActivePath string `json:"activePath"`

	// PrimaryConsecutiveFailures is the current failure streak for the primary path.
	PrimaryConsecutiveFailures int `json:"primaryConsecutiveFailures,omitempty"`

	// BackupConsecutiveFailures is the current failure streak for the backup path.
	BackupConsecutiveFailures int `json:"backupConsecutiveFailures,omitempty"`

	// LastProbeTime is when this node was last probed.
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// LastFailoverTime is when the last failover happened on this node (if any).
	LastFailoverTime *metav1.Time `json:"lastFailoverTime,omitempty"`

	// LastRecoveryTime is when the last recovery happened on this node (if any).
	LastRecoveryTime *metav1.Time `json:"lastRecoveryTime,omitempty"`

	// Message is a human-readable status message.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Failovers",type="integer",JSONPath=".status.failoverCount"
// +kubebuilder:printcolumn:name="Recoveries",type="integer",JSONPath=".status.recoveryCount"
// +kubebuilder:printcolumn:name="FailoverActive",type="boolean",JSONPath=".status.failoverActive"

// CNIResilience is the Schema for the cniresiliences API.
// One per cluster — configures dual-CNI monitoring, failover and recovery.
type CNIResilience struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CNIResilienceSpec   `json:"spec,omitempty"`
	Status CNIResilienceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CNIResilienceList contains a list of CNIResilience.
type CNIResilienceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CNIResilience `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &CNIResilience{}, &CNIResilienceList{})
		return nil
	})
}
