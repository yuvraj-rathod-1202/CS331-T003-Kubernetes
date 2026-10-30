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
// This file defines the CRD types for Problem 1: Dynamic Buddy-Style IPAM.
//
// Two CRDs:
//   - IPPool: cluster-scoped, represents one or more CIDR ranges available
//     for pod IP allocation. The controller manages a buddy tree per CIDR.
//   - NodeIPAllocation: cluster-scoped, tracks which blocks are assigned
//     to which node and their current usage.
//
// TODO: Adjust fields as needed during implementation. Run
//   `make manifests` after any change to regenerate CRD YAML.
// =============================================================================

// ============================================================
// IPPool — represents a pool of CIDR ranges for pod IPs
// ============================================================

// IPPoolSpec defines the desired state of an IPPool.
type IPPoolSpec struct {
	// CIDRs is the list of CIDR blocks available for allocation.
	// Supports discontiguous ranges (e.g. ["10.244.0.0/16", "172.20.0.0/18"]).
	// +kubebuilder:validation:MinItems=1
	CIDRs []string `json:"cidrs"`

	// MinBlockSize is the smallest block the buddy allocator will hand out,
	// expressed as a prefix length. Default: 28 (16 addresses).
	// +kubebuilder:default=28
	// +kubebuilder:validation:Minimum=24
	// +kubebuilder:validation:Maximum=30
	MinBlockSize int `json:"minBlockSize,omitempty"`

	// MaxBlockSize is the largest block a single node can receive,
	// expressed as a prefix length. Default: 22 (1024 addresses).
	// +kubebuilder:default=22
	// +kubebuilder:validation:Minimum=16
	// +kubebuilder:validation:Maximum=26
	MaxBlockSize int `json:"maxBlockSize,omitempty"`

	// ExpandThresholdPercent is the per-node usage percentage at which the
	// controller proactively allocates an additional block. Default: 80.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=50
	// +kubebuilder:validation:Maximum=95
	ExpandThresholdPercent int `json:"expandThresholdPercent,omitempty"`

	// TriggerFloor is the absolute number of free addresses below which a node
	// grows, even when its utilisation is still under ExpandThresholdPercent.
	// It should cover pod start rate x reaction time. Default: 8.
	// +kubebuilder:default=8
	// +kubebuilder:validation:Minimum=0
	TriggerFloor int `json:"triggerFloor,omitempty"`

	// GrowthPercent is how much a node grows by, as a percentage of its current
	// capacity, rounded up to a power of two (at least one minimum block).
	// Default: 50.
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	GrowthPercent int `json:"growthPercent,omitempty"`

	// CapacityCapPercent caps a node's total capacity at this percentage of its
	// allocatable pod count, so a leak cannot grow a node forever. Default: 120.
	// +kubebuilder:default=120
	// +kubebuilder:validation:Minimum=100
	CapacityCapPercent int `json:"capacityCapPercent,omitempty"`

	// InitialBlockPercent sizes a new node's first block as a percentage of its
	// allocatable pod count, rounded up to a power of two, with a floor of
	// 16 addresses. This block is also the node's capacity floor. Default: 25.
	// +kubebuilder:default=25
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	InitialBlockPercent int `json:"initialBlockPercent,omitempty"`

	// ReservedCIDRs are ranges the pool must not overlap, such as the Service
	// CIDR, the node network or Docker bridges. Pool CIDRs that overlap one of
	// these, or a node's InternalIP, are rejected.
	// +optional
	ReservedCIDRs []string `json:"reservedCIDRs,omitempty"`

	// ShrinkThresholdPercent: a node gives a block back only if, once the block
	// is drained, its remaining capacity would be at most this percent used.
	// Keep it well below ExpandThresholdPercent, or blocks flap. Default: 50.
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=90
	ShrinkThresholdPercent int `json:"shrinkThresholdPercent,omitempty"`

	// ShrinkCooldownSeconds is how long a block must have been Active before it
	// can be drained. Default: 300.
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=0
	ShrinkCooldownSeconds int `json:"shrinkCooldownSeconds,omitempty"`

	// QuarantineSeconds is how long a drained block is held back before it can
	// be handed to another node, so stale routes and late packets cannot reach a
	// new owner. Default: 60.
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=0
	QuarantineSeconds int `json:"quarantineSeconds,omitempty"`

	// LowWaterPercent: the pool reports Degraded when the free share of its
	// address space falls below this. Default: 10.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=90
	LowWaterPercent int `json:"lowWaterPercent,omitempty"`

	// DisableShrink turns off draining. Blocks are then only ever added.
	// +optional
	DisableShrink bool `json:"disableShrink,omitempty"`
}

