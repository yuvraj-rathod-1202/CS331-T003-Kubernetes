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
	"net"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// healthAgentPort is the port the health agent TCP listener binds on every node.
// NetworkPolicy explicitly allows this port between health agent pods.
const healthAgentPort = 9100

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

// ICMPProber

// ICMPProber sends ICMP echo requests (ping) using golang.org/x/net/icmp.
// The DaemonSet runs with hostNetwork:true and CAP_NET_RAW, which are both
// required to open a raw IP socket.
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

// Probe sends an ICMP echo request to target (IPv4 address string) and measures RTT.
// It requires CAP_NET_RAW. The network "ip4:icmp" opens a raw IPv4 socket.
func (p *ICMPProber) Probe(ctx context.Context, target string) (ProbeOutcome, error) {
	// Open raw ICMP socket. Requires CAP_NET_RAW.
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return ProbeOutcome{}, fmt.Errorf("icmp: open raw socket: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	deadline := time.Now().Add(p.Timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return ProbeOutcome{}, fmt.Errorf("icmp: set deadline: %w", err)
	}

	// Build ICMP echo request.
	payload := make([]byte, p.PacketSize)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   1,
			Seq:  1,
			Data: payload,
		},
	}
	msgBytes, err := msg.Marshal(nil)
	if err != nil {
		return ProbeOutcome{}, fmt.Errorf("icmp: marshal: %w", err)
	}

	dst := &net.IPAddr{IP: net.ParseIP(target)}
	start := time.Now()

	if _, err := conn.WriteTo(msgBytes, dst); err != nil {
		return ProbeOutcome{
			Success: false,
			Message: fmt.Sprintf("icmp: send to %s: %v", target, err),
		}, nil
	}

	// Wait for echo reply.
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return ProbeOutcome{Success: false, Message: "context cancelled"}, nil
		default:
		}

		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return ProbeOutcome{
				Success: false,
				Latency: time.Since(start),
				Message: fmt.Sprintf("icmp: read reply from %s: %v", target, err),
			}, nil
		}

		rm, err := icmp.ParseMessage(1 /* ICMPv4 */, buf[:n])
		if err != nil {
			continue
		}

		if rm.Type == ipv4.ICMPTypeEchoReply {
			// Verify it is a reply to our request (not another host's traffic).
			if reply, ok := rm.Body.(*icmp.Echo); ok && reply.ID == 1 && reply.Seq == 1 {
				_ = peer
				latency := time.Since(start)
				return ProbeOutcome{
					Success: true,
					Latency: latency,
					Message: fmt.Sprintf("icmp echo reply from %s in %v", target, latency),
				}, nil
			}
		}
	}
}

// TCPProber

// TCPProber attempts a TCP connection to a target host:port.
// The health agent on the peer node listens on healthAgentPort (9100).
type TCPProber struct {
	// Timeout is the maximum time to wait for the connection.
	Timeout time.Duration

	// Port is the TCP port to probe on the target.
	Port int
}

// NewTCPProber creates a TCP prober for the health agent port.
func NewTCPProber(timeout time.Duration, port int) *TCPProber {
	return &TCPProber{
		Timeout: timeout,
		Port:    port,
	}
}

func (p *TCPProber) Name() string { return "tcp" }

// Probe attempts a TCP connection to target:port and measures connection time.
func (p *TCPProber) Probe(ctx context.Context, target string) (ProbeOutcome, error) {
	addr := fmt.Sprintf("%s:%d", target, p.Port)
	start := time.Now()

	d := net.Dialer{Timeout: p.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return ProbeOutcome{
			Success: false,
			Latency: time.Since(start),
			Message: fmt.Sprintf("tcp: dial %s: %v", addr, err),
		}, nil
	}
	latency := time.Since(start)
	conn.Close() //nolint:errcheck

	return ProbeOutcome{
		Success: true,
		Latency: latency,
		Message: fmt.Sprintf("tcp: connected to %s in %v", addr, latency),
	}, nil
}

// MTUProber

// MTUProber sends large ICMP packets to detect MTU mismatches.
//
// This catches the VXLAN MTU problem: small packets (like regular pings)
// pass through fine, but large packets are silently dropped because the VXLAN
// overhead (50 bytes) reduces the effective MTU.
//
// Standard Ethernet MTU: 1500. VXLAN overhead: 50. Effective MTU = 1450.
// We probe with 1400-byte payloads - a healthy VXLAN tunnel must pass these.
type MTUProber struct {
	// Timeout is the maximum time to wait for a reply.
	Timeout time.Duration

	// PacketSize is the large payload size to send.
	PacketSize int
}

// NewMTUProber creates a probe that sends large packets.
func NewMTUProber(timeout time.Duration) *MTUProber {
	return &MTUProber{
		Timeout:    timeout,
		PacketSize: 1400, // Just under VXLAN effective MTU - must pass through healthy tunnels.
	}
}

func (p *MTUProber) Name() string { return "mtu" }

// Probe sends a large ICMP packet. A failure here (when small ICMP succeeds)
// strongly indicates an MTU mismatch on the VXLAN tunnel.
func (p *MTUProber) Probe(ctx context.Context, target string) (ProbeOutcome, error) {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return ProbeOutcome{}, fmt.Errorf("mtu: open raw socket: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	deadline := time.Now().Add(p.Timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return ProbeOutcome{}, fmt.Errorf("mtu: set deadline: %w", err)
	}

	// Large payload to expose MTU problems.
	payload := make([]byte, p.PacketSize)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   2, // Distinct ID from ICMPProber to avoid reply confusion.
			Seq:  1,
			Data: payload,
		},
	}
	msgBytes, err := msg.Marshal(nil)
	if err != nil {
		return ProbeOutcome{}, fmt.Errorf("mtu: marshal: %w", err)
	}

	dst := &net.IPAddr{IP: net.ParseIP(target)}
	start := time.Now()

	if _, err := conn.WriteTo(msgBytes, dst); err != nil {
		return ProbeOutcome{
			Success: false,
			Message: fmt.Sprintf("mtu: send %d-byte packet to %s: %v", p.PacketSize, target, err),
		}, nil
	}

	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return ProbeOutcome{Success: false, Message: "context cancelled"}, nil
		default:
		}

		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return ProbeOutcome{
				Success: false,
				Latency: time.Since(start),
				Message: fmt.Sprintf("mtu: no reply for %d-byte packet to %s (likely MTU drop): %v", p.PacketSize, target, err),
			}, nil
		}

		rm, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			continue
		}

		switch rm.Type {
		case ipv4.ICMPTypeEchoReply:
			if reply, ok := rm.Body.(*icmp.Echo); ok && reply.ID == 2 && reply.Seq == 1 {
				latency := time.Since(start)
				return ProbeOutcome{
					Success: true,
					Latency: latency,
					Message: fmt.Sprintf("mtu: %d-byte reply from %s in %v — tunnel MTU OK", p.PacketSize, target, latency),
				}, nil
			}
		case ipv4.ICMPTypeDestinationUnreachable:
			// "Fragmentation needed but DF set" — classic MTU problem signal.
			return ProbeOutcome{
				Success: false,
				Latency: time.Since(start),
				Message: fmt.Sprintf("mtu: ICMP destination unreachable (fragmentation needed) from %s — MTU MISMATCH", target),
			}, nil
		}
	}
}

// RunProbes helper

// RunProbes runs all probers against a target and returns aggregated results.
// A probe infrastructure error (e.g., failed to open socket) is returned as
// an error. A connectivity failure (e.g., timeout, refused) is returned as
// ProbeOutcome{Success: false} — not an error.
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
