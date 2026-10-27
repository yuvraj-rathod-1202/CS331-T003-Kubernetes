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

// Package monitor — Leak Reclaimer component.
//
// The LeakReclaimer periodically compares IPAM allocation records (what the
// system thinks is allocated) with the actual running pods (what really exists).
// Any address that is allocated but has no corresponding running pod is a "leak"
// and is freed after a grace period.
//
// Leaks happen when:
//   - A node crashes and the CNI DEL is never called for its pods.
//   - The container runtime restarts and loses track of pods.
//   - A race condition between pod deletion and IPAM cleanup.
package monitor

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// defaultGracePeriod is how long an orphaned allocation is left before reclaiming.
	// This avoids reclaiming addresses for pods that are still starting up.
	defaultGracePeriod = 2 * time.Minute
)

// LeakedAllocation represents an IP address that appears allocated but has no pod.
type LeakedAllocation struct {
	// IP is the leaked address.
	IP string

	// NodeName is the node that owns the block containing this IP.
	NodeName string

	// BlockCIDR is the CIDR block this IP belongs to.
	BlockCIDR string

	// DetectedAt is when the leak was first detected.
	DetectedAt time.Time

	// GracePeriodExpired is true if enough time has passed to safely reclaim.
	GracePeriodExpired bool
}

// LeakReclaimer compares IPAM records with actual pods and frees leaked addresses.
type LeakReclaimer struct {
	client      client.Client
	gracePeriod time.Duration

	// knownLeaks tracks leaked allocations and when they were first detected.
	// Key: IP address string.
	knownLeaks map[string]*LeakedAllocation
}

// NewLeakReclaimer creates a new leak reclaimer.
func NewLeakReclaimer(c client.Client) *LeakReclaimer {
	return &LeakReclaimer{
		client:      c,
		gracePeriod: defaultGracePeriod,
		knownLeaks:  make(map[string]*LeakedAllocation),
	}
}

// SetGracePeriod overrides the default grace period for testing.
func (lr *LeakReclaimer) SetGracePeriod(d time.Duration) {
	lr.gracePeriod = d
}

// DetectLeaks compares allocated IPs (from NodeIPAllocation CRs or IPAM records)
// with actually running pods and identifies leaks.
//
// TODO: Implement:
//  1. Get all NodeIPAllocation CRs → extract all allocated IPs per node.
//     (For now, we can use the host-local data dir or our own records.)
//  2. List all Pods across the cluster that have a PodIP.
//  3. Build a set of "actually used IPs".
//  4. For each allocated IP not in the "actually used" set:
//     a. If it's new, add it to knownLeaks with DetectedAt = now.
//     b. If it's already in knownLeaks and grace period has passed, mark GracePeriodExpired.
//  5. Remove entries from knownLeaks if the IP is now in use (pod came back).
//  6. Return the list of leaked allocations.
func (lr *LeakReclaimer) DetectLeaks(ctx context.Context) ([]LeakedAllocation, error) {
	logger := logf.FromContext(ctx).WithName("leak-reclaimer")
	logger.V(1).Info("Scanning for leaked IP allocations")

	// TODO: Implement leak detection.

	return nil, fmt.Errorf("TODO: DetectLeaks not yet implemented")
}

// ReclaimLeaks frees all leaked allocations that have passed the grace period.
//
// TODO: Implement:
//  1. Call DetectLeaks to get current leaks.
//  2. For each leak where GracePeriodExpired is true:
//     a. Free the IP from the IPAM allocation records.
//     b. Log the reclamation event.
//  3. Return the count of reclaimed addresses.
func (lr *LeakReclaimer) ReclaimLeaks(ctx context.Context) (int, error) {
	logger := logf.FromContext(ctx).WithName("leak-reclaimer")
	logger.Info("Running leak reclamation")

	// TODO: Implement leak reclamation.

	return 0, fmt.Errorf("TODO: ReclaimLeaks not yet implemented")
}

// GetKnownLeaks returns the current set of known leaked allocations (for status reporting).
func (lr *LeakReclaimer) GetKnownLeaks() []LeakedAllocation {
	result := make([]LeakedAllocation, 0, len(lr.knownLeaks))
	for _, leak := range lr.knownLeaks {
		result = append(result, *leak)
	}
	return result
}
