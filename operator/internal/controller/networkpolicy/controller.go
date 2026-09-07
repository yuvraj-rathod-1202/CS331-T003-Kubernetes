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

package networkpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/pkg/module"
)

const (
	moduleName = "networkpolicy"

	// labelValueTrue is the value a protected policy's label must carry.
	labelValueTrue = "true"

	severityCritical = "critical"
	severityWarning  = "warning"
	severityInfo     = "info"

	// Action types dispatched from Evaluate to Remediate.
	ActionRestoreDeleted = "restore_deleted_policies"
	ActionRestoreDrift   = "restore_drifted_policies"
	ActionRestartFelix   = "restart_enforcement_agent"
	ActionSyncBaseline   = "sync_baseline"

	// Defaults (mirrored by the +kubebuilder:default markers in the CRD spec).
	defaultProtectedLabel    = "remediation.cn-operator.yuvraj-rathod-1202.github.io/protected"
	defaultSnapshotNamespace = "kube-system"
	defaultSnapshotConfigMap = "networkpolicy-baseline"

	// Calico enforcement agent pods.
	calicoNamespace  = "kube-system"
	calicoLabelKey   = "k8s-app"
	calicoLabelValue = "calico-node"

	// felixRestartCooldown prevents restart-thrashing of calico-node pods.
	felixRestartCooldown = 60 * time.Second
)

// npConfig holds the module configuration resolved from the CR spec.
type npConfig struct {
	protectedLabel    string
	snapshotNamespace string
	snapshotConfigMap string
	autoRestore       bool
	verifyEnforcement bool
}

// NetworkPolicyModule implements the module.Module interface. It protects a set
// of NetworkPolicies by snapshotting their baseline spec and healing deletion,
// drift, and stale enforcement (crashed calico-node/felix).
type NetworkPolicyModule struct {
	// Client is the Kubernetes API client for interacting with cluster resources.
	Client client.Client
	// cfg is the configuration resolved during the last Check, reused by Remediate.
	cfg npConfig
	// lastFelixRestart tracks the last enforcement-agent restart for cooldown.
	lastFelixRestart time.Time
}

// New creates a new NetworkPolicyModule instance.
func New(c client.Client) *NetworkPolicyModule {
	return &NetworkPolicyModule{Client: c}
}

// Name returns the module name.
func (m *NetworkPolicyModule) Name() string {
	return moduleName
}

// resolveConfig applies defaults over whatever the CR spec provides.
func resolveConfig(spec *remediationv1alpha1.NetworkRemediationSpec) npConfig {
	c := npConfig{
		protectedLabel:    defaultProtectedLabel,
		snapshotNamespace: defaultSnapshotNamespace,
		snapshotConfigMap: defaultSnapshotConfigMap,
		autoRestore:       true,
		verifyEnforcement: true,
	}
	if spec == nil {
		return c
	}
	np := spec.NetworkPolicy
	if np.ProtectedLabel != "" {
		c.protectedLabel = np.ProtectedLabel
	}
	if np.SnapshotNamespace != "" {
		c.snapshotNamespace = np.SnapshotNamespace
	}
	if np.SnapshotConfigMapName != "" {
		c.snapshotConfigMap = np.SnapshotConfigMapName
	}
	c.autoRestore = np.AutoRestore
	c.verifyEnforcement = np.VerifyEnforcement
	return c
}

// splitKey splits a "namespace/name" snapshot key.
func splitKey(key string) (namespace, name string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
}

// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete

