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

// Package monitor — Prometheus metrics for IPAM evaluation.
//
// These metrics are how we evaluate the IPAM system's effectiveness.
// They map directly to the evaluation metrics in the problem statement:
//   - Address utilisation (used ÷ allocated)
//   - Pods failed or delayed due to IP errors
//   - Number of routes per node
//   - Time to allocate and to expand
//   - Pods disrupted by shrinking
package monitor

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	metricNamespace = "ipam"
	labelNodeName   = "node_name"
)

var (
	// addressUtilization tracks per-node IP utilization as a ratio (0.0 to 1.0).
	// Labels: node_name, pool_name
	addressUtilization = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "address_utilization_ratio",
			Help:      "Ratio of used addresses to total allocated addresses per node (0.0 to 1.0).",
		},
		[]string{labelNodeName, "pool_name"},
	)

	// failedPodsTotal counts pods that failed or were delayed due to IP exhaustion.
	// Labels: node_name, reason (e.g. "no_ip_available", "cidr_not_available")
	failedPodsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "failed_pods_total",
			Help:      "Total pods that failed to start due to IP address exhaustion.",
		},
		[]string{labelNodeName, "reason"},
	)

	// routesPerNode tracks the number of routes on each node.
	// Fewer routes = better aggregation.
	routesPerNode = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "routes_per_node",
			Help:      "Number of pod-network routes on each node.",
		},
		[]string{labelNodeName},
	)

	// allocationDuration measures how long it takes to allocate a block.
	allocationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Name:      "allocation_duration_seconds",
			Help:      "Time taken to allocate a CIDR block from the buddy allocator.",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 10), // 1ms to ~1s
		},
		[]string{"operation"}, // "allocate", "expand", "free", "defragment"
	)

	// blocksAllocated tracks the total number of buddy blocks currently allocated.
	blocksAllocated = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "blocks_allocated",
			Help:      "Number of CIDR blocks currently allocated per node.",
		},
		[]string{labelNodeName, "prefix_length"},
	)

	// leakedIPsDetected counts leaked IPs that were detected and reclaimed.
	leakedIPsDetected = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "leaked_ips_total",
			Help:      "Total number of leaked IP allocations detected and reclaimed.",
		},
		[]string{labelNodeName, "action"}, // action: "detected", "reclaimed"
	)

	// poolCapacity tracks total capacity and usage at the pool level.
	poolCapacity = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "pool_addresses",
			Help:      "Total addresses in the IP pool.",
		},
		[]string{"pool_name", "state"}, // state: "total", "allocated", "free"
	)

	// defragmentEvents counts buddy merge events during defragmentation.
	defragmentEvents = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "defragment_merges_total",
			Help:      "Total number of buddy block merges during defragmentation.",
		},
	)
)

func init() {
	// Register all metrics with the controller-runtime metrics registry.
	metrics.Registry.MustRegister(
		addressUtilization,
		failedPodsTotal,
		routesPerNode,
		allocationDuration,
		blocksAllocated,
		leakedIPsDetected,
		poolCapacity,
		defragmentEvents,
	)
}

// =============================================================================
// TODO: Helper functions to record metrics from the monitor and controller.
// Call these from the appropriate places in monitor.go, leak_reclaimer.go,
// and ipam_controller.go.
// =============================================================================

// RecordNodeUtilization updates the address utilization gauge for a node.
func RecordNodeUtilization(nodeName, poolName string, ratio float64) {
	addressUtilization.WithLabelValues(nodeName, poolName).Set(ratio)
}

// RecordFailedPod increments the failed pods counter.
func RecordFailedPod(nodeName, reason string) {
	failedPodsTotal.WithLabelValues(nodeName, reason).Inc()
}

// RecordRouteCount updates the route count for a node.
func RecordRouteCount(nodeName string, count float64) {
	routesPerNode.WithLabelValues(nodeName).Set(count)
}

// ObserveAllocationDuration records the time taken for an allocation operation.
func ObserveAllocationDuration(operation string, seconds float64) {
	allocationDuration.WithLabelValues(operation).Observe(seconds)
}

// RecordBlocksAllocated updates the block count for a node at a prefix length.
func RecordBlocksAllocated(nodeName string, prefixLen int, count float64) {
	blocksAllocated.WithLabelValues(nodeName, fmt.Sprintf("/%d", prefixLen)).Set(count)
}

// RecordLeakedIP increments the leaked IP counter.
func RecordLeakedIP(nodeName, action string) {
	leakedIPsDetected.WithLabelValues(nodeName, action).Inc()
}

// RecordPoolCapacity updates pool-level capacity gauges.
func RecordPoolCapacity(poolName string, total, allocated, free float64) {
	poolCapacity.WithLabelValues(poolName, "total").Set(total)
	poolCapacity.WithLabelValues(poolName, "allocated").Set(allocated)
	poolCapacity.WithLabelValues(poolName, "free").Set(free)
}

// RecordDefragMerge increments the defragmentation merge counter.
func RecordDefragMerge() {
	defragmentEvents.Inc()
}
