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

// Package ipam implements the IPAM controller for Problem 1.
//
// The controller owns the pools. On every reconcile it rebuilds the buddy trees
// from the NodeBlock objects (it keeps no private state), decides which blocks
// to hand out, and writes them as NodeBlock specs. It writes only spec; the
// node agent writes status and applies the blocks to the node's CNI config and
// routes. Pod startup never waits on this controller: it works ahead of demand.
package ipam

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam/buddy"
	"CS331-CN-Project-1/operator/internal/controller/ipam/monitor"
)

const (
	// requeueInterval is how often the controller re-reconciles when idle.
	requeueInterval = 30 * time.Second
	// requeueBusy is used while a block is Pending, or after a name collision.
	requeueBusy = 5 * time.Second
	// timerSlack is added to a timer's deadline so the next pass sees it expired.
	timerSlack = 500 * time.Millisecond
)

// Pool phases.
const (
	phaseReady     = "Ready"
	phaseExhausted = "Exhausted"
	phaseDegraded  = "Degraded"
	phaseInactive  = "Inactive"
)

// IPAMController reconciles IPPool resources and manages block allocations.
type IPAMController struct {
	client.Client
	Scheme *runtime.Scheme

	// Recorder emits Events. It may be nil (tests).
	Recorder record.EventRecorder

	// ConfigureKubeProxy makes the controller set kube-proxy's local-traffic
	// detection (see EnsureKubeProxyLocalMode). Off by default: it edits a
	// kube-system ConfigMap and restarts kube-proxy.
	ConfigureKubeProxy bool
	// APIReader reads kube-system objects directly from the API server.
	APIReader client.Reader

	kubeProxyDone atomic.Bool

	// monitor tracks per-node IP usage and detects exhaustion.
	monitor *monitor.Monitor
}

// NewIPAMController creates a new IPAM controller.
func NewIPAMController(c client.Client, scheme *runtime.Scheme) *IPAMController {
	return &IPAMController{
		Client:  c,
		Scheme:  scheme,
		monitor: monitor.NewMonitor(c),
	}
}

// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=ippools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=ippools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=nodeipallocations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=nodeipallocations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=nodeblocks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=remediation.cn-operator.yuvraj-rathod-1202.github.io,resources=nodeblocks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=configmaps,resourceNames=kube-proxy,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,resourceNames=kube-proxy,verbs=get;patch

