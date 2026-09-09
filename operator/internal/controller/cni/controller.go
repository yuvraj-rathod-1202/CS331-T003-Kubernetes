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

// Package cni implements the CNI health monitoring and remediation module.
//
// It monitors:
//   - Calico-node DaemonSet readiness (detected crash / eviction of CNI pods)
//   - Pods stuck in ContainerCreating with a CNI-error event (FailedCreatePodSandBox)
//   - IP pool utilisation via Calico IPAMBlock CRDs
//
// When failures are found it:
//   - Deletes unready calico-node pods so the DaemonSet controller recreates them.
//   - Deletes workload pods that are stuck in ContainerCreating due to CNI errors
//     so they can be rescheduled once the CNI recovers.
//   - Emits a warning log when IP-pool usage crosses the configured threshold.
package cni

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/pkg/module"
)

// Signal keys stored in CheckResult.Signals.
const (
	signalCalicoNodeUnready    = "calicoNodeUnready"    // []string  - names of unready calico-node pods
	signalStuckPods            = "stuckPods"            // []StuckPod
	signalIPAMUsageByPool      = "ipamUsageByPool"      // map[string]float64 - block CIDR → usage %
	signalIPAMExhaustedPools   = "ipamExhaustedPools"   // []string - block CIDRs at 100 %
	signalDisabledIPPools      = "disabledIPPools"      // []string - names of disabled Calico IPPools
	signalIPAMThresholdPercent = "ipamThresholdPercent" // int - configured IPAM usage threshold %

	kubeSystemNamespace = "kube-system"
	calicoCRDGroup      = "crd.projectcalico.org"
	appsGroup           = "apps"
	kindDaemonSet       = "DaemonSet"
	severityCritical    = "critical"
	severityWarning     = "warning"
	severityNone        = "none"

	defaultIPAMThresholdPercent = 80

	defaultCalicoDaemonSetName = "calico-node"
	k8sAppLabel                = "k8s-app"
	calicoIPPoolKind           = "IPPool"
)

// RemediationActionType specifies the targeted remediation branch.
type RemediationActionType string

const (
	// ActionNone indicates no remediation action is needed.
	ActionNone RemediationActionType = "none"
	// ActionRestartCalicoNode restarts unready calico-node pods.
	ActionRestartCalicoNode RemediationActionType = "restart_calico_node"
	// ActionReenableIPPool re-enables disabled Calico IPPools.
	ActionReenableIPPool RemediationActionType = "reenable_ippool"
	// ActionEvictStuckPods evicts workload pods stuck with CNI/IPAM errors.
	ActionEvictStuckPods RemediationActionType = "evict_stuck_pods"
)

type remediationTarget struct {
	actionType    RemediationActionType
	unreadyPods   []string
	disabledPools []string
	stuckPods     []StuckPod
}

// StuckPod records a pod that is stuck in ContainerCreating with a CNI error.
type StuckPod struct {
	Namespace     string
	Name          string
	Node          string
	CNIError      string // short message from the FailedCreatePodSandBox event
	IPAMExhausted bool   // true when the error indicates IPAM pool exhaustion
	WorkloadKey   string // owner workload e.g. "namespace/Kind/name" to prevent churn
}

// CNIModule monitors CNI plugin health and remediates detected failures.
type CNIModule struct {
	Client               client.Client
	pendingTarget        *remediationTarget
	ipamThresholdPercent int
	evictionCooldown     time.Duration
	lastWorkloadEviction map[string]time.Time
}

// New returns a configured CNIModule.
func New(c client.Client) *CNIModule {
	return &CNIModule{
		Client:               c,
		evictionCooldown:     3 * time.Minute,
		lastWorkloadEviction: make(map[string]time.Time),
	}
}

// Name satisfies module.Module.
func (m *CNIModule) Name() string { return "cni" }

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=crd.projectcalico.org,resources=ippools,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=crd.projectcalico.org,resources=ipamblocks,verbs=get;list;watch

