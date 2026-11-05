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
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam/buddy"
)

// Agent keeps one node's routes, CNI config and NodeBlock status in line with
// the NodeBlock specs.
type Agent struct {
	// Client reads and writes NodeBlocks and lists pods. Writes and pod lists go
	// to the API server.
	Client client.Client
	// Reader, if set, serves the node and NodeBlock lists (a cache fed by
	// watches). nil means Client.
	Reader   client.Reader
	NodeName string
	Routes   RouteTable

	// ConfPath is the conflist the runtime reads, e.g. /etc/cni/net.d/10-buddy.conflist.
	ConfPath string
	// NetName is the CNI network name.
	NetName string
	// LeaseDir is host-local's directory for this network
	// (<dataDir>/<NetName>); DataDir is the dataDir written to the config.
	LeaseDir string
	DataDir  string

	// PromoteTimeout is how long a Pending block waits for route acks before it
	// is promoted anyway, so a dead node cannot block a grow.
	PromoteTimeout time.Duration
	// NAT runs the host's iptables to remove stale CNI masquerade rules. nil
	// disables the clean-up.
	NAT Runner
	// NATEvery is the least time between clean-up passes.
	NATEvery time.Duration
	lastNAT  time.Time

	// ReclaimLeaks removes host-local leases that no pod on this node accounts
	// for, once they are older than LeakMinAge.
	ReclaimLeaks bool
	LeakMinAge   time.Duration
	// DrainSettle is how long a block stays in the Draining phase before it can
	// be reported empty. It covers a pod sandbox that read the old CNI config
	// just before the drain.
	DrainSettle time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// releasing remembers, per block, which half is being given back and since
	// when the CNI config has left it out. Lost on restart, which only means
	// the settle time is waited again.
	releasing map[string]releaseMark
}

