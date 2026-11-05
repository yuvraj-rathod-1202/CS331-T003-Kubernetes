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

package healthagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

const (
	// Port is the port agents probe each other on.
	Port = 9100

	// agentLabel selects agent pods.
	agentLabel = "cni-health-agent"

	// largeBodyBytes makes the large probe fill full-size TCP segments, which Linux
	// sends with DF set: a path whose MTU is too small drops them while small
	// requests still pass. 1400 bytes fits Calico IPIP (1480) and Flannel VXLAN (1450).
	largeBodyBytes = 1400

	multusNetworkStatusAnnotation = "k8s.v1.cni.cncf.io/network-status"
)

// RingOffsets returns the offsets a cluster of n nodes rotates through: 1 to n/2.
// The offset where 2r = n stays in, although that one round alone cannot separate a
// node from a link: without it, opposite nodes on an even ring are never paired.
// Reports keep the latest result per peer, so the sweep as a whole can separate them.
func RingOffsets(n int) []int {
	var offsets []int
	for r := 1; r <= n/2; r++ {
		offsets = append(offsets, r)
	}
	return offsets
}

// RingTarget returns which node this one probes in the given round, or "" if it
// has nobody to probe. Round numbers come from the clock, so all agents agree.
func RingTarget(sortedNodes []string, self string, round int64) string {
	offsets := RingOffsets(len(sortedNodes))
	index := slices.Index(sortedNodes, self)
	if len(offsets) == 0 || index < 0 {
		return ""
	}
	offset := offsets[round%int64(len(offsets))]
	return sortedNodes[(index+offset)%len(sortedNodes)]
}

// streak turns raw probe results into a stable state: it flips only after threshold
// identical results in a row, so one lost packet or one lucky reply changes nothing.
type streak struct {
	healthy bool
	run     int
}

func (s *streak) observe(ok bool, threshold int) {
	if ok == s.healthy {
		s.run = 0
		return
	}
	s.run++
	if s.run >= threshold {
		s.healthy = ok
		s.run = 0
	}
}

// forgetPeerAfter is how long a peer may be missing (node deleted, agent gone)
// before its probe state is dropped from this agent's report.
const forgetPeerAfter = 5 * time.Minute

// Daemon is the agent that runs on each node.
type Daemon struct {
	Client   client.Client
	NodeName string
	// Namespace is where the agent pods run. Peers are looked up only there, so a
	// pod elsewhere that copies the agent label cannot pose as a node's agent.
	Namespace string

	mu       sync.Mutex
	primary  map[string]*streak
	backup   map[string]*streak
	lastSeen map[string]time.Time
	// backupNet is the backup CNI's address space; backup IPs outside it are ignored.
	backupNet *net.IPNet
	lastSent  string
	lastTime  time.Time
}

// probeSettings turns the CR's probe fields into safe values. The CRD has minimums,
// but a zero interval would divide by zero and a zero timeout would mean "wait
// forever" while the probe holds the agent's lock.
func probeSettings(spec remediationv1alpha1.CNIResilienceSpec) (interval, timeout time.Duration, threshold int) {
	interval = time.Duration(max(spec.ProbeIntervalSeconds, 1)) * time.Second
	timeout = time.Duration(max(spec.ProbeTimeoutSeconds, 1)) * time.Second
	timeout = min(timeout, interval)
	threshold = max(spec.FailureThreshold, 1)
	return interval, timeout, threshold
}