// Check collects raw health signals from the Kubernetes API and stores them in
// CheckResult.Signals. It does NOT make any pass/fail decision - that is left
// to Evaluate.
func (m *CNIModule) Check(
	ctx context.Context,
	spec *remediationv1alpha1.NetworkRemediationSpec,
) (*module.CheckResult, error) {
	logger := log.FromContext(ctx).WithName("cni-check")

	signals := map[string]any{}

	calicoNS := kubeSystemNamespace
	calicoDSName := defaultCalicoDaemonSetName
	if spec.CNI.CalicoNamespace != "" {
		calicoNS = spec.CNI.CalicoNamespace
	}
	if spec.CNI.CalicoDaemonSetName != "" {
		calicoDSName = spec.CNI.CalicoDaemonSetName
	}
	stuckThreshold := 60
	if spec.CNI.StuckPodThresholdSeconds > 0 {
		stuckThreshold = spec.CNI.StuckPodThresholdSeconds
	}
	ipamThreshold := defaultIPAMThresholdPercent
	if spec.CNI.IPAMUsageThresholdPercent > 0 {
		ipamThreshold = spec.CNI.IPAMUsageThresholdPercent
	}
	m.ipamThresholdPercent = ipamThreshold
	signals[signalIPAMThresholdPercent] = ipamThreshold

	cooldown := 3 * time.Minute
	if spec.CNI.EvictionCooldownSeconds > 0 {
		cooldown = time.Duration(spec.CNI.EvictionCooldownSeconds) * time.Second
	}
	m.evictionCooldown = cooldown
	if m.lastWorkloadEviction == nil {
		m.lastWorkloadEviction = make(map[string]time.Time)
	}

	unreadyPods, err := m.checkCalicoDaemonSet(ctx, calicoNS, calicoDSName)
	if err != nil {
		return nil, fmt.Errorf("check calico DaemonSet %s/%s: %w", calicoNS, calicoDSName, err)
	}
	signals[signalCalicoNodeUnready] = unreadyPods

	stuckPods, err := m.checkStuckPods(ctx, time.Duration(stuckThreshold)*time.Second)
	if err != nil {
		logger.Error(err, "failed to check stuck pods")
		stuckPods = []StuckPod{}
	}
	signals[signalStuckPods] = stuckPods

	usageByPool, exhausted, disabledPools, err := m.checkIPAMUsage(ctx)
	if err != nil {
		logger.Error(err, "failed to query Calico IPAM resources")
		usageByPool = map[string]float64{}
		exhausted = []string{}
		disabledPools = []string{}
	}
	signals[signalIPAMUsageByPool] = usageByPool
	signals[signalIPAMExhaustedPools] = exhausted
	signals[signalDisabledIPPools] = disabledPools

	return &module.CheckResult{Signals: signals}, nil
}

// checkCalicoDaemonSet returns the names of calico-node pods that are Not Ready.
func (m *CNIModule) checkCalicoDaemonSet(ctx context.Context, ns, dsName string) ([]string, error) {
	ds := &unstructured.Unstructured{}
	ds.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   appsGroup,
		Version: "v1",
		Kind:    kindDaemonSet,
	})
	if err := m.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: dsName}, ds); err != nil {
		return nil, fmt.Errorf("get DaemonSet %s/%s: %w", ns, dsName, err)
	}

	desired, foundDesired, errDesired := unstructured.NestedInt64(ds.Object, "status", "desiredNumberScheduled")
	ready, foundReady, errReady := unstructured.NestedInt64(ds.Object, "status", "numberReady")
	if errDesired != nil {
		return nil, fmt.Errorf("parse DaemonSet %s/%s status.desiredNumberScheduled: %w", ns, dsName, errDesired)
	}
	if errReady != nil {
		return nil, fmt.Errorf("parse DaemonSet %s/%s status.numberReady: %w", ns, dsName, errReady)
	}

	if foundDesired && foundReady && desired > 0 && desired == ready {
		return []string{}, nil // all nodes covered and ready
	}

	listOpts := []client.ListOption{client.InNamespace(ns)}
	matchLabels, foundSelector, _ := unstructured.NestedStringMap(ds.Object, "spec", "selector", "matchLabels")
	if foundSelector && len(matchLabels) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(matchLabels))
	} else {
		listOpts = append(listOpts, client.MatchingLabels{k8sAppLabel: dsName})
	}

	podList := &corev1.PodList{}
	if err := m.Client.List(ctx, podList, listOpts...); err != nil {
		return nil, fmt.Errorf("list calico-node pods: %w", err)
	}

	var unready []string
	for _, pod := range podList.Items {
		if !isPodReady(&pod) {
			unready = append(unready, pod.Name)
		}
	}
	return unready, nil
}