// IPPoolStatus defines the observed state of an IPPool.
type IPPoolStatus struct {
	// TotalAddresses is the total number of addresses across all CIDRs.
	TotalAddresses int64 `json:"totalAddresses,omitempty"`

	// AllocatedAddresses is the number of addresses currently assigned to nodes.
	AllocatedAddresses int64 `json:"allocatedAddresses,omitempty"`

	// FreeAddresses is TotalAddresses - AllocatedAddresses.
	FreeAddresses int64 `json:"freeAddresses,omitempty"`

	// Phase is the current state of the pool: "Ready", "Exhausted", "Degraded",
	// or "Inactive" when another IPPool is serving the cluster's nodes.
	Phase string `json:"phase,omitempty"`

	// Message explains a Degraded, Exhausted or Inactive phase.
	Message string `json:"message,omitempty"`

	// CIDRs reports how much of each pool CIDR is handed out. CIDRs are filled
	// in order, so a later one stays Standby until the earlier ones are full.
	CIDRs []CIDRUsage `json:"cidrs,omitempty"`

	// NodeAllocations summarises per-node block assignments.
	NodeAllocations []NodeAllocationSummary `json:"nodeAllocations,omitempty"`

	// LastReconcileTime is the last time the controller reconciled this pool.
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// NodeAllocationSummary is a per-node view embedded in IPPoolStatus.
type NodeAllocationSummary struct {
	// NodeName is the name of the Kubernetes node.
	NodeName string `json:"nodeName"`

	// Blocks lists the CIDR blocks currently assigned to this node.
	Blocks []string `json:"blocks,omitempty"`

	// AllocatedIPs is how many individual pod IPs are in use on this node.
	AllocatedIPs int `json:"allocatedIPs"`

	// TotalCapacity is the sum of addresses in all blocks assigned to this node.
	TotalCapacity int `json:"totalCapacity"`

	// UtilizationPercent is (AllocatedIPs / TotalCapacity) * 100.
	UtilizationPercent int `json:"utilizationPercent"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Total",type="integer",JSONPath=".status.totalAddresses"
// +kubebuilder:printcolumn:name="Allocated",type="integer",JSONPath=".status.allocatedAddresses"
// +kubebuilder:printcolumn:name="Free",type="integer",JSONPath=".status.freeAddresses"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"

// IPPool is the Schema for the ippools API.
// It represents a set of CIDR ranges managed by the buddy allocator.
type IPPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPPoolSpec   `json:"spec,omitempty"`
	Status IPPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IPPoolList contains a list of IPPool.
type IPPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPPool `json:"items"`
}

// ============================================================
// NodeIPAllocation — tracks per-node block assignments
// ============================================================

// NodeIPAllocationSpec defines the desired allocation for a single node.
type NodeIPAllocationSpec struct {
	// NodeName is the Kubernetes node this allocation targets.
	NodeName string `json:"nodeName"`

	// PoolRef is the name of the IPPool this allocation draws from.
	PoolRef string `json:"poolRef"`
}

// NodeIPAllocationStatus defines the observed allocation state for a node.
type NodeIPAllocationStatus struct {
	// Blocks lists the CIDR blocks currently assigned to this node.
	Blocks []AllocatedBlock `json:"blocks,omitempty"`

	// TotalCapacity is the sum of addresses in all assigned blocks.
	TotalCapacity int `json:"totalCapacity,omitempty"`

	// AllocatedIPs is the number of pod IPs currently in use.
	AllocatedIPs int `json:"allocatedIPs,omitempty"`

	// LeakedIPs is the number of IPs that appear allocated but have no running pod.
	LeakedIPs int `json:"leakedIPs,omitempty"`

	// Phase: "Healthy", "NearExhaustion", "Exhausted".
	Phase string `json:"phase,omitempty"`

	// LastReconcileTime is the last time this allocation was checked.
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// AllocatedBlock represents a single CIDR block assigned to a node.
type AllocatedBlock struct {
	// CIDR is the block in CIDR notation, e.g. "10.244.3.0/25".
	CIDR string `json:"cidr"`

	// PrefixLength is the prefix length of this block (e.g. 25).
	PrefixLength int `json:"prefixLength"`

	// Addresses is the number of addresses in this block (2^(32-prefixLength)).
	Addresses int `json:"addresses"`

	// UsedAddresses is how many addresses in this block are occupied by pods.
	UsedAddresses int `json:"usedAddresses"`

	// State: "Active", "Draining", "Free".
	State string `json:"state"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName"
// +kubebuilder:printcolumn:name="Capacity",type="integer",JSONPath=".status.totalCapacity"
// +kubebuilder:printcolumn:name="Used",type="integer",JSONPath=".status.allocatedIPs"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"

// NodeIPAllocation is the Schema for the nodeipallocations API.
// One NodeIPAllocation exists per node, tracking its buddy-allocated blocks.
type NodeIPAllocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeIPAllocationSpec   `json:"spec,omitempty"`
	Status NodeIPAllocationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeIPAllocationList contains a list of NodeIPAllocation.
type NodeIPAllocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeIPAllocation `json:"items"`
}

// ============================================================
// NodeBlock — one allocated block
// ============================================================

// NodeBlock phases.
const (
	// NodeBlockPending: reserved by the controller, routes and CNI config not
	// yet in place everywhere.
	NodeBlockPending = "Pending"
	// NodeBlockActive: routes acknowledged and the owner's CNI config uses it.
	NodeBlockActive = "Active"
	// NodeBlockDraining: removed from the CNI config, waiting to empty.
	NodeBlockDraining = "Draining"
	// NodeBlockQuarantined: routes withdrawn, held back before reuse.
	NodeBlockQuarantined = "Quarantined"
	// NodeBlockReleased: returned to the pool.
	NodeBlockReleased = "Released"
)

// NodeBlockSpec is written only by the controller.
type NodeBlockSpec struct {
	// NodeName is the node that owns the block.
	NodeName string `json:"nodeName"`

	// PoolRef is the IPPool the block was carved from.
	PoolRef string `json:"poolRef"`

	// CIDR is the block, aligned to its own size (e.g. "10.244.0.64/27").
	CIDR string `json:"cidr"`

	// Drain asks the owner to stop handing out addresses from the block. The
	// controller sets it to shrink a node; it clears it again if the node needs
	// the space back before the drain finishes.
	// +optional
	Drain bool `json:"drain,omitempty"`
}

// NodeBlockStatus is written only by the node agent (and, for lifecycle phases
// the agent cannot observe, by the controller).
type NodeBlockStatus struct {
	// Phase is Pending, Active, Draining, Quarantined or Released.
	Phase string `json:"phase,omitempty"`

	// UsedAddresses is how many addresses in the block hold a lease, counted
	// from the node's host-local lease files.
	UsedAddresses int `json:"usedAddresses,omitempty"`

	// ReclaimedLeases counts leaked host-local leases the agent removed from
	// this block: leases with no pod behind them.
	ReclaimedLeases int `json:"reclaimedLeases,omitempty"`

	// RoutesAckedBy lists the nodes that have installed a route for this block.
	RoutesAckedBy []string `json:"routesAckedBy,omitempty"`

	// LastTransitionTime is when Phase last changed.
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName"
// +kubebuilder:printcolumn:name="CIDR",type="string",JSONPath=".spec.cidr"
// +kubebuilder:printcolumn:name="Used",type="integer",JSONPath=".status.usedAddresses"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"

// NodeBlock is one buddy block assigned to one node. Its name is derived from
// the CIDR, so the API server's name uniqueness prevents the same block being
// allocated twice. The controller keeps no private state: it rebuilds every
// buddy tree from these objects.
type NodeBlock struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeBlockSpec   `json:"spec,omitempty"`
	Status NodeBlockStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeBlockList contains a list of NodeBlock.
type NodeBlockList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeBlock `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion,
			&IPPool{}, &IPPoolList{},
			&NodeIPAllocation{}, &NodeIPAllocationList{},
			&NodeBlock{}, &NodeBlockList{})
		return nil
	})
}

// CIDRUsage is the usage of one pool CIDR.
type CIDRUsage struct {
	CIDR string `json:"cidr"`
	// State is Active (some of it is handed out), Standby (none yet) or Full.
	State     string `json:"state"`
	Total     int64  `json:"total"`
	Allocated int64  `json:"allocated"`
}