// Check gathers raw signals for the NetworkPolicy module: the live spec of every
// protected policy, the stored baseline, whether snapshotted policies still
// exist, and the health of the Calico enforcement agent. No decisions here.
func (m *NetworkPolicyModule) Check(ctx context.Context, spec *remediationv1alpha1.NetworkRemediationSpec) (*module.CheckResult, error) {
	log := logf.FromContext(ctx).WithName(moduleName)
	m.cfg = resolveConfig(spec)

	signals := map[string]any{
		"autoRestore":       m.cfg.autoRestore,
		"verifyEnforcement": m.cfg.verifyEnforcement,
	}

	// 1. Live spec of every protected NetworkPolicy (across all namespaces).
	var npList networkingv1.NetworkPolicyList
	if err := m.Client.List(ctx, &npList, client.MatchingLabels{m.cfg.protectedLabel: labelValueTrue}); err != nil {
		return nil, fmt.Errorf("failed to list network policies: %w", err)
	}
	liveSpecs := map[string]string{}
	for i := range npList.Items {
		p := &npList.Items[i]
		js, err := json.Marshal(p.Spec)
		if err != nil {
			log.Error(err, "failed to marshal policy spec", "policy", p.Namespace+"/"+p.Name)
			continue
		}
		liveSpecs[p.Namespace+"/"+p.Name] = string(js)
	}
	signals["liveSpecs"] = liveSpecs

	// 2. Stored baseline snapshot.
	snapshot := map[string]string{}
	var cm corev1.ConfigMap
	cmKey := types.NamespacedName{Namespace: m.cfg.snapshotNamespace, Name: m.cfg.snapshotConfigMap}
	if err := m.Client.Get(ctx, cmKey, &cm); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to read baseline configmap")
		}
	} else {
		maps.Copy(snapshot, cm.Data)
	}
	signals["snapshot"] = snapshot

	// 3. For snapshotted policies not currently protected-live, decide whether
	// they were truly deleted or merely had their protected label removed.
	orphanExists := map[string]bool{}
	for key := range snapshot {
		if _, ok := liveSpecs[key]; ok {
			continue
		}
		ns, name := splitKey(key)
		var np networkingv1.NetworkPolicy
		err := m.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &np)
		switch {
		case err == nil:
			orphanExists[key] = true // exists but no longer protected -> prune baseline
		case apierrors.IsNotFound(err):
			orphanExists[key] = false // truly deleted -> restore
		default:
			log.Error(err, "failed to check policy existence", "policy", key)
			orphanExists[key] = true // unknown: do not recreate destructively
		}
	}
	signals["orphanExists"] = orphanExists

	// 4. Enforcement agent (calico-node / felix) health.
	if m.cfg.verifyEnforcement {
		m.checkEnforcementAgent(ctx, signals)
	}

	return &module.CheckResult{Signals: signals}, nil
}

// checkEnforcementAgent records which calico-node pods look unhealthy.
func (m *NetworkPolicyModule) checkEnforcementAgent(ctx context.Context, signals map[string]any) {
	log := logf.FromContext(ctx).WithName(moduleName)
	var pods corev1.PodList
	if err := m.Client.List(ctx, &pods,
		client.InNamespace(calicoNamespace),
		client.MatchingLabels{calicoLabelKey: calicoLabelValue}); err != nil {
		log.Error(err, "failed to list calico-node pods")
		return
	}

	unhealthy := []string{}
	for i := range pods.Items {
		p := &pods.Items[i]
		bad := p.Status.Phase != corev1.PodRunning
		for _, cs := range p.Status.ContainerStatuses {
			if cs.RestartCount >= 3 || !cs.Ready {
				bad = true
			}
			if cs.State.Waiting != nil {
				if r := cs.State.Waiting.Reason; r == "CrashLoopBackOff" || r == "Error" {
					bad = true
				}
			}
		}
		if bad {
			unhealthy = append(unhealthy, p.Namespace+"/"+p.Name)
		}
	}
	signals["calicoNodeTotal"] = len(pods.Items)
	signals["unhealthyFelixPods"] = unhealthy
}