// isPodReady returns true when the pod has the Ready condition set to True.
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// checkStuckPods lists cluster-wide pods in ContainerCreating (no IP) that
// have been waiting longer than threshold and have a CNI-related event.
func (m *CNIModule) checkStuckPods(ctx context.Context, threshold time.Duration) ([]StuckPod, error) {
	podList := &corev1.PodList{}
	if err := m.Client.List(ctx, podList); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	now := time.Now()
	var stuck []StuckPod

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodPending {
			continue
		}
		if pod.Status.PodIP != "" {
			continue
		}

		inContainerCreating := false
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" {
				inContainerCreating = true
				break
			}
		}
		// Also catch pods that have no ContainerStatus yet (sandbox not started).
		if !inContainerCreating && len(pod.Status.ContainerStatuses) == 0 {
			inContainerCreating = true
		}
		if !inContainerCreating {
			continue
		}

		if threshold > 0 && now.Sub(pod.CreationTimestamp.Time) < threshold {
			continue
		}

		cniErr, ipamExhausted := m.findCNIEvent(ctx, pod.Namespace, pod.Name)
		if cniErr == "" {
			continue // stuck but not due to CNI
		}

		stuck = append(stuck, StuckPod{
			Namespace:     pod.Namespace,
			Name:          pod.Name,
			Node:          pod.Spec.NodeName,
			CNIError:      cniErr,
			IPAMExhausted: ipamExhausted,
			WorkloadKey:   getWorkloadKey(&pod),
		})
	}

	return stuck, nil
}

// getWorkloadKey extracts the owner workload key (e.g. "default/ReplicaSet/backend-65d68")
// to group pods belonging to the same controller and prevent repeated eviction churn.
func getWorkloadKey(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != "" && ref.Name != "" {
			return fmt.Sprintf("%s/%s/%s", pod.Namespace, ref.Kind, ref.Name)
		}
	}
	if app, ok := pod.Labels["app"]; ok && app != "" {
		return fmt.Sprintf("%s/App/%s", pod.Namespace, app)
	}
	return fmt.Sprintf("%s/Pod/%s", pod.Namespace, pod.Name)
}

// findCNIEvent checks whether there is a FailedCreatePodSandBox warning event
// for the given pod and returns the error message and whether it indicates
// IPAM exhaustion. Empty string means no CNI event was found.
func (m *CNIModule) findCNIEvent(ctx context.Context, ns, podName string) (errMsg string, ipamExhausted bool) {
	evList := &corev1.EventList{}
	if err := m.Client.List(ctx, evList, client.InNamespace(ns)); err != nil {
		return "", false
	}

	for _, ev := range evList.Items {
		if ev.InvolvedObject.Name != podName || ev.InvolvedObject.Namespace != ns {
			continue
		}
		if ev.Reason != "FailedCreatePodSandBox" {
			continue
		}
		msg := ev.Message
		msgLower := strings.ToLower(msg)
		if strings.Contains(msgLower, "failed to setup network for sandbox") ||
			strings.Contains(msgLower, "no ips available in pools") ||
			strings.Contains(msgLower, "no ip addresses available") ||
			strings.Contains(msgLower, "failed to allocate") ||
			strings.Contains(msgLower, "failed to request ipv4 addresses") ||
			strings.Contains(msgLower, "plugin type=\"calico\"") ||
			strings.Contains(msgLower, "calico") ||
			strings.Contains(msgLower, "cni plugin") {

			ipamEx := strings.Contains(msgLower, "no ips available") ||
				strings.Contains(msgLower, "no ip addresses available") ||
				strings.Contains(msgLower, "assigned 0 out of") ||
				strings.Contains(msgLower, "failed to allocate")

			if len(msg) > 200 {
				msg = msg[:200] + "…"
			}
			return msg, ipamEx
		}
	}
	return "", false
}