type releaseMark struct {
	half  string
	since time.Time
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Run calls Sync until ctx is cancelled: whenever trigger fires (a watch event;
// nil is fine) and at least every interval. Events that arrive close together
// are merged. A failed pass is logged and retried; the next pass starts from
// scratch, so the interval alone is enough to converge when watches break.
func (a *Agent) Run(ctx context.Context, interval time.Duration, trigger <-chan struct{}) {
	logger := logf.FromContext(ctx).WithName("ipam-agent")
	for {
		if err := a.Sync(ctx); err != nil {
			logger.Error(err, "sync failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			// Let a burst of events settle before reading state again.
			select {
			case <-ctx.Done():
				return
			case <-time.After(debounce):
			}
		case <-time.After(interval):
		}
	}
}

// debounce merges a burst of watch events into one pass.
const debounce = 300 * time.Millisecond

func (a *Agent) reader() client.Reader {
	if a.Reader != nil {
		return a.Reader
	}
	return a.Client
}

// Sync performs one level-triggered pass. Order matters: routes first, then the
// CNI config, then the Active phase. Otherwise a pod could be given an address
// that nobody can reach.
func (a *Agent) Sync(ctx context.Context) error {
	logger := logf.FromContext(ctx).WithName("ipam-agent")

	var nodeList corev1.NodeList
	if err := a.reader().List(ctx, &nodeList); err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	var blockList remediationv1alpha1.NodeBlockList
	if err := a.reader().List(ctx, &blockList); err != nil {
		return fmt.Errorf("list nodeblocks: %w", err)
	}
	nodes, blocks := nodeList.Items, blockList.Items

	var errs []error

	// 1. Routes to every other node's blocks. An owner with no InternalIP yet
	// is skipped and reported, but it does not hold up the other owners: its
	// blocks are simply not acknowledged until its route can be installed.
	routesOK := true
	if err := a.syncRoutes(nodes, blocks, logger); err != nil {
		routesOK = false
		errs = append(errs, err)
	}
	if skipped := UnroutableOwners(a.NodeName, nodes, blocks); len(skipped) > 0 {
		errs = append(errs, fmt.Errorf("incomplete routes: no InternalIP for %v", slices.Sorted(maps.Keys(skipped))))
	}

	// 2. Acknowledge them, only once they are really installed.
	if routesOK {
		for _, name := range AckableBlocks(a.NodeName, nodes, blocks) {
			if err := a.ack(ctx, name); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// 3. Leftover masquerade rules of an earlier CNI plugin.
	if err := a.cleanNAT(blocks); err != nil {
		errs = append(errs, err)
	}

	// 4. This node's own blocks.
	if err := a.syncOwn(ctx, nodes, blocks, routesOK); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (a *Agent) syncRoutes(nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock, logger logr.Logger) error {
	want, problems := DesiredRoutes(a.NodeName, nodes, blocks)
	for _, p := range problems {
		logger.Info("route skipped", "reason", p)
	}
	have, err := a.Routes.Managed()
	if err != nil {
		return fmt.Errorf("list routes: %w", err)
	}
	wantSet := map[Route]bool{}
	for _, r := range want {
		wantSet[r] = true
	}
	haveSet := map[Route]bool{}
	for _, r := range have {
		haveSet[r] = true
	}

	// New routes go in before stale ones come out. When a block is split or
	// buddies merge, the old and new prefixes overlap; the longest prefix wins,
	// so traffic never sees a gap.
	var errs []error
	for _, r := range want {
		if !haveSet[r] {
			if err := a.Routes.Replace(r); err != nil {
				errs = append(errs, fmt.Errorf("install route %s via %s: %w", r.Dst, r.Gateway, err))
				continue
			}
			logger.Info("route installed", "dst", r.Dst, "via", r.Gateway)
		}
	}
	for _, r := range have {
		if !wantSet[r] {
			if err := a.Routes.Delete(r); err != nil {
				errs = append(errs, fmt.Errorf("delete route %s: %w", r.Dst, err))
				continue
			}
			logger.Info("route removed", "dst", r.Dst, "via", r.Gateway)
		}
	}
	// Owners skipped for lack of an InternalIP are reported by Sync and only
	// keep their own blocks unacknowledged; they are not an install failure.
	return errors.Join(errs...)
}

// ack records that this node has the route for a block. Several agents update
// the same object, so it retries on conflict.
func (a *Agent) ack(ctx context.Context, name string) error {
	return a.updateStatus(ctx, name, func(b *remediationv1alpha1.NodeBlock) bool {
		if slices.Contains(b.Status.RoutesAckedBy, a.NodeName) {
			return false
		}
		b.Status.RoutesAckedBy = append(b.Status.RoutesAckedBy, a.NodeName)
		slices.Sort(b.Status.RoutesAckedBy)
		return true
	})
}

func (a *Agent) updateStatus(ctx context.Context, name string, mutate func(*remediationv1alpha1.NodeBlock) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var b remediationv1alpha1.NodeBlock
		if err := a.Client.Get(ctx, types.NamespacedName{Name: name}, &b); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !mutate(&b) {
			return nil
		}
		return a.Client.Status().Update(ctx, &b)
	})
}

func (a *Agent) syncOwn(ctx context.Context, nodes []corev1.Node, blocks []remediationv1alpha1.NodeBlock, routesOK bool) error {
	logger := logf.FromContext(ctx).WithName("ipam-agent")
	var own []*remediationv1alpha1.NodeBlock
	for i := range blocks {
		if blocks[i].Spec.NodeName == a.NodeName {
			own = append(own, &blocks[i])
		}
	}
	if len(own) == 0 {
		return nil
	}

	// Active blocks stay in the config, except those the controller asked to
	// drain. A Draining block comes back if the controller withdraws the request.
	// A Pending block joins only when the routes are in place everywhere that
	// matters.
	var inConfig, promote []*remediationv1alpha1.NodeBlock
	for _, b := range own {
		switch phaseOf(b) {
		case remediationv1alpha1.NodeBlockActive:
			if !b.Spec.Drain {
				inConfig = append(inConfig, b)
			}
		case remediationv1alpha1.NodeBlockDraining:
			if !b.Spec.Drain {
				inConfig = append(inConfig, b)
			}
		case remediationv1alpha1.NodeBlockPending:
			// A block created to be given back (the released half of a split)
			// never joins the config.
			if !b.Spec.Drain && routesOK && ReadyToPromote(a.NodeName, b, nodes, a.now(), a.PromoteTimeout) {
				inConfig = append(inConfig, b)
				promote = append(promote, b)
			}
		}
	}

	configOK := false
	if len(inConfig) > 0 {
		cidrs := make([]string, len(inConfig))
		for i, b := range inConfig {
			cidrs[i] = ServedCIDR(b)
		}
		conf, err := RenderCNIConfig(a.NetName, a.DataDir, cidrs)
		if err != nil {
			return err
		}
		changed, err := WriteFileIfChanged(a.ConfPath, conf)
		if err != nil {
			// Not Active unless the config is really on disk.
			return fmt.Errorf("write CNI config: %w", err)
		}
		if changed {
			ranges, _ := buddy.Aggregate(cidrs)
			logger.Info("CNI config written", "blocks", cidrs, "ranges", ranges)
		}
		configOK = true
	}

	all := make([]string, len(own))
	for i, b := range own {
		all[i] = b.Spec.CIDR
	}

	// Leaked leases are removed before counting, so usage is not inflated by them.
	reclaimed := map[string]int{}
	if a.ReclaimLeaks {
		var err error
		if reclaimed, err = a.reclaim(ctx, all); err != nil {
			logger.Error(err, "lease reclaim failed")
		}
	}

	// Usage comes from the lease files of every block this node owns.
	used, err := CountLeases(a.LeaseDir, all)
	if err != nil {
		return fmt.Errorf("count leases: %w", err)
	}

	// Usage per half, so the controller can see a half that could be given back.
	halfUsed, err := countHalves(a.LeaseDir, own)
	if err != nil {
		return err
	}
	ready := a.releaseReady(own, halfUsed, configOK)

	promoted := map[string]bool{}
	for _, b := range promote {
		promoted[b.Name] = true
	}
	var errs []error
	for _, b := range own {
		name, n := b.Name, used[b.Spec.CIDR]
		promoting := promoted[name]
		reclaimedHere := reclaimed[b.Spec.CIDR]
		hu := halfUsage(b.Spec.CIDR, halfUsed)
		rr := ready[name]
		readCIDR, readRelease := b.Spec.CIDR, b.Spec.Release
		if err := a.updateStatus(ctx, name, func(cur *remediationv1alpha1.NodeBlock) bool {
			changed := setHalfStatus(cur, readCIDR, readRelease, hu, rr)
			if ph := a.nextPhase(cur, promoting, n, configOK); ph != "" {
				now := metav1.NewTime(a.now())
				cur.Status.Phase = ph
				cur.Status.LastTransitionTime = &now
				changed = true
				logger.Info("block "+ph, "cidr", cur.Spec.CIDR)
			}
			if cur.Status.UsedAddresses != n {
				cur.Status.UsedAddresses = n
				changed = true
			}
			if reclaimedHere > 0 {
				cur.Status.ReclaimedLeases += reclaimedHere
				changed = true
			}
			return changed
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// countHalves counts the leases in each half of every block.
func countHalves(dir string, own []*remediationv1alpha1.NodeBlock) (map[string]int, error) {
	var halves []string
	for _, b := range own {
		if lo, hi, err := buddy.Halves(b.Spec.CIDR); err == nil {
			halves = append(halves, lo, hi)
		}
	}
	n, err := CountLeases(dir, halves)
	if err != nil {
		return nil, fmt.Errorf("count leases per half: %w", err)
	}
	return n, nil
}

// halfUsage is a block's [lower, upper] lease count, or nil if it has no halves.
func halfUsage(cidr string, halfUsed map[string]int) []int {
	lo, hi, err := buddy.Halves(cidr)
	if err != nil {
		return nil
	}
	return []int{halfUsed[lo], halfUsed[hi]}
}

// setHalfStatus records the per-half usage and release readiness, and reports
// whether it changed anything. The block may have been split since it was
// read: the numbers only apply to the CIDR and release they were computed for.
func setHalfStatus(cur *remediationv1alpha1.NodeBlock, readCIDR, readRelease string, halfUsage []int, ready string) bool {
	if cur.Spec.CIDR != readCIDR {
		return false
	}
	changed := false
	if !slices.Equal(cur.Status.HalfUsage, halfUsage) {
		cur.Status.HalfUsage = halfUsage
		changed = true
	}
	if cur.Spec.Release != readRelease {
		ready = ""
	}
	if cur.Status.ReleaseReady != ready {
		cur.Status.ReleaseReady = ready
		changed = true
	}
	return changed
}

// releaseReady reports, per block name, the half that can be given back: the
// CNI config has left it out for at least DrainSettle and it holds no lease.
// The settle time covers a pod sandbox that read the old config.
func (a *Agent) releaseReady(own []*remediationv1alpha1.NodeBlock, halfUsed map[string]int, configOK bool) map[string]string {
	if a.releasing == nil {
		a.releasing = map[string]releaseMark{}
	}
	out := map[string]string{}
	seen := map[string]bool{}
	for _, b := range own {
		half := b.Spec.Release
		if half == "" || b.Spec.Drain || phaseOf(b) != remediationv1alpha1.NodeBlockActive || ServedCIDR(b) == b.Spec.CIDR {
			continue
		}
		seen[b.Name] = true
		m, ok := a.releasing[b.Name]
		if !ok || m.half != half {
			if !configOK {
				continue // the config on disk may still hand out the half
			}
			m = releaseMark{half: half, since: a.now()}
			a.releasing[b.Name] = m
		}
		if halfUsed[half] == 0 && a.now().Sub(m.since) >= a.DrainSettle {
			out[b.Name] = half
		}
	}
	for name := range a.releasing {
		if !seen[name] {
			delete(a.releasing, name)
		}
	}
	return out
}

func transitionTime(b *remediationv1alpha1.NodeBlock) time.Time {
	if b.Status.LastTransitionTime != nil {
		return b.Status.LastTransitionTime.Time
	}
	return b.CreationTimestamp.Time
}

// reclaim removes the leases in cidrs that no pod on this node holds. A pod
// list that comes back empty is not trusted: every node runs at least
// kube-proxy, so an empty answer means the list failed, not that all leases
// are leaks.
func (a *Agent) reclaim(ctx context.Context, cidrs []string) (map[string]int, error) {
	var pods corev1.PodList
	if err := a.Client.List(ctx, &pods, client.MatchingFields{"spec.nodeName": a.NodeName}); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}
	return ReclaimLeases(a.LeaseDir, cidrs, PodIPs(pods.Items), a.LeakMinAge, a.now())
}

// nextPhase is the phase a block moves to, or "" to stay put. configOK says the
// CNI config on disk now matches the blocks that should be in it.
func (a *Agent) nextPhase(cur *remediationv1alpha1.NodeBlock, promoting bool, used int, configOK bool) string {
	switch phase := phaseOf(cur); {
	case promoting && phase == remediationv1alpha1.NodeBlockPending:
		return remediationv1alpha1.NodeBlockActive
	case phase == remediationv1alpha1.NodeBlockActive && cur.Spec.Drain && configOK:
		// The config no longer lists this block.
		return remediationv1alpha1.NodeBlockDraining
	case phase == remediationv1alpha1.NodeBlockDraining && !cur.Spec.Drain:
		return remediationv1alpha1.NodeBlockActive
	case phase == remediationv1alpha1.NodeBlockPending && cur.Spec.Drain && used == 0 &&
		a.now().Sub(transitionTime(cur)) >= a.DrainSettle:
		// Created to be given back (the released half of a split, when the
		// controller did not get to quarantine it itself): never in the config.
		return remediationv1alpha1.NodeBlockQuarantined
	case phase == remediationv1alpha1.NodeBlockDraining && used == 0 && configOK &&
		a.now().Sub(transitionTime(cur)) >= a.DrainSettle:
		// Empty and out of the config: routes can be withdrawn and the block held
		// back until the controller releases it.
		return remediationv1alpha1.NodeBlockQuarantined
	}
	return ""
}

// cleanNAT removes stale CNI masquerade rules for addresses in the blocks this
// node owns, at most once per NATEvery.
func (a *Agent) cleanNAT(blocks []remediationv1alpha1.NodeBlock) error {
	if a.NAT == nil || a.now().Sub(a.lastNAT) < a.NATEvery {
		return nil
	}
	var cidrs []string
	for i := range blocks {
		if blocks[i].Spec.NodeName == a.NodeName {
			cidrs = append(cidrs, blocks[i].Spec.CIDR)
		}
	}
	if len(cidrs) == 0 {
		return nil
	}
	a.lastNAT = a.now()
	n, err := CleanStaleNAT(a.NAT, cidrs)
	if n > 0 {
		logf.Log.WithName("ipam-agent").Info("removed stale CNI masquerade rules", "rules", n)
	}
	return err
}