// Serve answers peers' probes.
func Serve(ctx context.Context) error {
	body := make([]byte, largeBodyBytes)
	mux := http.NewServeMux()
	mux.HandleFunc("/small", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/large", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	server := &http.Server{Addr: ":" + strconv.Itoa(Port), Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Run probes one ring target per round until ctx ends.
func (d *Daemon) Run(ctx context.Context) {
	logger := logf.FromContext(ctx).WithName("health-agent")
	d.primary = make(map[string]*streak)
	d.backup = make(map[string]*streak)
	d.lastSeen = make(map[string]time.Time)

	interval, timeout, threshold := 2*time.Second, time.Second, 3
	for {
		var list remediationv1alpha1.CNIResilienceList
		if err := d.Client.List(ctx, &list); err == nil && len(list.Items) > 0 {
			spec := list.Items[0].Spec
			interval, timeout, threshold = probeSettings(spec)
			_, network, err := net.ParseCIDR(spec.BackupCNI.AddressSpace)
			if err != nil {
				network = nil
			}
			d.mu.Lock()
			d.backupNet = network
			d.mu.Unlock()
		}

		if err := d.round(ctx, interval, timeout, threshold); err != nil {
			logger.Error(err, "Probe round failed")
		}

		wait := interval - time.Duration(time.Now().UnixNano())%interval
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (d *Daemon) round(ctx context.Context, interval, timeout time.Duration, threshold int) error {
	var pods corev1.PodList
	opts := []client.ListOption{client.MatchingLabels{"app": agentLabel}}
	if d.Namespace != "" {
		opts = append(opts, client.InNamespace(d.Namespace))
	}
	if err := d.Client.List(ctx, &pods, opts...); err != nil {
		return d.publish(ctx)
	}

	d.mu.Lock()
	backupNet := d.backupNet
	d.mu.Unlock()

	type peer struct{ primaryIP, backupIP string }
	peers := make(map[string]peer)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.PodIP != "" && pod.Status.Phase == corev1.PodRunning && ownedByDaemonSet(pod) {
			peers[pod.Spec.NodeName] = peer{pod.Status.PodIP, backupIP(pod, backupNet)}
		}
	}
	d.forgetMissingPeers(slices.Collect(maps.Keys(peers)), time.Now())

	names := slices.Sorted(maps.Keys(peers))
	target := RingTarget(names, d.NodeName, time.Now().UnixNano()/int64(interval))
	if target != "" {
		d.mu.Lock()
		if d.primary[target] == nil {
			d.primary[target] = &streak{healthy: true}
		}
		d.primary[target].observe(probe(ctx, peers[target].primaryIP, timeout), threshold)
		if ip := peers[target].backupIP; ip != "" {
			if d.backup[target] == nil {
				d.backup[target] = &streak{healthy: true}
			}
			d.backup[target].observe(probe(ctx, ip, timeout), threshold)
		}
		d.mu.Unlock()
	}
	return d.publish(ctx)
}

// publish writes the report only when it changed or the heartbeat is due (Part L7).
func (d *Daemon) publish(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	report := Report{Peers: make(map[string]PeerResult, len(d.primary))}
	for name, state := range d.primary {
		result := PeerResult{Primary: state.healthy}
		if b := d.backup[name]; b != nil {
			healthy := b.healthy
			result.Backup = &healthy
		}
		report.Peers[name] = result
	}
	content, err := json.Marshal(report.Peers)
	if err != nil {
		return err
	}
	if string(content) == d.lastSent && time.Since(d.lastTime) < HeartbeatSeconds*time.Second {
		return nil
	}

	report.Time = time.Now().Unix()
	value, err := json.Marshal(report)
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{ReportAnnotation: string(value)}},
	})
	if err != nil {
		return err
	}
	node := &corev1.Node{}
	node.Name = d.NodeName
	if err := d.Client.Patch(ctx, node, client.RawPatch("application/merge-patch+json", patch)); err != nil {
		return fmt.Errorf("publish report: %w", err)
	}
	d.lastSent, d.lastTime = string(content), time.Now()
	return nil
}

// probe needs both requests to work, so a path that passes small packets but drops
// full-size ones counts as broken. Keep-alives are off because every request must open
// a new connection: Calico lets established connections through ahead of later rules,
// so a reused connection keeps working on a path where new ones fail.
func probe(ctx context.Context, ip string, timeout time.Duration) bool {
	httpClient := &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}
	address := net.JoinHostPort(ip, strconv.Itoa(Port))
	for _, path := range []string{"/small", "/large"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, nil)
		if err != nil {
			return false
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return false
		}
		size, readErr := io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK || (path == "/large" && size != largeBodyBytes) {
			return false
		}
	}
	return true
}

// forgetMissingPeers drops the probe state of peers that have been missing for
// forgetPeerAfter. Without this a deleted node's last (failing) result was reported
// forever. A short absence, such as an agent restart, keeps the state.
func (d *Daemon) forgetMissingPeers(present []string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, name := range present {
		d.lastSeen[name] = now
	}
	for name := range d.primary {
		if seen, ok := d.lastSeen[name]; !ok || now.Sub(seen) > forgetPeerAfter {
			delete(d.primary, name)
			delete(d.backup, name)
			delete(d.lastSeen, name)
		}
	}
}

func ownedByDaemonSet(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.OwnerReferences, func(o metav1.OwnerReference) bool {
		return o.Kind == "DaemonSet" && o.Controller != nil && *o.Controller
	})
}

// backupIP returns the pod's net1 address from Multus' network-status annotation.
// The annotation can be written by anyone who can edit the pod, so when the backup
// address space is known an address outside it is ignored.
func backupIP(pod *corev1.Pod, backupNet *net.IPNet) string {
	var entries []struct {
		Interface string   `json:"interface"`
		IPs       []string `json:"ips"`
	}
	if err := json.Unmarshal([]byte(pod.Annotations[multusNetworkStatusAnnotation]), &entries); err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.Interface == "net1" && len(entry.IPs) > 0 {
			ip := net.ParseIP(entry.IPs[0])
			if ip == nil || (backupNet != nil && !backupNet.Contains(ip)) {
				return ""
			}
			return entry.IPs[0]
		}
	}
	return ""
}