// checkIPAMUsage reads Calico IPAMBlock and IPPool CRDs to compute per-block utilisation
// and discover disabled pools.
// Returns (usageByBlock, exhaustedBlocks, disabledPools, error).
func (m *CNIModule) checkIPAMUsage(ctx context.Context) (map[string]float64, []string, []string, error) {
	// 1. Check IPPools for disabled status
	poolList := &unstructured.UnstructuredList{}
	poolList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   calicoCRDGroup,
		Version: "v1",
		Kind:    "IPPoolList",
	})
	var disabledPools []string
	if err := m.Client.List(ctx, poolList); err == nil {
		for _, item := range poolList.Items {
			disabled, _, _ := unstructured.NestedBool(item.Object, "spec", "disabled")
			if disabled {
				disabledPools = append(disabledPools, item.GetName())
			}
		}
	}

	// 2. Check IPAMBlocks for capacity and usage
	blockList := &unstructured.UnstructuredList{}
	blockList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   calicoCRDGroup,
		Version: "v1",
		Kind:    "IPAMBlockList",
	})
	if err := m.Client.List(ctx, blockList); err != nil {
		return nil, nil, disabledPools, fmt.Errorf("list IPAMBlocks: %w", err)
	}

	usageByPool := make(map[string]float64)
	var exhausted []string

	for _, item := range blockList.Items {
		cidr, _, _ := unstructured.NestedString(item.Object, "spec", "cidr")
		if cidr == "" {
			continue
		}
		allocations, _, _ := unstructured.NestedSlice(item.Object, "spec", "allocations")
		unallocated, _, _ := unstructured.NestedSlice(item.Object, "spec", "unallocated")

		total := len(allocations)
		if total == 0 {
			continue
		}

		free := min(len(unallocated), total)
		used := total - free

		pct := float64(used) / float64(total) * 100
		usageByPool[cidr] = pct
		if used == total {
			exhausted = append(exhausted, cidr)
		}
	}
	return usageByPool, exhausted, disabledPools, nil
}

