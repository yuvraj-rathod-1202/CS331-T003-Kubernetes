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

// Package healthagent implements the DaemonSet-based health agent for dual-CNI monitoring.
//
// The health agent runs on every node (as a DaemonSet). It continuously probes
// both the primary CNI path (Calico) and the backup CNI path (Flannel) to detect
// failures. The agent:
//   - Probes other health agents over each network separately (never application pods)
//   - Performs ICMP, TCP, and large-packet (MTU) probes
//   - Checks the local CNI agent health endpoints
//   - Verifies expected routes and interfaces exist on the node
//
// This is inspired by BFD (RFC 5880): detect a dead path in sub-second time
// instead of waiting for routing-protocol timeouts.
package healthagent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// PathID identifies which network path we're probing.
type PathID string

const (
	// PrimaryPath is the primary CNI path (e.g. Calico).
	PrimaryPath PathID = "primary"

	// BackupPath is the backup CNI path (e.g. Flannel).
	BackupPath PathID = "backup"
)

// ProbeResult holds the result of probing a single path to a single peer.
type ProbeResult struct {
	// Path is which network path was probed.
	Path PathID

	// PeerNode is the name of the node we probed.
	PeerNode string

	// Healthy is true if all probes to this peer over this path succeeded.
	Healthy bool

	// ICMPSuccess is true if ICMP ping succeeded.
	ICMPSuccess bool

	// TCPSuccess is true if TCP probe succeeded.
	TCPSuccess bool

	// MTUSuccess is true if the large-packet probe succeeded (catches MTU mismatches).
	MTUSuccess bool

	// Latency is the round-trip time of the ICMP probe.
	Latency time.Duration

	// Error is set if the probe failed.
	Error string

	// Timestamp is when the probe was performed.
	Timestamp time.Time
}

// NodeHealthStatus holds the health of both paths on a single node.
type NodeHealthStatus struct {
	// NodeName is the node being monitored.
	NodeName string

	// PrimaryHealthy is true if the primary CNI path is working.
	PrimaryHealthy bool

	// BackupHealthy is true if the backup CNI path is working.
	BackupHealthy bool

	// PrimaryConsecutiveFailures counts consecutive primary path failures.
	PrimaryConsecutiveFailures int

	// BackupConsecutiveFailures counts consecutive backup path failures.
	BackupConsecutiveFailures int

	// PrimaryProbes are recent probe results for the primary path.
	PrimaryProbes []ProbeResult

	// BackupProbes are recent probe results for the backup path.
	BackupProbes []ProbeResult

	// LocalCNIAgentHealthy is true if the local CNI agent (e.g. calico-node) is healthy.
	LocalCNIAgentHealthy bool

	// LocalRoutesIntact is true if expected routes are present on this node.
	LocalRoutesIntact bool

	// LocalInterfacesIntact is true if expected interfaces (eth0, net1) exist.
	LocalInterfacesIntact bool

	// LastProbeTime is when the last probe cycle completed.
	LastProbeTime time.Time
}

// Agent is the health agent that runs on each node.
type Agent struct {
	client client.Client
	mu     sync.RWMutex

	// nodeStatuses holds the latest health status per node.
	nodeStatuses map[string]*NodeHealthStatus

	// probeInterval is how often to probe each path.
	probeInterval time.Duration

	// probeTimeout is the timeout for a single probe.
	probeTimeout time.Duration

	// failureThreshold is how many consecutive failures mark a path as down.
	failureThreshold int
}

// NewAgent creates a new health agent.
func NewAgent(c client.Client) *Agent {
	return &Agent{
		client:           c,
		nodeStatuses:     make(map[string]*NodeHealthStatus),
		probeInterval:    5 * time.Second,
		probeTimeout:     2 * time.Second,
		failureThreshold: 3,
	}
}

// Configure updates the agent's probe settings from the CR spec.
func (a *Agent) Configure(probeIntervalSec, probeTimeoutSec, failureThreshold int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.probeInterval = time.Duration(probeIntervalSec) * time.Second
	a.probeTimeout = time.Duration(probeTimeoutSec) * time.Second
	a.failureThreshold = failureThreshold
}

// ProbeAllNodes runs a probe cycle against all peer nodes and returns per-node health.
//
// TODO: Implement the probing logic:
//   1. List all nodes in the cluster (excluding the local node).
//   2. For each peer node, get its primary IP (Calico network) and backup IP (Flannel network).
//   3. Probe each peer over the primary path:
//      a. ICMP ping to the primary IP.
//      b. TCP probe to the health agent port on the primary IP.
//      c. Large-packet probe (e.g. 1400-byte ICMP) to detect MTU issues.
//   4. Probe each peer over the backup path (same 3 probes, backup IP).
//   5. Update consecutive failure counters.
//   6. Mark path as down if consecutive failures >= failureThreshold.
//   7. Return the combined health status for all nodes.
func (a *Agent) ProbeAllNodes(ctx context.Context) (map[string]*NodeHealthStatus, error) {
	logger := logf.FromContext(ctx).WithName("health-agent")
	logger.V(1).Info("Starting probe cycle")

	// TODO: Implement probing logic.

	return nil, fmt.Errorf("TODO: ProbeAllNodes not yet implemented")
}

// CheckLocalCNIAgent checks whether the local CNI agent is healthy.
//
// TODO: Implement:
//   1. Hit the Calico health endpoint (e.g. http://localhost:9099/liveness).
//   2. Hit the Flannel health endpoint (if it has one).
//   3. Check if the calico-node and flanneld processes are running.
//   4. Return status for both agents.
func (a *Agent) CheckLocalCNIAgent(ctx context.Context, healthEndpoint string) (bool, error) {
	_ = logf.FromContext(ctx).WithName("health-agent")

	// TODO: HTTP GET to the health endpoint, check for 200 OK.

	return false, fmt.Errorf("TODO: CheckLocalCNIAgent not yet implemented")
}

// VerifyLocalRoutes checks that expected routes exist on this node.
//
// TODO: Implement:
//   1. Read the routing table (e.g. parse `ip route` output or use netlink).
//   2. Verify that routes for peer node pod CIDRs exist.
//   3. For Flannel: verify that the VXLAN tunnel interface (flannel.1) exists.
//   4. Return true if all expected routes are present.
func (a *Agent) VerifyLocalRoutes(ctx context.Context) (bool, error) {
	// TODO: Implement route verification.

	return false, fmt.Errorf("TODO: VerifyLocalRoutes not yet implemented")
}

// VerifyLocalInterfaces checks that expected network interfaces exist.
//
// TODO: Implement:
//   1. Check that eth0 (or cali* interface) exists for the primary path.
//   2. Check that flannel.1 (VXLAN interface) exists for the backup path.
//   3. Return true if both interfaces are present and UP.
func (a *Agent) VerifyLocalInterfaces(ctx context.Context) (bool, error) {
	// TODO: Implement interface verification.

	return false, fmt.Errorf("TODO: VerifyLocalInterfaces not yet implemented")
}

// GetNodeHealth returns the current health status for a specific node.
func (a *Agent) GetNodeHealth(nodeName string) *NodeHealthStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.nodeStatuses[nodeName]
}

// GetAllNodeHealth returns health status for all nodes.
func (a *Agent) GetAllNodeHealth() map[string]*NodeHealthStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := make(map[string]*NodeHealthStatus, len(a.nodeStatuses))
	for k, v := range a.nodeStatuses {
		copied := *v
		result[k] = &copied
	}
	return result
}
