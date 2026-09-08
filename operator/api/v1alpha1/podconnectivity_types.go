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

// PodConnectivitySpec configures the pod-to-pod and inter-node connectivity probing module.
type PodConnectivitySpec struct {
	// Enabled toggles the pod connectivity monitoring module.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// TargetNamespace is the namespace where probing pods or responders are located.
	// +kubebuilder:default="default"
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`

	// CheckPolicy configures probing mechanics, topology, and probe intervals.
	// +optional
	CheckPolicy CheckPolicySpec `json:"checkPolicy,omitempty"`

	// EvaluatePolicy configures triangulation thresholds and anchor validation.
	// +optional
	EvaluatePolicy EvaluatePolicySpec `json:"evaluatePolicy,omitempty"`

	// RemediationPolicy specifies automated healing steps and escalation limits.
	// +optional
	RemediationPolicy RemediationPolicySpec `json:"remediationPolicy,omitempty"`
}

// CheckPolicySpec defines how the probing layer executes.
type CheckPolicySpec struct {
	// Topology defines the probing graph: "ring", "mesh", or "sampled".
	// +kubebuilder:default="ring"
	// +kubebuilder:validation:Enum=ring;mesh;sampled
	// +optional
	Topology string `json:"topology,omitempty"`

	// IntervalSeconds is how frequently probes execute.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=300
	// +optional
	IntervalSeconds int `json:"intervalSeconds,omitempty"`

	// TimeoutSeconds specifies timeout for each synthetic probe.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=30
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// ProbeProtocol specifies network protocol: "tcp" or "http".
	// +kubebuilder:default="tcp"
	// +kubebuilder:validation:Enum=tcp;http
	// +optional
	ProbeProtocol string `json:"probeProtocol,omitempty"`

	// ProbePort is the port on which the probe agent and responders listen.
	// +kubebuilder:default=9099
	// +optional
	ProbePort int `json:"probePort,omitempty"`

	// Anchors defines static external anchors for corroboration.
	// +optional
	Anchors []AnchorTarget `json:"anchors,omitempty"`
}

// AnchorTarget defines a static anchor for out-of-band triangulation.
type AnchorTarget struct {
	Name string `json:"name"`

	Address string `json:"address"`

	// +optional
	Port int `json:"port,omitempty"`

	// +optional
	Protocol string `json:"protocol,omitempty"` // "tcp", "udp", "icmp"
}

// EvaluatePolicySpec configures failure classification and triangulation rules.
type EvaluatePolicySpec struct {
	// TriangulationEnabled enables cross-node triangulation.
	// +kubebuilder:default=true
	// +optional
	TriangulationEnabled bool `json:"triangulationEnabled,omitempty"`

	// AnchorCorroborationEnabled enables verification against static system anchors.
	// +kubebuilder:default=true
	// +optional
	AnchorCorroborationEnabled bool `json:"anchorCorroborationEnabled,omitempty"`

	// LocalCNIValidationEnabled enables host-to-pod loopback verification.
	// +kubebuilder:default=true
	// +optional
	LocalCNIValidationEnabled bool `json:"localCNIValidationEnabled,omitempty"`

	// ConsecutiveFailureThreshold specifies how many probe failures confirm an issue.
	// +kubebuilder:default=3
	// +optional
	ConsecutiveFailureThreshold int `json:"consecutiveFailureThreshold,omitempty"`

	// PacketLossThresholdPercent defines degradation threshold.
	// +kubebuilder:default=20
	// +optional
	PacketLossThresholdPercent int `json:"packetLossThresholdPercent,omitempty"`
}

// RemediationPolicySpec governs automated recovery actions.
type RemediationPolicySpec struct {
	// AutoRemediationEnabled permits the operator to execute corrective actions.
	// +kubebuilder:default=true
	// +optional
	AutoRemediationEnabled bool `json:"autoRemediationEnabled,omitempty"`

	// CNISubsystem configures restarts of the node's local CNI agent.
	// +optional
	CNISubsystem CNISubsystemRemediation `json:"cniSubsystem,omitempty"`

	// NodeIsolation configures taints and cordoning for degraded nodes.
	// +optional
	NodeIsolation NodeIsolationRemediation `json:"nodeIsolation,omitempty"`

	// NodeDrain configures workload eviction when lower tiers fail.
	// +optional
	NodeDrain NodeDrainRemediation `json:"nodeDrain,omitempty"`
}

// CNISubsystemRemediation configures CNI pod restarts.
type CNISubsystemRemediation struct {
	// Enabled allows restarting local CNI DaemonSet pod.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// DaemonSetName is the name of the CNI DaemonSet (e.g. "calico-node", "kindnet").
	// +kubebuilder:default="calico-node"
	// +optional
	DaemonSetName string `json:"daemonSetName,omitempty"`

	// Namespace is the namespace where the CNI DaemonSet lives.
	// +kubebuilder:default="kube-system"
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// MaxRestartAttempts is the maximum attempts before escalating to drain.
	// +kubebuilder:default=2
	// +optional
	MaxRestartAttempts int `json:"maxRestartAttempts,omitempty"`

	// RestartBackoffSeconds is cooldown between restarts.
	// +kubebuilder:default=60
	// +optional
	RestartBackoffSeconds int `json:"restartBackoffSeconds,omitempty"`
}

// NodeIsolationRemediation configures taints and cordoning.
type NodeIsolationRemediation struct {
	// TaintNode adds a taint to faulty nodes.
	// +kubebuilder:default=true
	// +optional
	TaintNode bool `json:"taintNode,omitempty"`

	// TaintKey is the taint key applied to degraded nodes.
	// +kubebuilder:default="network-degraded"
	// +optional
	TaintKey string `json:"taintKey,omitempty"`

	// TaintValue is the value of the taint.
	// +kubebuilder:default="true"
	// +optional
	TaintValue string `json:"taintValue,omitempty"`

	// TaintEffect is the effect applied (NoSchedule, PreferNoSchedule).
	// +kubebuilder:default="NoSchedule"
	// +optional
	TaintEffect string `json:"taintEffect,omitempty"`

	// CordonNode marks the node unschedulable.
	// +kubebuilder:default=true
	// +optional
	CordonNode bool `json:"cordonNode,omitempty"`
}

// NodeDrainRemediation configures safe eviction.
type NodeDrainRemediation struct {
	// Enabled permits node draining.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// EscalateAfterFailedRestarts triggers drain after CNI restarts fail.
	// +kubebuilder:default=2
	// +optional
	EscalateAfterFailedRestarts int `json:"escalateAfterFailedRestarts,omitempty"`

	// DrainTimeoutSeconds is eviction timeout.
	// +kubebuilder:default=120
	// +optional
	DrainTimeoutSeconds int `json:"drainTimeoutSeconds,omitempty"`

	// DeleteEmptyDirData allows eviction of pods with emptyDir volumes.
	// +kubebuilder:default=true
	// +optional
	DeleteEmptyDirData bool `json:"deleteEmptyDirData,omitempty"`

	// IgnoreDaemonSets skips DaemonSet pods during eviction.
	// +kubebuilder:default=true
	// +optional
	IgnoreDaemonSets bool `json:"ignoreDaemonSets,omitempty"`
}