// Evaluate analyses the signals collected by Check and decides whether the CNI
// plugin is healthy and whether remediation is required.
func (m *CNIModule) Evaluate(
	ctx context.Context,
	checkResult *module.CheckResult,
) (*module.EvalResult, error) {
	logger := log.FromContext(ctx).WithName("cni-evaluate")

	if checkResult == nil {
		m.pendingTarget = nil
		return &module.EvalResult{IsHealthy: true, NeedsRemediation: false, Reason: "no check result"}, nil
	}

	unreadyPods := signalStringSlice(checkResult, signalCalicoNodeUnready)
	stuckPods := signalStuckPodSlice(checkResult)
	exhaustedPools := signalStringSlice(checkResult, signalIPAMExhaustedPools)
	disabledPools := signalStringSlice(checkResult, signalDisabledIPPools)
	usageByPool, _ := checkResult.Signals[signalIPAMUsageByPool].(map[string]float64)
	ipamThreshold := signalInt(checkResult, signalIPAMThresholdPercent, m.ipamThresholdPercent)
	if ipamThreshold <= 0 {
		ipamThreshold = defaultIPAMThresholdPercent
	}

	// ---- calico-node crash (highest severity) ----
	if len(unreadyPods) > 0 {
		reason := fmt.Sprintf(
			"%d calico-node pod(s) not ready: %s - CNI may be unavailable on those nodes",
			len(unreadyPods), strings.Join(unreadyPods, ", "),
		)
		logger.Info("CNI unhealthy: calico-node pods not ready", "pods", unreadyPods)
		m.pendingTarget = &remediationTarget{
			actionType:  ActionRestartCalicoNode,
			unreadyPods: unreadyPods,
		}
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: true,
			Reason:           reason,
			Severity:         severityCritical,
		}, nil
	}

	// ---- disabled IPPools causing IP allocation failure ----
	if len(disabledPools) > 0 && (len(stuckPods) > 0) {
		reason := fmt.Sprintf(
			"Calico IPPool(s) disabled [%s] while %d pod(s) are failing to acquire IPs",
			strings.Join(disabledPools, ", "), countIPAMStuck(stuckPods),
		)
		logger.Info("CNI unhealthy: disabled IPPools causing pod creation failure", "disabledPools", disabledPools)
		m.pendingTarget = &remediationTarget{
			actionType:    ActionReenableIPPool,
			disabledPools: disabledPools,
			stuckPods:     stuckPods,
		}
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: true,
			Reason:           reason,
			Severity:         severityCritical,
		}, nil
	}

	// Segregate stuck pods into actionable vs throttled (in cooldown)
	if m.lastWorkloadEviction == nil {
		m.lastWorkloadEviction = make(map[string]time.Time)
	}
	if m.evictionCooldown == 0 {
		m.evictionCooldown = 3 * time.Minute
	}

	var actionableStuckPods []StuckPod
	var throttledStuckPods []StuckPod
	throttledWorkloadsMap := make(map[string]bool)

	for _, sp := range stuckPods {
		key := sp.WorkloadKey
		if key == "" {
			key = fmt.Sprintf("%s/Pod/%s", sp.Namespace, sp.Name)
		}
		if lastTime, ok := m.lastWorkloadEviction[key]; ok {
			if time.Since(lastTime) < m.evictionCooldown {
				throttledStuckPods = append(throttledStuckPods, sp)
				throttledWorkloadsMap[key] = true
				continue
			}
		}
		actionableStuckPods = append(actionableStuckPods, sp)
	}

	var throttledWorkloads []string
	for w := range throttledWorkloadsMap {
		throttledWorkloads = append(throttledWorkloads, w)
	}

	// ---- IPAM exhaustion ----
	if len(exhaustedPools) > 0 {
		if len(actionableStuckPods) > 0 {
			reason := fmt.Sprintf(
				"IPAM exhaustion: %d block(s) at 100%% (%s); evicting %d stuck pod(s) due to IPAM",
				len(exhaustedPools), strings.Join(exhaustedPools, ", "), countIPAMStuck(actionableStuckPods),
			)
			logger.Info("CNI unhealthy: IPAM exhaustion", "exhaustedPools", exhaustedPools, "evicting", len(actionableStuckPods))
			m.pendingTarget = &remediationTarget{
				actionType: ActionEvictStuckPods,
				stuckPods:  actionableStuckPods,
			}
			return &module.EvalResult{
				IsHealthy:        false,
				NeedsRemediation: true,
				Reason:           reason,
				Severity:         severityCritical,
			}, nil
		} else if len(throttledStuckPods) > 0 {
			reason := fmt.Sprintf(
				"IPAM exhaustion: %d block(s) at 100%% (%s); %d pod(s) waiting for IPs [%s]. Eviction throttled to avoid ReplicaSet churn",
				len(exhaustedPools), strings.Join(exhaustedPools, ", "), len(throttledStuckPods), strings.Join(throttledWorkloads, ", "),
			)
			logger.Info("CNI degraded: IPAM exhaustion eviction throttled", "exhaustedPools", exhaustedPools, "throttledPods", len(throttledStuckPods))
			m.pendingTarget = nil
			return &module.EvalResult{
				IsHealthy:        false,
				NeedsRemediation: false,
				Reason:           reason,
				Severity:         severityCritical,
			}, nil
		} else {
			reason := fmt.Sprintf(
				"IPAM exhaustion: %d block(s) at 100%% (%s); no stuck pods currently",
				len(exhaustedPools), strings.Join(exhaustedPools, ", "),
			)
			logger.Info("CNI unhealthy: IPAM block(s) at 100%", "exhaustedPools", exhaustedPools)
			m.pendingTarget = nil
			return &module.EvalResult{
				IsHealthy:        false,
				NeedsRemediation: false,
				Reason:           reason,
				Severity:         severityCritical,
			}, nil
		}
	}

	// ---- IPAM usage above defined threshold (e.g. >= 80%) ----
	var elevatedPools []string
	for cidr, pct := range usageByPool {
		if pct >= float64(ipamThreshold) {
			logger.Info("IPAM usage elevated",
				"block", cidr,
				"usagePct", fmt.Sprintf("%.1f%%", pct),
				"thresholdPct", fmt.Sprintf("%d%%", ipamThreshold),
			)
			elevatedPools = append(elevatedPools, fmt.Sprintf("%s (%.1f%%)", cidr, pct))
		}
	}

	if len(elevatedPools) > 0 {
		if len(actionableStuckPods) > 0 {
			reason := fmt.Sprintf(
				"IPAM threshold breached (%s >= %d%%); evicting %d stuck pod(s) without IP",
				strings.Join(elevatedPools, ", "), ipamThreshold, len(actionableStuckPods),
			)
			logger.Info("CNI degraded: IPAM threshold breached with stuck pods", "elevatedPools", elevatedPools)
			m.pendingTarget = &remediationTarget{
				actionType: ActionEvictStuckPods,
				stuckPods:  actionableStuckPods,
			}
			return &module.EvalResult{
				IsHealthy:        false,
				NeedsRemediation: true,
				Reason:           reason,
				Severity:         severityWarning,
			}, nil
		} else if len(throttledStuckPods) > 0 {
			reason := fmt.Sprintf(
				"IPAM threshold breached (%s >= %d%%); %d pod(s) waiting for IPs [%s]. Eviction throttled to avoid ReplicaSet churn",
				strings.Join(elevatedPools, ", "), ipamThreshold, len(throttledStuckPods), strings.Join(throttledWorkloads, ", "),
			)
			logger.Info("CNI degraded: IPAM threshold breached, eviction throttled", "elevatedPools", elevatedPools)
			m.pendingTarget = nil
			return &module.EvalResult{
				IsHealthy:        false,
				NeedsRemediation: false,
				Reason:           reason,
				Severity:         severityWarning,
			}, nil
		} else {
			m.pendingTarget = nil
			return &module.EvalResult{
				IsHealthy:        true,
				NeedsRemediation: false,
				Reason:           fmt.Sprintf("CNI plugin is healthy, but IPAM usage elevated above %d%%: %s", ipamThreshold, strings.Join(elevatedPools, ", ")),
				Severity:         severityWarning,
			}, nil
		}
	}

	// ---- pods stuck with other CNI errors ----
	if len(stuckPods) > 0 {
		if len(actionableStuckPods) > 0 {
			reason := fmt.Sprintf(
				"%d pod(s) stuck in ContainerCreating with CNI errors", len(actionableStuckPods),
			)
			logger.Info("CNI degraded: stuck pods", "count", len(actionableStuckPods))
			m.pendingTarget = &remediationTarget{
				actionType: ActionEvictStuckPods,
				stuckPods:  actionableStuckPods,
			}
			return &module.EvalResult{
				IsHealthy:        false,
				NeedsRemediation: true,
				Reason:           reason,
				Severity:         severityWarning,
			}, nil
		} else {
			reason := fmt.Sprintf(
				"%d pod(s) stuck in ContainerCreating with CNI errors; eviction cooldown active for [%s]",
				len(throttledStuckPods), strings.Join(throttledWorkloads, ", "),
			)
			m.pendingTarget = nil
			return &module.EvalResult{
				IsHealthy:        false,
				NeedsRemediation: false,
				Reason:           reason,
				Severity:         severityWarning,
			}, nil
		}
	}

	return &module.EvalResult{
		IsHealthy:        true,
		NeedsRemediation: false,
		Reason:           "CNI plugin is healthy",
		Severity:         severityNone,
	}, nil
}