// Reconcile rebuilds the pool from NodeBlocks, plans new blocks, persists them
// and updates the IPPool status. It is level-triggered and idempotent.
func (r *IPAMController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithName("ipam-controller")

	var ippool remediationv1alpha1.IPPool
	if err := r.Get(ctx, req.NamespacedName, &ippool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Nodes are served by one pool. With several IPPools the earliest created
	// wins; the others report Inactive instead of competing for nodes.
	primary, err := r.primaryPool(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if primary != ippool.Name {
		return ctrl.Result{}, r.updateStatus(ctx, &ippool, nil, nil, nil, phaseInactive,
			fmt.Sprintf("IPPool %s is serving the cluster's nodes", primary))
	}

	spec := withAPIDefaults(ippool.Spec)

	if r.ConfigureKubeProxy && r.APIReader != nil && !r.kubeProxyDone.Load() {
		if changed, err := EnsureKubeProxyLocalMode(ctx, r.APIReader, r.Client); err != nil {
			logger.Error(err, "Could not configure kube-proxy; will retry")
			r.event(&ippool, corev1.EventTypeWarning, "KubeProxyNotConfigured", err.Error())
		} else {
			r.kubeProxyDone.Store(true)
			if changed {
				logger.Info("Configured kube-proxy local-traffic detection and restarted it")
				r.event(&ippool, corev1.EventTypeNormal, "KubeProxyConfigured", "set detectLocalMode=InterfaceNamePrefix, clusterCIDR empty")
			}
		}
	}

	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return ctrl.Result{}, err
	}
	blocks, foreign, err := r.blocksFor(ctx, ippool.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	pods, waiting, err := r.nodePods(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	starved, err := r.starvedNodes(ctx, time.Now(), waiting)
	if err != nil {
		return ctrl.Result{}, err
	}
	lastChange := map[string]time.Time{}
	for _, na := range ippool.Status.NodeAllocations {
		if na.LastBlockChange != nil {
			lastChange[na.NodeName] = na.LastBlockChange.Time
		}
	}
	plan := planPool(&spec, nodes.Items, blocks, planInput{Now: time.Now(), Starved: starved, LastChange: lastChange, Waiting: waiting, Pods: pods, Foreign: foreign})
	touched := plan.changedNodes(blocks)

	collided := false
	for _, pb := range plan.New {
		created, err := r.createBlock(ctx, &ippool, pb)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !created {
			// Another writer (or a stale cache) already holds this block.
			// The next pass rebuilds the tree and picks another one.
			collided = true
			continue
		}
		logger.Info("Allocated block", "node", pb.NodeName, "cidr", pb.CIDR, "reason", pb.Reason)
		r.event(&ippool, corev1.EventTypeNormal, "BlockAllocated",
			fmt.Sprintf("%s block %s to node %s", pb.Reason, pb.CIDR, pb.NodeName))
	}

	changed, err := r.applyLifecycle(ctx, &ippool, plan)
	if err != nil {
		return ctrl.Result{}, err
	}

	phase, msg := phaseFor(plan)
	if plan.Exhausted {
		r.event(&ippool, corev1.EventTypeWarning, "PoolExhausted", msg)
	}

	// Re-list so the status reflects what was just created.
	if len(plan.New) > 0 || changed {
		if blocks, _, err = r.blocksFor(ctx, ippool.Name); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.updateStatus(ctx, &ippool, plan, blocks, touched, phase, msg); err != nil {
		if apierrors.IsConflict(err) {
			// The cache is a write behind our own previous status update.
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if collided || plan.Pending || changed {
		return ctrl.Result{RequeueAfter: requeueBusy}, nil
	}
	return ctrl.Result{RequeueAfter: requeueAfter(nextTimer(&spec, blocks, time.Now()))}, nil
}

func phaseFor(plan *poolPlan) (string, string) {
	switch {
	case plan.Exhausted:
		return phaseExhausted, joinProblems(plan.Problems)
	case len(plan.Problems) > 0:
		return phaseDegraded, joinProblems(plan.Problems)
	default:
		return phaseReady, ""
	}
}

// applyLifecycle carries out the drain, undrain, quarantine and delete steps of
// a plan. Draining is a spec change; withdrawing the block of a deleted node is
// a status change, because no agent is left to make it.
func (r *IPAMController) applyLifecycle(ctx context.Context, pool *remediationv1alpha1.IPPool, plan *poolPlan) (bool, error) {
	logger := logf.FromContext(ctx).WithName("ipam-controller")
	changed := false

	setDrain := func(name string, drain bool, reason, verb string) error {
		var nb remediationv1alpha1.NodeBlock
		if err := r.Get(ctx, types.NamespacedName{Name: name}, &nb); err != nil {
			return client.IgnoreNotFound(err)
		}
		if nb.Spec.Drain == drain {
			return nil
		}
		nb.Spec.Drain = drain
		if err := r.Update(ctx, &nb); err != nil {
			return client.IgnoreNotFound(err)
		}
		logger.Info(verb+" block", "block", nb.Spec.CIDR, "node", nb.Spec.NodeName)
		r.event(pool, corev1.EventTypeNormal, reason, fmt.Sprintf("%s block %s of node %s", verb, nb.Spec.CIDR, nb.Spec.NodeName))
		changed = true
		return nil
	}
	for _, n := range plan.Drain {
		if err := setDrain(n, true, "BlockDraining", "Draining"); err != nil {
			return changed, err
		}
	}
	for _, n := range plan.Undrain {
		if err := setDrain(n, false, "BlockRestored", "Restored"); err != nil {
			return changed, err
		}
	}

	setRelease := func(name, half string) error {
		var nb remediationv1alpha1.NodeBlock
		if err := r.Get(ctx, types.NamespacedName{Name: name}, &nb); err != nil {
			return client.IgnoreNotFound(err)
		}
		if nb.Spec.Release == half {
			return nil
		}
		was := nb.Spec.Release
		nb.Spec.Release = half
		if err := r.Update(ctx, &nb); err != nil {
			return client.IgnoreNotFound(err)
		}
		if half != "" {
			logger.Info("Releasing half of block", "block", nb.Spec.CIDR, "half", half, "node", nb.Spec.NodeName)
			r.event(pool, corev1.EventTypeNormal, "BlockHalfReleasing",
				fmt.Sprintf("Releasing half %s of block %s of node %s", half, nb.Spec.CIDR, nb.Spec.NodeName))
		} else {
			logger.Info("Kept block whole", "block", nb.Spec.CIDR, "half", was, "node", nb.Spec.NodeName)
			r.event(pool, corev1.EventTypeNormal, "BlockHalfRestored",
				fmt.Sprintf("Kept half %s of block %s of node %s", was, nb.Spec.CIDR, nb.Spec.NodeName))
		}
		changed = true
		return nil
	}
	for _, h := range plan.Release {
		if err := setRelease(h.Name, h.Half); err != nil {
			return changed, err
		}
	}
	for _, n := range plan.Unrelease {
		if err := setRelease(n, ""); err != nil {
			return changed, err
		}
	}
	for _, n := range plan.Split {
		done, err := r.splitBlock(ctx, pool, n)
		if err != nil {
			return changed, err
		}
		changed = changed || done
	}

	for _, n := range plan.Quarantine {
		var nb remediationv1alpha1.NodeBlock
		if err := r.Get(ctx, types.NamespacedName{Name: n}, &nb); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return changed, err
		}
		now := metav1.Now()
		nb.Status.Phase = remediationv1alpha1.NodeBlockQuarantined
		nb.Status.LastTransitionTime = &now
		if err := r.Status().Update(ctx, &nb); err != nil {
			return changed, client.IgnoreNotFound(err)
		}
		logger.Info("Quarantined block of a deleted node", "block", nb.Spec.CIDR, "node", nb.Spec.NodeName)
		r.event(pool, corev1.EventTypeNormal, "BlockQuarantined", fmt.Sprintf("block %s: node %s is gone", nb.Spec.CIDR, nb.Spec.NodeName))
		changed = true
	}

	for _, n := range plan.Delete {
		// Read the block first so the log and event name its CIDR, not its object name.
		var nb remediationv1alpha1.NodeBlock
		if err := r.Get(ctx, types.NamespacedName{Name: n}, &nb); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return changed, err
		}
		if err := r.Delete(ctx, &nb); client.IgnoreNotFound(err) != nil {
			return changed, err
		}
		logger.Info("Released block", "block", nb.Spec.CIDR, "node", nb.Spec.NodeName)
		r.event(pool, corev1.EventTypeNormal, "BlockReleased", fmt.Sprintf("block %s of node %s returned to the pool", nb.Spec.CIDR, nb.Spec.NodeName))
		changed = true
	}
	return changed, nil
}

// starveWindow is how long a "no IP addresses available" event keeps a node
// marked as starved.
const starveWindow = 3 * time.Minute

// starvedNodes returns, per node, when its kubelet last failed to create a pod
// sandbox because host-local had no address left, within starveWindow. That is
// demand the usage counter cannot see, so it forces a grow.
func (r *IPAMController) starvedNodes(ctx context.Context, now time.Time, waiting map[string]int) (map[string]time.Time, error) {
	var events corev1.EventList
	if err := r.List(ctx, &events); err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for i := range events.Items {
		e := &events.Items[i]
		if !isNoIPEvent(e) {
			continue
		}
		t := e.LastTimestamp.Time
		if t.IsZero() {
			t = e.EventTime.Time
		}
		if t.IsZero() {
			t = e.CreationTimestamp.Time
		}
		if now.Sub(t) > starveWindow {
			continue
		}
		node := e.Source.Host
		if node == "" {
			node = e.ReportingInstance
		}
		if node != "" && t.After(out[node]) {
			out[node] = t
		}
	}
	// A failure only matters while a pod on that node is still waiting for an
	// address. Seen live: after a full pool was freed, the waiting pods were
	// deleted, but their recent failures made the controller grab every block
	// that came back, only to drain it again.
	for node := range out {
		if waiting[node] == 0 {
			delete(out, node)
		}
	}
	return out, nil
}

// nodePods counts, per node, the pods that hold or need an address (not host
// network, not finished, not being deleted) and, of those, the ones still
// Pending without an IP: waiting for an address. The controller sizes a grow to
// this demand, so a node gets what its pods need and no more.
func (r *IPAMController) nodePods(ctx context.Context) (pods, waiting map[string]int, err error) {
	var list corev1.PodList
	if err := r.List(ctx, &list); err != nil {
		return nil, nil, err
	}
	pods, waiting = map[string]int{}, map[string]int{}
	for i := range list.Items {
		p := &list.Items[i]
		if p.Spec.NodeName == "" || p.Spec.HostNetwork || !p.DeletionTimestamp.IsZero() ||
			p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		pods[p.Spec.NodeName]++
		if p.Status.Phase == corev1.PodPending && p.Status.PodIP == "" {
			waiting[p.Spec.NodeName]++
		}
	}
	return pods, waiting, nil
}

// sandboxFailedReason is the kubelet event reason for a pod sandbox that could
// not be set up; with "no IP addresses available" it means host-local is full.
const sandboxFailedReason = "FailedCreatePodSandBox"

func isNoIPEvent(e *corev1.Event) bool {
	return e.Reason == sandboxFailedReason && strings.Contains(e.Message, "no IP addresses available")
}

// primaryPool returns the name of the IPPool that serves the nodes: the
// earliest created, with the name as tie-break.
func (r *IPAMController) primaryPool(ctx context.Context) (string, error) {
	var pools remediationv1alpha1.IPPoolList
	if err := r.List(ctx, &pools); err != nil {
		return "", err
	}
	items := pools.Items
	slices.SortFunc(items, func(a, b remediationv1alpha1.IPPool) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(items) == 0 {
		return "", nil
	}
	return items[0].Name, nil
}

// blocksFor splits all NodeBlocks into the ones this pool manages and the
// foreign ones (another pool's, or a pool's that no longer exists).
func (r *IPAMController) blocksFor(ctx context.Context, pool string) (mine, foreign []remediationv1alpha1.NodeBlock, err error) {
	var list remediationv1alpha1.NodeBlockList
	if err := r.List(ctx, &list); err != nil {
		return nil, nil, err
	}
	mine = make([]remediationv1alpha1.NodeBlock, 0, len(list.Items))
	for _, b := range list.Items {
		if b.Spec.PoolRef == pool {
			mine = append(mine, b)
		} else {
			foreign = append(foreign, b)
		}
	}
	return mine, foreign, nil
}

// splitBlock completes a release: the block shrinks to the half it keeps, and
// the released half becomes its own Quarantined block, so it rests like any
// drained block before going back to the pool.
//
// Order matters. The block shrinks first, in one update, so the half is never
// owned twice. Before this, the owner's CNI config already left the half out
// and the agent saw it empty for the settle time, so a crash before the second
// step is harmless: the half is simply free again, without its rest. Other
// nodes replace their route for the whole block by one for the kept half; the
// agent installs the new route before removing the old one.
func (r *IPAMController) splitBlock(ctx context.Context, pool *remediationv1alpha1.IPPool, name string) (bool, error) {
	logger := logf.FromContext(ctx).WithName("ipam-controller")
	var nb remediationv1alpha1.NodeBlock
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &nb); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	half := nb.Spec.Release
	if half == "" || nb.Status.ReleaseReady != half {
		return false, nil
	}
	whole := nb.Spec.CIDR
	keep, err := buddy.OtherHalf(whole, half)
	if err != nil {
		// Not a half of this block: drop the request.
		nb.Spec.Release = ""
		return true, client.IgnoreNotFound(r.Update(ctx, &nb))
	}

	nb.Spec.CIDR = keep
	nb.Spec.Release = ""
	if err := r.Update(ctx, &nb); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	now := metav1.Now()
	nb.Status.HalfUsage = nil
	nb.Status.ReleaseReady = ""
	nb.Status.LastTransitionTime = &now
	if err := r.Status().Update(ctx, &nb); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return true, err
	}

	rest := &remediationv1alpha1.NodeBlock{
		ObjectMeta: metav1.ObjectMeta{Name: blockName(half)},
		Spec: remediationv1alpha1.NodeBlockSpec{
			NodeName: nb.Spec.NodeName,
			PoolRef:  pool.Name,
			CIDR:     half,
			Drain:    true,
		},
	}
	if err := r.Create(ctx, rest); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return true, err
		}
	} else {
		// Should this write fail, the agent moves the block to Quarantined itself.
		rest.Status.Phase = remediationv1alpha1.NodeBlockQuarantined
		rest.Status.LastTransitionTime = &now
		if err := r.Status().Update(ctx, rest); err != nil && !apierrors.IsNotFound(err) {
			return true, err
		}
	}
	logger.Info("Split block", "block", whole, "kept", keep, "released", half, "node", nb.Spec.NodeName)
	r.event(pool, corev1.EventTypeNormal, "BlockSplit",
		fmt.Sprintf("Split block %s of node %s: kept %s, %s goes back to the pool", whole, nb.Spec.NodeName, keep, half))
	return true, nil
}

// createBlock persists a planned block as a Pending NodeBlock. It returns false
// without error when the name is already taken, which is exactly how double
// allocation is prevented.
func (r *IPAMController) createBlock(ctx context.Context, pool *remediationv1alpha1.IPPool, pb plannedBlock) (bool, error) {
	nb := &remediationv1alpha1.NodeBlock{
		ObjectMeta: metav1.ObjectMeta{Name: blockName(pb.CIDR)},
		Spec: remediationv1alpha1.NodeBlockSpec{
			NodeName: pb.NodeName,
			PoolRef:  pool.Name,
			CIDR:     pb.CIDR,
			Initial:  pb.Reason == reasonInitial,
		},
	}
	// No owner reference on purpose: deleting an IPPool must not garbage-collect
	// blocks that pods are still using.
	if err := r.Create(ctx, nb); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	// A crash before this write is harmless: an empty phase counts as Pending.
	now := metav1.Now()
	nb.Status.Phase = remediationv1alpha1.NodeBlockPending
	nb.Status.LastTransitionTime = &now
	if err := r.Status().Update(ctx, nb); err != nil {
		return true, client.IgnoreNotFound(err)
	}
	return true, nil
}

func (r *IPAMController) updateStatus(ctx context.Context, pool *remediationv1alpha1.IPPool, plan *poolPlan,
	blocks []remediationv1alpha1.NodeBlock, touched map[string]bool, phase, msg string) error {
	st := remediationv1alpha1.IPPoolStatus{Phase: phase, Message: msg}
	now := metav1.Now()
	st.LastReconcileTime = &now

	if plan != nil {
		stats := plan.Pool.PoolStats()
		st.TotalAddresses = int64(stats.TotalAddresses)
		st.AllocatedAddresses = int64(stats.AllocatedAddresses)
		st.FreeAddresses = int64(stats.FreeAddresses)
		st.NodeAllocations = summarise(blocks)
		st.CIDRs = plan.CIDRs
	}
	stampChanges(st.NodeAllocations, pool.Status.NodeAllocations, touched, now)
	// An unchanged status is not rewritten, except for a heartbeat: the timestamp
	// is refreshed every heartbeatEvery so a dashboard can tell a running
	// controller from a stopped one.
	if equalStatus(pool.Status, st) && !heartbeatDue(pool.Status.LastReconcileTime, now.Time) {
		return nil
	}
	pool.Status = st
	err := r.Status().Update(ctx, pool)
	if !apierrors.IsConflict(err) || len(touched) == 0 {
		return err
	}
	// The change times must not be lost: without them the failures this pass
	// answered would count again on the next one. Retry on the latest object.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh remediationv1alpha1.IPPool
		if err := r.Get(ctx, types.NamespacedName{Name: pool.Name}, &fresh); err != nil {
			return err
		}
		stampChanges(st.NodeAllocations, fresh.Status.NodeAllocations, touched, now)
		fresh.Status = st
		if err := r.Status().Update(ctx, &fresh); err != nil {
			return err
		}
		*pool = fresh
		return nil
	})
}