// Evaluate analyzes the signals and decides which (if any) remediation to run.
func (m *NetworkPolicyModule) Evaluate(ctx context.Context, checkResult *module.CheckResult) (*module.EvalResult, error) {
	if checkResult == nil || checkResult.Signals == nil {
		return &module.EvalResult{IsHealthy: true, Reason: "No check signals available", Severity: severityInfo}, nil
	}
	s := checkResult.Signals

	liveSpecs, _ := s["liveSpecs"].(map[string]string)
	snapshot, _ := s["snapshot"].(map[string]string)
	orphanExists, _ := s["orphanExists"].(map[string]bool)
	autoRestore, _ := s["autoRestore"].(bool)
	verifyEnforcement, _ := s["verifyEnforcement"].(bool)
	unhealthyFelix, _ := s["unhealthyFelixPods"].([]string)

	deleted := []string{}
	drifted := []string{}
	for key, snapJSON := range snapshot {
		if liveJSON, ok := liveSpecs[key]; ok {
			if liveJSON != snapJSON {
				drifted = append(drifted, key)
			}
		} else if exists, ok := orphanExists[key]; ok && !exists {
			deleted = append(deleted, key)
		}
	}
	missingBaseline := []string{}
	for key := range liveSpecs {
		if _, ok := snapshot[key]; !ok {
			missingBaseline = append(missingBaseline, key)
		}
	}
	unlabeled := []string{}
	for key, exists := range orphanExists {
		if exists {
			unlabeled = append(unlabeled, key)
		}
	}
	slices.Sort(deleted)
	slices.Sort(drifted)
	slices.Sort(missingBaseline)
	slices.Sort(unlabeled)

	// Priority 1: a protected policy was deleted (security posture silently changed).
	if len(deleted) > 0 {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: autoRestore,
			Reason:           fmt.Sprintf("%d protected NetworkPolicy(ies) deleted: %s", len(deleted), strings.Join(deleted, ", ")),
			Severity:         severityCritical,
			ActionType:       ActionRestoreDeleted,
			ActionData:       map[string]any{"keys": deleted, "snapshot": snapshot},
		}, nil
	}

	// Priority 2: a protected policy drifted from its baseline.
	if len(drifted) > 0 {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: autoRestore,
			Reason:           fmt.Sprintf("%d protected NetworkPolicy(ies) drifted from baseline: %s", len(drifted), strings.Join(drifted, ", ")),
			Severity:         severityWarning,
			ActionType:       ActionRestoreDrift,
			ActionData:       map[string]any{"keys": drifted, "snapshot": snapshot},
		}, nil
	}

	// Priority 3: enforcement agent unhealthy -> policies may not be enforced.
	if verifyEnforcement && len(unhealthyFelix) > 0 {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: true,
			Reason:           fmt.Sprintf("Calico enforcement agent unhealthy on %d pod(s): %s; policy enforcement may be stale", len(unhealthyFelix), strings.Join(unhealthyFelix, ", ")),
			Severity:         severityWarning,
			ActionType:       ActionRestartFelix,
			ActionData:       map[string]any{"pods": unhealthyFelix},
		}, nil
	}

	// Priority 4: baseline maintenance (learn new protected policies, drop unprotected ones).
	if len(missingBaseline) > 0 || len(unlabeled) > 0 {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: true,
			Reason:           fmt.Sprintf("Baseline sync required (record %d new, prune %d unprotected)", len(missingBaseline), len(unlabeled)),
			Severity:         severityInfo,
			ActionType:       ActionSyncBaseline,
			ActionData:       map[string]any{"record": missingBaseline, "prune": unlabeled, "liveSpecs": liveSpecs},
		}, nil
	}

	return &module.EvalResult{
		IsHealthy:        true,
		NeedsRemediation: false,
		Reason:           fmt.Sprintf("All %d protected NetworkPolicy(ies) match baseline and enforcement is healthy", len(snapshot)),
		Severity:         severityInfo,
	}, nil
}

// Remediate dispatches to the corrective action chosen by Evaluate.
func (m *NetworkPolicyModule) Remediate(ctx context.Context, evalResult *module.EvalResult) (*module.RemediateResult, error) {
	if evalResult == nil {
		return &module.RemediateResult{Action: "none", Success: true}, nil
	}
	switch evalResult.ActionType {
	case ActionRestoreDeleted:
		return m.restoreDeleted(ctx, evalResult)
	case ActionRestoreDrift:
		return m.restoreDrift(ctx, evalResult)
	case ActionRestartFelix:
		return m.restartEnforcementAgent(ctx, evalResult)
	case ActionSyncBaseline:
		return m.syncBaseline(ctx, evalResult)
	default:
		return &module.RemediateResult{Action: "none", Success: true}, nil
	}
}

// restoreDeleted recreates deleted protected policies from their baseline spec.
func (m *NetworkPolicyModule) restoreDeleted(ctx context.Context, eval *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName(moduleName)
	keys, _ := eval.ActionData["keys"].([]string)
	snapshot, _ := eval.ActionData["snapshot"].(map[string]string)

	restored := []string{}
	for _, key := range keys {
		specJSON, ok := snapshot[key]
		if !ok {
			continue
		}
		var npSpec networkingv1.NetworkPolicySpec
		if err := json.Unmarshal([]byte(specJSON), &npSpec); err != nil {
			log.Error(err, "failed to decode baseline spec", "policy", key)
			continue
		}
		ns, name := splitKey(key)
		np := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      name,
				Labels:    map[string]string{m.cfg.protectedLabel: labelValueTrue},
			},
			Spec: npSpec,
		}
		if err := m.Client.Create(ctx, np); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return &module.RemediateResult{Action: "recreate " + key, Success: false, Err: err}, err
		}
		restored = append(restored, key)
	}
	msg := fmt.Sprintf("Recreated %d deleted protected NetworkPolicy(ies): %s", len(restored), strings.Join(restored, ", "))
	log.Info(msg)
	return &module.RemediateResult{Action: msg, Success: true}, nil
}

