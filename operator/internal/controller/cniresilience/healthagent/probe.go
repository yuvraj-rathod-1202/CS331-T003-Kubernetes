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

// Package healthagent — probe implementations.
//
// This file contains the low-level probe implementations used by the health agent.
// Each probe tests a different aspect of network connectivity:
//   - ICMP ping: basic reachability
//   - TCP probe: transport-layer connectivity (can we establish a connection?)
//   - Large-packet probe: MTU correctness (VXLAN MTU mismatches silently drop large packets)
package healthagent

import (
	"context"
	"fmt"
	"time"
)

// Prober defines the interface for network probes.
type Prober interface {
	// Probe performs a single probe to the target and returns the result.
	Probe(ctx context.Context, target string) (ProbeOutcome, error)

	// Name returns the probe type name (e.g. "icmp", "tcp", "mtu").
	Name() string
}

// ProbeOutcome holds the result of a single probe attempt.
type ProbeOutcome struct {
	// Success is true if the probe succeeded.
	Success bool

	// Latency is the round-trip time.
	Latency time.Duration

	// Message is a human-readable result description.
	Message string
}

// ICMPProber sends ICMP echo requests (ping).
//
// TODO: Implement using raw sockets or the `golang.org/x/net/icmp` package.
// Note: raw sockets require CAP_NET_RAW capability in the DaemonSet pod.
type ICMPProber struct {
	// Timeout is the maximum time to wait for a reply.
	Timeout time.Duration

	// PacketSize is the ICMP payload size in bytes. Default: 56 (standard ping).
	PacketSize int
}

// NewICMPProber creates a standard ICMP prober.
func NewICMPProber(timeout time.Duration) *ICMPProber {
	return &ICMPProber{
		Timeout:    timeout,
		PacketSize: 56,
	}
}

func (p *ICMPProber) Name() string { return "icmp" }

// Probe sends an ICMP echo request to the target IP.
//
// TODO: Implement:
//   1. Create a raw ICMP socket (or use golang.org/x/net/icmp).
//   2. Send an echo request with p.PacketSize payload.
//   3. Wait for echo reply up to p.Timeout.
//   4. Measure round-trip time.
//   5. Return ProbeOutcome with success/failure and latency.
func (p *ICMPProber) Probe(ctx context.Context, target string) (ProbeOutcome, error) {
	// TODO: Implement ICMP probe.

	return ProbeOutcome{}, fmt.Errorf("TODO: ICMPProber.Probe not yet implemented")
}

// TCPProber attempts a TCP connection to a target host:port.
//
// TODO: Implement using net.DialTimeout.
// The health agent on the peer node listens on a known port (e.g. 9100).
type TCPProber struct {
	// Timeout is the maximum time to wait for the connection.
	Timeout time.Duration

	// Port is the TCP port to probe on the target.
	Port int
}

// NewTCPProber creates a TCP prober for the given port.
func NewTCPProber(timeout time.Duration, port int) *TCPProber {
	return &TCPProber{
		Timeout: timeout,
		Port:    port,
	}
}

func (p *TCPProber) Name() string { return "tcp" }

// Probe attempts a TCP connection to target:port.
//
// TODO: Implement:
//   1. Use net.DialTimeout("tcp", target:port, p.Timeout).
//   2. If connection succeeds, close it and return success.
//   3. If it fails, return failure with the error message.
func (p *TCPProber) Probe(ctx context.Context, target string) (ProbeOutcome, error) {
	// TODO: Implement TCP probe.

	return ProbeOutcome{}, fmt.Errorf("TODO: TCPProber.Probe not yet implemented")
}

// MTUProber sends large ICMP packets to detect MTU mismatches.
//
// This catches the professor's VXLAN MTU problem: small packets (like regular pings)
// pass through fine, but large packets are silently dropped because the VXLAN
// overhead (50 bytes) reduces the effective MTU.
//
// Standard MTU: 1500. VXLAN overhead: 50. So effective MTU = 1450.
// If one side has MTU 1500 and the other 1450, packets > 1450 bytes are dropped.
//
// TODO(Deep): Implement using raw sockets with DF (Don't Fragment) bit set.
type MTUProber struct {
	// Timeout is the maximum time to wait for a reply.
	Timeout time.Duration

	// PacketSize is the large payload size (e.g. 1400 bytes to test near-MTU).
	PacketSize int
}

// NewMTUProber creates a probe that sends large packets.
func NewMTUProber(timeout time.Duration) *MTUProber {
	return &MTUProber{
		Timeout:    timeout,
		PacketSize: 1400, // Just under standard VXLAN effective MTU
	}
}

func (p *MTUProber) Name() string { return "mtu" }

// Probe sends a large ICMP packet with the Don't Fragment bit set.
//
// TODO(Deep): Implement:
//   1. Create a raw ICMP socket.
//   2. Set the Don't Fragment (DF) bit on the IP header.
//   3. Send an echo request with p.PacketSize payload.
//   4. If we get a "message too big" ICMP error, the MTU is wrong → failure.
//   5. If we get a normal echo reply, MTU is fine → success.
func (p *MTUProber) Probe(ctx context.Context, target string) (ProbeOutcome, error) {
	// TODO(Deep): Implement MTU probe.

	return ProbeOutcome{}, fmt.Errorf("TODO: MTUProber.Probe not yet implemented")
}

// RunProbes runs all probers against a target and returns aggregated results.
//
// TODO(Deep): This is a convenience function. Implement:
//   1. Run each prober in the list.
//   2. Collect results.
//   3. Return all results (the caller decides what constitutes "healthy").
func RunProbes(ctx context.Context, target string, probers ...Prober) ([]ProbeOutcome, error) {
	results := make([]ProbeOutcome, 0, len(probers))

	for _, prober := range probers {
		outcome, err := prober.Probe(ctx, target)
		if err != nil {
			// Probe infrastructure error (not a connectivity failure).
			return nil, fmt.Errorf("prober %s error: %w", prober.Name(), err)
		}
		results = append(results, outcome)
	}

	return results, nil
}
