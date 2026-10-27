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
//
// Peer IP Discovery strategy (decided by the team):
//
//	The backup (Flannel/net1) IP for each node is discovered by reading the
//	Multus "k8s.v1.cni.cncf.io/network-status" annotation on any pod running
//	on that node. The primary (Calico/eth0) IP is the node's InternalIP from
//	node.Status.Addresses.
package healthagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// PathID identifies which network path we're probing.
type PathID string

const (
	// PrimaryPath is the primary CNI path (e.g. Calico eth0).
	PrimaryPath PathID = "primary"

	// BackupPath is the backup CNI path (e.g. Flannel net1).
	BackupPath PathID = "backup"

	// multusNetworkStatusAnnotation is the Multus annotation key that lists all
	// network interfaces assigned to a pod, including secondary ones.
	multusNetworkStatusAnnotation = "k8s.v1.cni.cncf.io/network-status"

	// backupNetworkName is the name of the Multus NetworkAttachmentDefinition
	// for the Flannel secondary network (matches deploy/multus-nad.yaml).
	backupNetworkName = "flannel-backup"

	// calicoHealthEndpointDefault is the Calico node health check URL.
	// The calico-node pod exposes a Felix liveness endpoint.
	calicoHealthEndpointDefault = "http://localhost:9099/liveness"
)

// multusEntry represents one element in the Multus network-status JSON array.
type multusEntry struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface"`
	IPs       []string `json:"ips"`
}

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

	// LocalInterfacesIntact is true if expected interfaces (cali*, flannel.1) exist.
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

	// localNodeName is the name of the node this agent pod runs on.
	// Populated from the NODE_NAME env var injected by the DaemonSet.
	localNodeName string
}