// restoreDrift reverts drifted policies back to their baseline spec.
func (m *NetworkPolicyModule) restoreDrift(ctx context.Context, eval *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName(moduleName)
	keys, _ := eval.ActionData["keys"].([]string)
	snapshot, _ := eval.ActionData["snapshot"].(map[string]string)

	fixed := []string{}
	for _, key := range keys {
		specJSON, ok := snapshot[key]
		if !ok {
			continue
		}
		var npSpec networkingv1.NetworkPolicySpec
		if err := json.Unmarshal([]byte(specJSON), &npSpec); err != nil {
			log.Error(err, "failed to decode baseline spec", "policy", key)
			continue
		}
		ns, name := splitKey(key)
		var np networkingv1.NetworkPolicy
		if err := m.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &np); err != nil {
			return &module.RemediateResult{Action: "fetch " + key, Success: false, Err: err}, err
		}
		np.Spec = npSpec
		if err := m.Client.Update(ctx, &np); err != nil {
			return &module.RemediateResult{Action: "restore " + key, Success: false, Err: err}, err
		}
		fixed = append(fixed, key)
	}
	msg := fmt.Sprintf("Reverted %d drifted NetworkPolicy(ies) to baseline: %s", len(fixed), strings.Join(fixed, ", "))
	log.Info(msg)
	return &module.RemediateResult{Action: msg, Success: true}, nil
}

// restartEnforcementAgent deletes unhealthy calico-node pods so the DaemonSet
// recreates them and felix re-programs the policy rules.
func (m *NetworkPolicyModule) restartEnforcementAgent(ctx context.Context, eval *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName(moduleName)

	if !m.lastFelixRestart.IsZero() && time.Since(m.lastFelixRestart) < felixRestartCooldown {
		msg := "enforcement-agent restart on cooldown; skipping to avoid thrashing"
		log.Info(msg)
		return &module.RemediateResult{Action: msg, Success: true}, nil
	}

	pods, _ := eval.ActionData["pods"].([]string)
	deleted := []string{}
	for _, key := range pods {
		ns, name := splitKey(key)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		if err := m.Client.Delete(ctx, pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return &module.RemediateResult{Action: "delete " + key, Success: false, Err: err}, err
		}
		deleted = append(deleted, key)
	}
	m.lastFelixRestart = time.Now()
	msg := fmt.Sprintf("Restarted %d calico-node pod(s) to resync policy enforcement: %s", len(deleted), strings.Join(deleted, ", "))
	log.Info(msg)
	return &module.RemediateResult{Action: msg, Success: true}, nil
}

// syncBaseline records baselines for newly protected policies and prunes
// entries for policies that are no longer protected.
func (m *NetworkPolicyModule) syncBaseline(ctx context.Context, eval *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName(moduleName)
	record, _ := eval.ActionData["record"].([]string)
	prune, _ := eval.ActionData["prune"].([]string)
	liveSpecs, _ := eval.ActionData["liveSpecs"].(map[string]string)

	var cm corev1.ConfigMap
	cmKey := types.NamespacedName{Namespace: m.cfg.snapshotNamespace, Name: m.cfg.snapshotConfigMap}
	create := false
	if err := m.Client.Get(ctx, cmKey, &cm); err != nil {
		if !apierrors.IsNotFound(err) {
			return &module.RemediateResult{Action: "fetch baseline configmap", Success: false, Err: err}, err
		}
		create = true
		cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: m.cfg.snapshotNamespace, Name: m.cfg.snapshotConfigMap}}
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	for _, key := range record {
		if js, ok := liveSpecs[key]; ok {
			cm.Data[key] = js
		}
	}
	for _, key := range prune {
		delete(cm.Data, key)
	}

	if create {
		if err := m.Client.Create(ctx, &cm); err != nil {
			return &module.RemediateResult{Action: "create baseline configmap", Success: false, Err: err}, err
		}
	} else if err := m.Client.Update(ctx, &cm); err != nil {
		return &module.RemediateResult{Action: "update baseline configmap", Success: false, Err: err}, err
	}

	msg := fmt.Sprintf("Synced NetworkPolicy baseline (recorded %d, pruned %d)", len(record), len(prune))
	log.Info(msg)
	return &module.RemediateResult{Action: msg, Success: true}, nil
}