// Remediate applies targeted automated fixes based on the specific failure:
//   - ActionRestartCalicoNode: only deletes unready calico-node pods.
//   - ActionReenableIPPool: only re-enables the disabled IPPool(s) and evicts pods waiting for them.
//   - ActionEvictStuckPods: only evicts workload pods stuck with CNI/IPAM errors.
func (m *CNIModule) Remediate(
	ctx context.Context,
	evalResult *module.EvalResult,
) (*module.RemediateResult, error) {
	logger := log.FromContext(ctx).WithName("cni-remediate")

	if evalResult == nil || !evalResult.NeedsRemediation {
		return &module.RemediateResult{Action: "none", Success: true}, nil
	}
	if evalResult.IsHealthy {
		return &module.RemediateResult{Action: "none (healthy)", Success: true}, nil
	}

	actionType := ActionNone
	var unreadyPods []string
	var disabledPools []string
	var stuckPods []StuckPod

	if m.pendingTarget != nil {
		actionType = m.pendingTarget.actionType
		unreadyPods = m.pendingTarget.unreadyPods
		disabledPools = m.pendingTarget.disabledPools
		stuckPods = m.pendingTarget.stuckPods
		m.pendingTarget = nil // reset after consuming
	} else {
		// Fallback for direct invocations (e.g. unit tests without prior Evaluate call)
		reason := evalResult.Reason
		switch {
		case strings.Contains(reason, "calico-node") && strings.Contains(reason, "not ready"):
			actionType = ActionRestartCalicoNode
		case strings.Contains(reason, "disabled"):
			actionType = ActionReenableIPPool
		case strings.Contains(reason, "IPAM exhaustion"),
			strings.Contains(reason, "stuck in ContainerCreating"),
			strings.Contains(reason, "stuck pods"):
			actionType = ActionEvictStuckPods
		}
	}

	var actions []string
	var lastErr error

	switch actionType {
	case ActionRestartCalicoNode:
		// TARGETED ACTION 1: Restart unready calico-node pods only
		if len(unreadyPods) == 0 {
			unreadyPods, _ = m.checkCalicoDaemonSet(ctx, kubeSystemNamespace, "calico-node")
		}
		for _, podName := range unreadyPods {
			pod := &corev1.Pod{}
			pod.Name = podName
			pod.Namespace = kubeSystemNamespace
			if err := m.Client.Delete(ctx, pod); err != nil {
				logger.Error(err, "failed to delete unready calico-node pod", "pod", podName)
				lastErr = err
			} else {
				logger.Info("deleted unready calico-node pod for restart", "pod", podName)
				actions = append(actions, "restarted calico-node/"+podName)
			}
		}

	case ActionReenableIPPool:
		// TARGETED ACTION 2: Re-enable the specific disabled IPPools and evict waiting pods
		if len(disabledPools) == 0 {
			_, _, disabledPools, _ = m.checkIPAMUsage(ctx)
		}
		for _, poolName := range disabledPools {
			pool := &unstructured.Unstructured{}
			pool.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   calicoCRDGroup,
				Version: "v1",
				Kind:    calicoIPPoolKind,
			})
			if err := m.Client.Get(ctx, types.NamespacedName{Name: poolName}, pool); err == nil {
				patch := pool.DeepCopy()
				if err := unstructured.SetNestedField(patch.Object, false, "spec", "disabled"); err == nil {
					if err := m.Client.Patch(ctx, patch, client.MergeFrom(pool)); err != nil {
						logger.Error(err, "failed to re-enable disabled IPPool", "pool", poolName)
						lastErr = err
					} else {
						logger.Info("re-enabled disabled IPPool", "pool", poolName)
						actions = append(actions, "re-enabled IPPool "+poolName)
					}
				}
			}
		}

		if len(stuckPods) == 0 {
			stuckPods, _ = m.checkStuckPods(ctx, 0)
		}
		for _, sp := range stuckPods {
			pod := &corev1.Pod{}
			pod.Name = sp.Name
			pod.Namespace = sp.Namespace
			if err := m.Client.Delete(ctx, pod); err != nil {
				logger.Error(err, "failed to delete CNI-stuck pod after IPPool re-enable", "pod", sp.Name, "ns", sp.Namespace)
				lastErr = err
			} else {
				logger.Info("evicted CNI-stuck pod to acquire unblocked IP", "pod", sp.Name, "ns", sp.Namespace)
				actions = append(actions, fmt.Sprintf("evicted %s/%s", sp.Namespace, sp.Name))
			}
		}

	case ActionEvictStuckPods:
		// TARGETED ACTION 3: Evict stuck workload pods only
		if m.lastWorkloadEviction == nil {
			m.lastWorkloadEviction = make(map[string]time.Time)
		}
		if len(stuckPods) == 0 {
			stuckPods, _ = m.checkStuckPods(ctx, 0)
		}
		now := time.Now()
		for _, sp := range stuckPods {
			pod := &corev1.Pod{}
			pod.Name = sp.Name
			pod.Namespace = sp.Namespace
			if err := m.Client.Delete(ctx, pod); err != nil {
				logger.Error(err, "failed to delete CNI-stuck pod", "pod", sp.Name, "ns", sp.Namespace)
				lastErr = err
			} else {
				key := sp.WorkloadKey
				if key == "" {
					key = fmt.Sprintf("%s/Pod/%s", sp.Namespace, sp.Name)
				}
				m.lastWorkloadEviction[key] = now
				logger.Info("evicted CNI-stuck pod", "pod", sp.Name, "ns", sp.Namespace, "workload", key, "ipamExhausted", sp.IPAMExhausted)
				actions = append(actions, fmt.Sprintf("evicted %s/%s", sp.Namespace, sp.Name))
			}
		}

		// Prune expired cooldown entries
		for k, t := range m.lastWorkloadEviction {
			if now.Sub(t) > 2*m.evictionCooldown {
				delete(m.lastWorkloadEviction, k)
			}
		}

	default:
		return &module.RemediateResult{
			Action:  "none",
			Success: true,
		}, nil
	}

	if len(actions) == 0 && lastErr == nil {
		return &module.RemediateResult{
			Action:  "no targets found (already recovered?)",
			Success: true,
		}, nil
	}

	return &module.RemediateResult{
		Action:  strings.Join(actions, "; "),
		Success: lastErr == nil,
		Err:     lastErr,
	}, nil
}

