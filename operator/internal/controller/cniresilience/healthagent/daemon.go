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
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
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

// Daemon is the agent that runs on each node.
type Daemon struct {
	Client   client.Client
	NodeName string

	// Probe checks one peer address. Nil means the package's probe.
	Probe func(ctx context.Context, ip string, timeout time.Duration) bool

	mu       sync.Mutex
	primary  map[string]*streak
	backup   map[string]*streak
	lastSent string
	lastTime time.Time
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

// init allocates the streak maps.
func (d *Daemon) init() {
	d.primary = make(map[string]*streak)
	d.backup = make(map[string]*streak)
}

// seed starts every streak from this node's last fresh report, so a restart does not
// report a down peer as healthy again. With run 0, it still takes threshold opposite
// results to flip a seeded verdict.
func (d *Daemon) seed(ctx context.Context) {
	logger := logf.FromContext(ctx).WithName("health-agent")
	node := &corev1.Node{}
	if err := d.Client.Get(ctx, client.ObjectKey{Name: d.NodeName}, node); err != nil {
		logger.Error(err, "Reading last report failed", "node", d.NodeName)
		return
	}
	report, ok := ParseReport(node, time.Now())
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for peer, r := range report.Peers {
		d.primary[peer] = &streak{healthy: r.Primary}
		if r.Backup != nil {
			d.backup[peer] = &streak{healthy: *r.Backup}
		}
	}
}

// Run probes every peer each round until ctx ends.
func (d *Daemon) Run(ctx context.Context) {
	logger := logf.FromContext(ctx).WithName("health-agent")
	d.init()
	d.seed(ctx)

	interval, timeout, threshold := 2*time.Second, time.Second, 3
	for {
		var list remediationv1alpha1.CNIResilienceList
		if err := d.Client.List(ctx, &list); err == nil && len(list.Items) > 0 {
			spec := list.Items[0].Spec
			interval = time.Duration(spec.ProbeIntervalSeconds) * time.Second
			timeout = time.Duration(spec.ProbeTimeoutSeconds) * time.Second
			threshold = spec.FailureThreshold
		}

		if err := d.round(ctx, timeout, threshold); err != nil {
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

// round probes every other agent on both paths at once, then applies the results to
// the streaks and publishes. The lock is not held while probing.
func (d *Daemon) round(ctx context.Context, timeout time.Duration, threshold int) error {
	var pods corev1.PodList
	if err := d.Client.List(ctx, &pods, client.MatchingLabels{"app": agentLabel}); err != nil {
		return d.publish(ctx)
	}

	check := d.Probe
	if check == nil {
		check = probe
	}

	type peer struct{ primaryIP, backupIP string }
	type outcome struct {
		primary   bool
		backup    bool
		hasBackup bool
	}
	peers := make(map[string]peer)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != d.NodeName && pod.Status.PodIP != "" && pod.Status.Phase == corev1.PodRunning {
			peers[pod.Spec.NodeName] = peer{pod.Status.PodIP, backupIP(pod)}
		}
	}

	var (
		wg       sync.WaitGroup
		resultMu sync.Mutex
		results  = make(map[string]outcome, len(peers))
	)
	for name, p := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var o outcome
			var inner sync.WaitGroup
			if p.backupIP != "" {
				o.hasBackup = true
				inner.Add(1)
				go func() {
					defer inner.Done()
					o.backup = check(ctx, p.backupIP, timeout)
				}()
			}
			o.primary = check(ctx, p.primaryIP, timeout)
			inner.Wait()
			resultMu.Lock()
			results[name] = o
			resultMu.Unlock()
		}()
	}
	wg.Wait()

	d.mu.Lock()
	for name, o := range results {
		if d.primary[name] == nil {
			d.primary[name] = &streak{healthy: true}
		}
		d.primary[name].observe(o.primary, threshold)
		if o.hasBackup {
			if d.backup[name] == nil {
				d.backup[name] = &streak{healthy: true}
			}
			d.backup[name].observe(o.backup, threshold)
		}
	}
	d.mu.Unlock()
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

// probe needs both requests to work, and both share one timeout, so a path that passes small packets but drops
// full-size ones counts as broken. Keep-alives are off because every request must open
// a new connection: Calico lets established connections through ahead of later rules,
// so a reused connection keeps working on a path where new ones fail.
func probe(ctx context.Context, ip string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpClient := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
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

func backupIP(pod *corev1.Pod) string {
	var entries []struct {
		Interface string   `json:"interface"`
		IPs       []string `json:"ips"`
	}
	if err := json.Unmarshal([]byte(pod.Annotations[multusNetworkStatusAnnotation]), &entries); err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.Interface == "net1" && len(entry.IPs) > 0 {
			return entry.IPs[0]
		}
	}
	return ""
}