// NewAgent creates a new health agent.
func NewAgent(c client.Client) *Agent {
	return &Agent{
		client:           c,
		nodeStatuses:     make(map[string]*NodeHealthStatus),
		probeInterval:    5 * time.Second,
		probeTimeout:     2 * time.Second,
		failureThreshold: 3,
		localNodeName:    os.Getenv("NODE_NAME"),
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
// Strategy:
//  1. List all nodes. Skip the local node (we probe peers, not ourselves).
//  2. For each node, discover:
//     - Primary IP: node.Status.Addresses[InternalIP]
//     - Backup IP: parse Multus annotation on any pod running on that node.
//  3. Run ICMP + TCP + MTU probes over primary path -> update PrimaryConsecutiveFailures.
//  4. Run ICMP + TCP + MTU probes over backup path -> update BackupConsecutiveFailures.
//  5. Mark path as healthy/unhealthy based on failureThreshold.
func (a *Agent) ProbeAllNodes(ctx context.Context) (map[string]*NodeHealthStatus, error) {
	logger := logf.FromContext(ctx).WithName("health-agent")
	logger.V(1).Info("Starting probe cycle")

	// 1. List all nodes.
	nodeList := &corev1.NodeList{}
	if err := a.client.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	// Build probers using configured timeout.
	a.mu.RLock()
	timeout := a.probeTimeout
	threshold := a.failureThreshold
	localNode := a.localNodeName
	a.mu.RUnlock()

	icmpProber := NewICMPProber(timeout)
	tcpProber := NewTCPProber(timeout, healthAgentPort)
	mtuProber := NewMTUProber(timeout)

	results := make(map[string]*NodeHealthStatus)

	for _, node := range nodeList.Items {
		nodeName := node.Name
		if nodeName == localNode {
			continue // Skip ourselves; we probe peers only.
		}

		// 2a. Primary IP from node's InternalIP address.
		primaryIP := getNodeInternalIP(node)
		if primaryIP == "" {
			logger.V(1).Info("Node has no InternalIP, skipping", "node", nodeName)
			continue
		}

		// 2b. Backup IP from Multus annotation on a pod on this node.
		backupIP, err := a.discoverBackupIP(ctx, nodeName)
		if err != nil {
			logger.V(1).Info("Could not discover backup IP", "node", nodeName, "err", err)
			// We still probe the primary path; backup probes will mark as failed.
		}

		// Retrieve or initialise the per-node status.
		a.mu.Lock()
		status, exists := a.nodeStatuses[nodeName]
		if !exists {
			status = &NodeHealthStatus{
				NodeName:       nodeName,
				PrimaryHealthy: true,
				BackupHealthy:  true,
			}
			a.nodeStatuses[nodeName] = status
		}
		a.mu.Unlock()

		// 3. Probe primary path (Calico / eth0).
		primaryResult := a.probeOnePath(ctx, nodeName, PrimaryPath, primaryIP,
			icmpProber, tcpProber, mtuProber)

		// 4. Probe backup path (Flannel / net1).
		var backupResult ProbeResult
		if backupIP != "" {
			backupResult = a.probeOnePath(ctx, nodeName, BackupPath, backupIP,
				icmpProber, tcpProber, mtuProber)
		} else {
			backupResult = ProbeResult{
				Path:      BackupPath,
				PeerNode:  nodeName,
				Healthy:   false,
				Error:     "backup IP not discoverable",
				Timestamp: time.Now(),
			}
		}

		// 5. Update consecutive failure counters and derive healthy flag.
		a.mu.Lock()
		// Primary.
		if primaryResult.Healthy {
			status.PrimaryConsecutiveFailures = 0
		} else {
			status.PrimaryConsecutiveFailures++
		}
		status.PrimaryHealthy = status.PrimaryConsecutiveFailures < threshold

		// Backup.
		if backupResult.Healthy {
			status.BackupConsecutiveFailures = 0
		} else {
			status.BackupConsecutiveFailures++
		}
		status.BackupHealthy = status.BackupConsecutiveFailures < threshold

		// Keep a short rolling window of probe results.
		status.PrimaryProbes = appendProbeResult(status.PrimaryProbes, primaryResult, 5)
		status.BackupProbes = appendProbeResult(status.BackupProbes, backupResult, 5)
		status.LastProbeTime = time.Now()

		// Snapshot for return.
		copied := *status
		a.mu.Unlock()

		results[nodeName] = &copied
	}

	// Persist latest state.
	a.mu.Lock()
	for k, v := range results {
		a.nodeStatuses[k] = v
	}
	a.mu.Unlock()

	return results, nil
}

// probeOnePath runs ICMP + TCP + MTU probes to a target IP and returns aggregated result.
func (a *Agent) probeOnePath(ctx context.Context, peerNode string, path PathID, targetIP string,
	icmpP *ICMPProber, tcpP *TCPProber, mtuP *MTUProber) ProbeResult {

	result := ProbeResult{
		Path:      path,
		PeerNode:  peerNode,
		Timestamp: time.Now(),
	}

	// ICMP probe.
	icmpOut, err := icmpP.Probe(ctx, targetIP)
	if err == nil && icmpOut.Success {
		result.ICMPSuccess = true
		result.Latency = icmpOut.Latency
	}

	// TCP probe.
	tcpOut, err := tcpP.Probe(ctx, targetIP)
	if err == nil && tcpOut.Success {
		result.TCPSuccess = true
	}

	// MTU probe (large ICMP packet).
	mtuOut, err := mtuP.Probe(ctx, targetIP)
	if err == nil && mtuOut.Success {
		result.MTUSuccess = true
	}

	// A path is healthy only if ALL three probes succeed.
	// This prevents MTU black-holes being reported as healthy just because ping works.
	result.Healthy = result.ICMPSuccess && result.TCPSuccess && result.MTUSuccess

	if !result.Healthy {
		msgs := []string{}
		if !result.ICMPSuccess {
			msgs = append(msgs, "ICMP failed")
		}
		if !result.TCPSuccess {
			msgs = append(msgs, fmt.Sprintf("TCP:%d failed", healthAgentPort))
		}
		if !result.MTUSuccess {
			msgs = append(msgs, "MTU probe failed (possible VXLAN MTU mismatch)")
		}
		result.Error = strings.Join(msgs, "; ")
	}

	return result
}

// discoverBackupIP finds the Flannel backup IP for a node by reading the
// Multus network-status annotation on any pod running on that node.
//
// The Multus annotation looks like:
//
//	[
//	  {"name":"cluster-network","interface":"eth0","ips":["10.244.1.5"]},
//	  {"name":"flannel-backup",  "interface":"net1","ips":["192.168.1.5"]}
//	]
//
// We find any pod on the node, parse its annotation, and extract the IP for
// the "flannel-backup" network.
func (a *Agent) discoverBackupIP(ctx context.Context, nodeName string) (string, error) {
	podList := &corev1.PodList{}
	if err := a.client.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return "", fmt.Errorf("list pods on node %s: %w", nodeName, err)
	}

	for _, pod := range podList.Items {
		annotation, ok := pod.Annotations[multusNetworkStatusAnnotation]
		if !ok || annotation == "" {
			continue
		}

		var entries []multusEntry
		if err := json.Unmarshal([]byte(annotation), &entries); err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.Name == backupNetworkName || entry.Interface == "net1" {
				if len(entry.IPs) > 0 {
					return entry.IPs[0], nil
				}
			}
		}
	}

	return "", fmt.Errorf("no pod on node %s has a Multus %q network annotation", nodeName, backupNetworkName)
}

