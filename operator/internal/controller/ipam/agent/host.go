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

package agent

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// RouteTable manages the routes the agent owns.
type RouteTable interface {
	// Managed lists the routes carrying RouteProtocol.
	Managed() ([]Route, error)
	// Replace installs or updates a route.
	Replace(Route) error
	// Delete removes a route.
	Delete(Route) error
}

// CountLeases counts host-local lease files per block. host-local stores one
// file per allocated address, named by the address, in its data directory. This
// is the ground truth: it includes leaked leases that no pod accounts for.
// Files that are not addresses (last_reserved_ip.*, lock) are ignored.
func CountLeases(dir string, cidrs []string) (map[string]int, error) {
	nets := make(map[string]*net.IPNet, len(cidrs))
	counts := make(map[string]int, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", c, err)
		}
		nets[c] = n
		counts[c] = 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return counts, nil
		}
		return nil, err
	}
	for _, e := range entries {
		ip := net.ParseIP(e.Name())
		if ip == nil {
			continue
		}
		for c, n := range nets {
			if n.Contains(ip) {
				counts[c]++
				break
			}
		}
	}
	return counts, nil
}

// WriteFileIfChanged writes data to path atomically (temp file + rename) and
// reports whether it changed anything. The container runtime watches the
// directory, so a half-written file must never be visible.
func WriteFileIfChanged(path string, data []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".buddy-*.tmp")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

// PodIPs is the set of addresses the given pods hold.
func PodIPs(pods []corev1.Pod) map[string]bool {
	out := map[string]bool{}
	for i := range pods {
		if ip := pods[i].Status.PodIP; ip != "" {
			out[ip] = true
		}
		for _, p := range pods[i].Status.PodIPs {
			out[p.IP] = true
		}
	}
	return out
}

// ReclaimLeases deletes the lease files for addresses inside cidrs that no pod
// holds (podIPs) and that are older than minAge. The age guard covers a pod
// whose sandbox has its address but whose status has not been reported yet. It
// returns how many leases were removed per block. Removing the file is exactly
// what host-local's DEL does, so the address is free again at once.
func ReclaimLeases(dir string, cidrs []string, podIPs map[string]bool, minAge time.Duration, now time.Time) (map[string]int, error) {
	nets := make(map[string]*net.IPNet, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", c, err)
		}
		nets[c] = n
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string]int{}
	var firstErr error
	for _, e := range entries {
		ip := net.ParseIP(e.Name())
		if ip == nil || podIPs[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < minAge {
			continue
		}
		for c, n := range nets {
			if !n.Contains(ip) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			out[c]++
			break
		}
	}
	return out, firstErr
}