func signalInt(cr *module.CheckResult, key string, fallback int) int {
	if cr == nil || cr.Signals == nil {
		return fallback
	}
	v, ok := cr.Signals[key]
	if !ok {
		return fallback
	}
	switch val := v.(type) {
	case int:
		if val > 0 {
			return val
		}
	case int32:
		if val > 0 {
			return int(val)
		}
	case int64:
		if val > 0 {
			return int(val)
		}
	case float64:
		if val > 0 {
			return int(val)
		}
	}
	return fallback
}

func signalStringSlice(cr *module.CheckResult, key string) []string {
	if cr == nil {
		return nil
	}
	v, ok := cr.Signals[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func signalStuckPodSlice(cr *module.CheckResult) []StuckPod {
	if cr == nil {
		return nil
	}
	v, ok := cr.Signals[signalStuckPods]
	if !ok {
		return nil
	}
	if sp, ok := v.([]StuckPod); ok {
		return sp
	}
	return nil
}

func hasIPAMStuck(pods []StuckPod) bool {
	for _, p := range pods {
		if p.IPAMExhausted {
			return true
		}
	}
	return false
}

func countIPAMStuck(pods []StuckPod) int {
	n := 0
	for _, p := range pods {
		if p.IPAMExhausted {
			n++
		}
	}
	return n
}
