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
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/pkg/module"
)

const (
	moduleName = "networkpolicy"

	severityWarning = "warning"
	severityInfo    = "info"

	// Action types dispatched from Evaluate to Remediate.
	ActionRestartFelix = "restart_enforcement_agent"

	// Calico enforcement agent pods.
	calicoNamespace  = "kube-system"
	calicoLabelKey   = "k8s-app"
	calicoLabelValue = "calico-node"

	// Default cooldown between enforcement agent restarts to prevent thrashing.
	defaultCooldownSeconds = 60
)

// npConfig holds the module configuration resolved from the CR spec.
type npConfig struct {
	autoHeal        bool
	cooldownSeconds int32
}

// NetworkPolicyModule implements the module.Module interface. It monitors the
// health of the underlying CNI dataplane enforcement agent (Calico felix running inside
// calico-node pods) to prevent stale packet-filtering rules in the Linux kernel.
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
		autoHeal:        true,
		cooldownSeconds: defaultCooldownSeconds,
	}
	if spec == nil {
		return c
	}
	np := spec.NetworkPolicy
	c.autoHeal = np.AutoHeal
	if np.CooldownSeconds > 0 {
		c.cooldownSeconds = np.CooldownSeconds
	}
	return c
}

// splitKey splits a "namespace/name" key.
func splitKey(key string) (namespace, name string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete

// Check gathers raw telemetry regarding the health of Calico enforcement agent
// (calico-node / felix) pods.
func (m *NetworkPolicyModule) Check(ctx context.Context, spec *remediationv1alpha1.NetworkRemediationSpec) (*module.CheckResult, error) {
	log := logf.FromContext(ctx).WithName(moduleName)
	m.cfg = resolveConfig(spec)

	signals := map[string]any{
		"autoHeal":        m.cfg.autoHeal,
		"cooldownSeconds": m.cfg.cooldownSeconds,
	}

	var pods corev1.PodList
	if err := m.Client.List(ctx, &pods,
		client.InNamespace(calicoNamespace),
		client.MatchingLabels{calicoLabelKey: calicoLabelValue}); err != nil {
		log.Error(err, "failed to list calico-node pods")
		return nil, fmt.Errorf("failed to list calico-node pods: %w", err)
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

	return &module.CheckResult{Signals: signals}, nil
}

// Evaluate analyzes the signals and determines if the CNI enforcement agent is degraded.
func (m *NetworkPolicyModule) Evaluate(ctx context.Context, checkResult *module.CheckResult) (*module.EvalResult, error) {
	if checkResult == nil || checkResult.Signals == nil {
		return &module.EvalResult{IsHealthy: true, Reason: "No check signals available", Severity: severityInfo}, nil
	}
	s := checkResult.Signals

	autoHeal, _ := s["autoHeal"].(bool)
	unhealthyFelix, _ := s["unhealthyFelixPods"].([]string)
	calicoNodeTotal, _ := s["calicoNodeTotal"].(int)

	if len(unhealthyFelix) > 0 {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: autoHeal,
			Reason:           fmt.Sprintf("Calico enforcement agent unhealthy on %d pod(s): %s; policy enforcement may be stale", len(unhealthyFelix), strings.Join(unhealthyFelix, ", ")),
			Severity:         severityWarning,
			ActionType:       ActionRestartFelix,
			ActionData:       map[string]any{"pods": unhealthyFelix},
		}, nil
	}

	return &module.EvalResult{
		IsHealthy:        true,
		NeedsRemediation: false,
		Reason:           fmt.Sprintf("All %d Calico enforcement agent pod(s) healthy; dataplane rules active", calicoNodeTotal),
		Severity:         severityInfo,
	}, nil
}

// Remediate executes remediation for degraded enforcement agent pods.
func (m *NetworkPolicyModule) Remediate(ctx context.Context, evalResult *module.EvalResult) (*module.RemediateResult, error) {
	if evalResult == nil {
		return &module.RemediateResult{Action: "none", Success: true}, nil
	}
	if evalResult.ActionType == ActionRestartFelix {
		return m.restartEnforcementAgent(ctx, evalResult)
	}
	return &module.RemediateResult{Action: "none", Success: true}, nil
}

// restartEnforcementAgent deletes unhealthy calico-node pods so the DaemonSet
// recreates them and felix re-programs the policy rules.
func (m *NetworkPolicyModule) restartEnforcementAgent(ctx context.Context, eval *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName(moduleName)

	cooldownDuration := time.Duration(m.cfg.cooldownSeconds) * time.Second
	if cooldownDuration == 0 {
		cooldownDuration = defaultCooldownSeconds * time.Second
	}

	if !m.lastFelixRestart.IsZero() && time.Since(m.lastFelixRestart) < cooldownDuration {
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