// stampChanges carries each node's LastBlockChange over from the previous
// status, and sets it to now for the nodes this pass changed.
func stampChanges(cur, prev []remediationv1alpha1.NodeAllocationSummary, touched map[string]bool, now metav1.Time) {
	last := map[string]*metav1.Time{}
	for i := range prev {
		last[prev[i].NodeName] = prev[i].LastBlockChange
	}
	for i := range cur {
		switch {
		case touched[cur[i].NodeName]:
			t := now
			cur[i].LastBlockChange = &t
		case last[cur[i].NodeName] != nil:
			t := *last[cur[i].NodeName]
			cur[i].LastBlockChange = &t
		}
	}
}

// heartbeatEvery is how often an otherwise unchanged pool status is rewritten.
const heartbeatEvery = 30 * time.Second

func heartbeatDue(last *metav1.Time, now time.Time) bool {
	return last == nil || now.Sub(last.Time) >= heartbeatEvery
}

// equalStatus ignores the timestamp so an idle pool does not write every pass.
func equalStatus(a, b remediationv1alpha1.IPPoolStatus) bool {
	a.LastReconcileTime, b.LastReconcileTime = nil, nil
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func summarise(blocks []remediationv1alpha1.NodeBlock) []remediationv1alpha1.NodeAllocationSummary {
	byNode := map[string]*remediationv1alpha1.NodeAllocationSummary{}
	active := map[string][]string{}
	for i := range blocks {
		b := &blocks[i]
		if blockPhase(b) == remediationv1alpha1.NodeBlockReleased {
			continue
		}
		s := byNode[b.Spec.NodeName]
		if s == nil {
			s = &remediationv1alpha1.NodeAllocationSummary{NodeName: b.Spec.NodeName}
			byNode[b.Spec.NodeName] = s
		}
		s.Blocks = append(s.Blocks, b.Spec.CIDR)
		s.AllocatedIPs += b.Status.UsedAddresses
		if blockPhase(b) == remediationv1alpha1.NodeBlockActive && !b.Spec.Drain {
			active[b.Spec.NodeName] = append(active[b.Spec.NodeName], b.Spec.CIDR)
		}
	}
	out := make([]remediationv1alpha1.NodeAllocationSummary, 0, len(byNode))
	for name, s := range byNode {
		slices.Sort(s.Blocks)
		s.TotalCapacity = capacityOf(active[name])
		if s.TotalCapacity > 0 {
			s.UtilizationPercent = s.AllocatedIPs * 100 / s.TotalCapacity
		}
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b remediationv1alpha1.NodeAllocationSummary) int {
		return strings.Compare(a.NodeName, b.NodeName)
	})
	return out
}