// CheckLocalCNIAgent checks whether the local CNI agent is healthy by hitting
// its HTTP health endpoint. Calico's Felix liveness endpoint is at
// http://localhost:9099/liveness by default.
func (a *Agent) CheckLocalCNIAgent(ctx context.Context, healthEndpoint string) (bool, error) {
	logger := logf.FromContext(ctx).WithName("health-agent")

	if healthEndpoint == "" {
		healthEndpoint = calicoHealthEndpointDefault
	}

	httpClient := &http.Client{Timeout: a.probeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthEndpoint, nil)
	if err != nil {
		return false, fmt.Errorf("build request for %s: %w", healthEndpoint, err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		logger.V(1).Info("CNI agent health endpoint unreachable", "url", healthEndpoint, "err", err)
		return false, nil //nolint:nilerr // connectivity failure, not infra error
	}
	defer resp.Body.Close() //nolint:errcheck

	healthy := resp.StatusCode == http.StatusOK
	if !healthy {
		logger.V(1).Info("CNI agent health endpoint returned non-200",
			"url", healthEndpoint, "status", resp.StatusCode)
	}
	return healthy, nil
}

// VerifyLocalRoutes checks that expected routes exist on this node by reading
// /proc/net/route (Linux kernel routing table). Each entry corresponds to a
// route in the main routing table. We verify that there is at least one
// non-default, non-loopback route — a sign that the CNI has installed pod routes.
//
// A proper implementation would parse the kernel route table and check for
// specific peer node pod CIDRs; for the course demo this heuristic is sufficient.
func (a *Agent) VerifyLocalRoutes(ctx context.Context) (bool, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false, fmt.Errorf("read /proc/net/route: %w", err)
	}

	// /proc/net/route columns: Iface Destination Gateway Flags RefCnt Use Metric Mask ...
	// Destination 00000000 = default route. We want routes that are:
	//   - Not the default route (Destination != "00000000")
	//   - Not loopback (Destination != "0000007F" = 127.0.0.1/8)
	lines := strings.Split(string(data), "\n")
	podRouteCount := 0
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		dest := fields[1]
		if dest == "00000000" || dest == "0000007F" || dest == "" {
			continue
		}
		podRouteCount++
	}

	return podRouteCount > 0, nil
}

// VerifyLocalInterfaces checks that expected network interfaces exist.
// For Calico: at least one "cali*" veth interface should exist.
// For Flannel: the "flannel.1" VXLAN interface should exist.
//
// We read /proc/net/dev which lists all network interfaces.
func (a *Agent) VerifyLocalInterfaces(ctx context.Context) (bool, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return false, fmt.Errorf("read /proc/net/dev: %w", err)
	}

	var hasCali, hasFlannel bool
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "cali") {
			hasCali = true
		}
		if strings.HasPrefix(trimmed, "flannel.1") {
			hasFlannel = true
		}
	}

	// Both must be present for a fully functioning dual-CNI node.
	return hasCali && hasFlannel, nil
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

// Helpers

// getNodeInternalIP extracts the first InternalIP from a node's address list.
func getNodeInternalIP(node corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

// appendProbeResult appends a result and keeps only the last `max` entries.
func appendProbeResult(history []ProbeResult, result ProbeResult, max int) []ProbeResult {
	history = append(history, result)
	if len(history) > max {
		history = history[len(history)-max:]
	}
	return history
}