func (r *IPAMController) event(obj client.Object, typ, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(obj, typ, reason, msg)
	}
}

// newEventRecorder returns a recorder whose rate limits fit this controller. The
// default recorder lets an object emit a burst of 25 events and then about one
// every five minutes, and merges similar events into one "(combined from similar
// events)" object. A pool that fills up makes dozens of block decisions in a
// minute, so with the defaults most of them never reach the API, and they are
// what people read to see what the controller did.
func newEventRecorder(mgr ctrl.Manager) (record.EventRecorder, error) {
	cs, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}
	b := record.NewBroadcasterWithCorrelatorOptions(record.CorrelatorOptions{BurstSize: 1000, QPS: 10, MaxEvents: 1000})
	b.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: cs.CoreV1().Events("")})
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-ctx.Done()
		b.Shutdown()
		return nil
	})); err != nil {
		return nil, err
	}
	return b.NewRecorder(mgr.GetScheme(), corev1.EventSource{Component: "ipam-controller"}), nil
}

// SetupWithManager sets up the controller with the Manager. Node and NodeBlock
// changes both trigger a reconcile of the pool they belong to.
func (r *IPAMController) SetupWithManager(mgr ctrl.Manager) error {
	rec, err := newEventRecorder(mgr)
	if err != nil {
		return err
	}
	r.Recorder = rec
	r.APIReader = mgr.GetAPIReader()

	allPools := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var pools remediationv1alpha1.IPPoolList
		if err := r.List(ctx, &pools); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(pools.Items))
		for _, p := range pools.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: p.Name}})
		}
		return reqs
	})
	ownerPool := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		nb, ok := o.(*remediationv1alpha1.NodeBlock)
		if !ok || nb.Spec.PoolRef == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nb.Spec.PoolRef}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1alpha1.IPPool{}).
		Watches(&corev1.Node{}, allPools).
		Watches(&remediationv1alpha1.NodeBlock{}, ownerPool).
		Watches(&corev1.Event{}, allPools, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			e, ok := o.(*corev1.Event)
			return ok && isNoIPEvent(e)
		}))).
		Named("ipam").
		Complete(r)
}

// requeueAfter wakes the controller when the next timer runs out (see
// nextTimer), and otherwise at the idle interval.
func requeueAfter(timer time.Duration) time.Duration {
	if timer > 0 && timer+timerSlack < requeueInterval {
		return timer + timerSlack
	}
	return requeueInterval
}
