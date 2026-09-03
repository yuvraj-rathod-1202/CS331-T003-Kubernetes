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

package coredns

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/pkg/module"
)

const (
	corednsNamespace = "kube-system"
	corednsName      = "coredns"

	// Default values
	defaultExpectedReplicas   int32 = 2
	defaultLatencyThresholdMs int64 = 150
)

// Regex to detect forward directives in Corefile
var forwardRegex = regexp.MustCompile(`(?m)^\s*forward\s+\.\s+([^\s{]+)`)

// CoreDNSModule implements the module.Module interface for CoreDNS health monitoring.
type CoreDNSModule struct {
	// Client is the Kubernetes API client for interacting with cluster resources.
	Client client.Client
}

// New creates a new CoreDNSModule instance.
func New(c client.Client) *CoreDNSModule {
	return &CoreDNSModule{
		Client: c,
	}
}

// Name returns the module name.
func (m *CoreDNSModule) Name() string {
	return "coredns"
}

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;update;patch

// Check gathers raw health signals for CoreDNS.
func (m *CoreDNSModule) Check(ctx context.Context, spec *remediationv1alpha1.NetworkRemediationSpec) (*module.CheckResult, error) {
	log := logf.FromContext(ctx).WithName("coredns")
	signals := map[string]any{}

	expectedReplicas := defaultExpectedReplicas
	if spec != nil && spec.CoreDNS.ExpectedReplicas > 0 {
		expectedReplicas = spec.CoreDNS.ExpectedReplicas
	}
	signals["expectedReplicas"] = expectedReplicas

	autoHealReplicas := true
	if spec != nil && spec.CoreDNS.ExpectedReplicas > 0 {
		autoHealReplicas = spec.CoreDNS.AutoHealReplicas
	}
	signals["autoHealReplicas"] = autoHealReplicas

	autoHealConfigMap := true
	if spec != nil {
		autoHealConfigMap = spec.CoreDNS.AutoHealConfigMap
	}
	signals["autoHealConfigMap"] = autoHealConfigMap

	fallbackDNS := []string{"/etc/resolv.conf"}
	if spec != nil && len(spec.CoreDNS.FallbackUpstreamServers) > 0 {
		fallbackDNS = spec.CoreDNS.FallbackUpstreamServers
	}
	signals["fallbackDNS"] = fallbackDNS

	// 1. Inspect CoreDNS Deployment in kube-system
	var deploy appsv1.Deployment
	deployKey := types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}
	if err := m.Client.Get(ctx, deployKey, &deploy); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "CoreDNS deployment not found in kube-system")
			signals["deploymentFound"] = false
			return &module.CheckResult{Signals: signals}, nil
		}
		return nil, fmt.Errorf("failed to get coredns deployment: %w", err)
	}

	signals["deploymentFound"] = true
	var desiredReplicas int32 = 1
	if deploy.Spec.Replicas != nil {
		desiredReplicas = *deploy.Spec.Replicas
	}
	signals["desiredReplicas"] = desiredReplicas
	signals["availableReplicas"] = deploy.Status.AvailableReplicas
	signals["readyReplicas"] = deploy.Status.ReadyReplicas
	signals["updatedReplicas"] = deploy.Status.UpdatedReplicas

	// Check if CPU limits/requests are heavily throttled (<10m CPU, e.g. 1m from Experiment 2.2)
	cpuThrottled := false
	if len(deploy.Spec.Template.Spec.Containers) > 0 {
		container := deploy.Spec.Template.Spec.Containers[0]
		if reqCPU, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			if reqCPU.MilliValue() > 0 && reqCPU.MilliValue() <= 5 {
				cpuThrottled = true
			}
		}
		if limCPU, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
			if limCPU.MilliValue() > 0 && limCPU.MilliValue() <= 5 {
				cpuThrottled = true
			}
		}
	}
	signals["cpuThrottled"] = cpuThrottled

	// 2. Inspect CoreDNS Pods in kube-system
	var podList corev1.PodList
	if err := m.Client.List(ctx, &podList, client.InNamespace(corednsNamespace), client.MatchingLabels{"k8s-app": "kube-dns"}); err != nil {
		log.Error(err, "Failed to list coredns pods")
	} else {
		runningPods := 0
		crashLoopingPods := 0
		var totalRestarts int32 = 0

		for _, pod := range podList.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningPods++
			}
			for _, cs := range pod.Status.ContainerStatuses {
				totalRestarts += cs.RestartCount
				if cs.State.Waiting != nil {
					reason := cs.State.Waiting.Reason
					if reason == "CrashLoopBackOff" || reason == "Error" || reason == "ImagePullBackOff" {
						crashLoopingPods++
					}
				}
			}
		}

		signals["podCount"] = len(podList.Items)
		signals["runningPods"] = runningPods
		signals["crashLoopingPods"] = crashLoopingPods
		signals["totalRestarts"] = totalRestarts
	}

	// 3. Inspect CoreDNS ConfigMap in kube-system
	var cm corev1.ConfigMap
	cmKey := types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}
	if err := m.Client.Get(ctx, cmKey, &cm); err != nil {
		log.Error(err, "Failed to get coredns configmap")
		signals["configMapFound"] = false
	} else {
		signals["configMapFound"] = true
		corefile := cm.Data["Corefile"]
		signals["corefileLength"] = len(corefile)

		matches := forwardRegex.FindStringSubmatch(corefile)
		upstreamCorrupted := false
		var forwardTarget string
		if len(matches) > 1 {
			forwardTarget = strings.TrimSpace(matches[1])
			signals["forwardTarget"] = forwardTarget

			// Detect known bad/unreachable IP blocks (e.g. 192.0.2.x from RFC 5737 TEST-NET-1, 198.51.100.x, 203.0.113.x)
			if strings.HasPrefix(forwardTarget, "192.0.2.") ||
				strings.HasPrefix(forwardTarget, "198.51.100.") ||
				strings.HasPrefix(forwardTarget, "203.0.113.") ||
				forwardTarget == "0.0.0.0" ||
				forwardTarget == "127.0.0.1" {
				upstreamCorrupted = true
			}
		}
		signals["upstreamCorrupted"] = upstreamCorrupted
	}

	// 4. Query Prometheus metrics for DNS latency (if Prometheus is available)
	if promLatencyMs, err := queryPrometheusLatency(ctx); err == nil && promLatencyMs > 0 {
		signals["prometheusLatencyMs"] = promLatencyMs
	}

	return &module.CheckResult{
		Signals: signals,
	}, nil
}

// Evaluate analyzes the CoreDNS check results to determine if there is an issue.
func (m *CoreDNSModule) Evaluate(ctx context.Context, checkResult *module.CheckResult) (*module.EvalResult, error) {
	if checkResult == nil || checkResult.Signals == nil {
		return &module.EvalResult{
			IsHealthy:        true,
			NeedsRemediation: false,
			Reason:           "No check signals available",
			Severity:         "info",
		}, nil
	}

	signals := checkResult.Signals

	// Check if deployment exists
	if found, ok := signals["deploymentFound"].(bool); ok && !found {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: false,
			Reason:           "CoreDNS deployment missing in kube-system namespace",
			Severity:         "critical",
		}, nil
	}

	expectedReplicas := defaultExpectedReplicas
	if er, ok := signals["expectedReplicas"].(int32); ok && er > 0 {
		expectedReplicas = er
	}

	availableReplicas, _ := signals["availableReplicas"].(int32)
	readyReplicas, _ := signals["readyReplicas"].(int32)
	desiredReplicas, _ := signals["desiredReplicas"].(int32)
	autoHealReplicas, _ := signals["autoHealReplicas"].(bool)
	autoHealConfigMap, _ := signals["autoHealConfigMap"].(bool)
	upstreamCorrupted, _ := signals["upstreamCorrupted"].(bool)
	forwardTarget, _ := signals["forwardTarget"].(string)
	cpuThrottled, _ := signals["cpuThrottled"].(bool)
	crashLoopingPods, _ := signals["crashLoopingPods"].(int)

	// Scenario 1: CoreDNS Scale to Zero / Complete Availability Loss
	if availableReplicas == 0 || desiredReplicas == 0 {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: autoHealReplicas,
			Reason:           fmt.Sprintf("CoreDNS deployment has 0 available replicas (expected %d, desired %d); all cluster DNS resolution is unavailable", expectedReplicas, desiredReplicas),
			Severity:         "critical",
		}, nil
	}

	// Scenario 2: Replica Degradation
	if availableReplicas < expectedReplicas {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: autoHealReplicas,
			Reason:           fmt.Sprintf("CoreDNS deployment is degraded: %d/%d available replicas ready", availableReplicas, expectedReplicas),
			Severity:         "warning",
		}, nil
	}

	// Scenario 3: Corrupted Upstream DNS Configuration (Experiment 2.3)
	if upstreamCorrupted {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: autoHealConfigMap,
			Reason:           fmt.Sprintf("CoreDNS upstream resolver corrupted (forward directive points to unreachable '%s'); external DNS lookups failing", forwardTarget),
			Severity:         "critical",
		}, nil
	}

	// Scenario 4: CPU Throttling / Resource Starvation (Experiment 2.2)
	if cpuThrottled {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: true,
			Reason:           "CoreDNS deployment CPU is severely throttled (<= 5m CPU request/limit), causing DNS latency spikes",
			Severity:         "warning",
		}, nil
	}

	// Scenario 5: Prometheus DNS Latency Spike Detection
	latencyThresholdMs := defaultLatencyThresholdMs
	if lt, ok := signals["latencyThresholdMs"].(int64); ok && lt > 0 {
		latencyThresholdMs = lt
	}
	if promLatencyMs, ok := signals["prometheusLatencyMs"].(float64); ok && promLatencyMs > float64(latencyThresholdMs) {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: true,
			Reason:           fmt.Sprintf("Prometheus metric query detected DNS latency (%.2f ms) exceeding threshold (%d ms)", promLatencyMs, latencyThresholdMs),
			Severity:         "warning",
		}, nil
	}

	// Scenario 6: CrashLooping Pods
	if crashLoopingPods > 0 {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: true,
			Reason:           fmt.Sprintf("%d CoreDNS pod(s) are crashlooping", crashLoopingPods),
			Severity:         "critical",
		}, nil
	}

	// Healthy
	return &module.EvalResult{
		IsHealthy:        true,
		NeedsRemediation: false,
		Reason:           fmt.Sprintf("CoreDNS is healthy (%d/%d replicas ready and serving)", readyReplicas, expectedReplicas),
		Severity:         "info",
	}, nil
}

// Remediate executes corrective actions for CoreDNS failures.
func (m *CoreDNSModule) Remediate(ctx context.Context, evalResult *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName("coredns")
	reason := evalResult.Reason

	// Remediation 1: Fix Scale to Zero or Degraded Replicas
	if strings.Contains(reason, "available replicas") || strings.Contains(reason, "0 available replicas") {
		var deploy appsv1.Deployment
		deployKey := types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}
		if err := m.Client.Get(ctx, deployKey, &deploy); err != nil {
			return &module.RemediateResult{Action: "fetch coredns deployment", Success: false, Err: err}, err
		}

		targetReplicas := defaultExpectedReplicas
		deploy.Spec.Replicas = &targetReplicas

		if err := m.Client.Update(ctx, &deploy); err != nil {
			log.Error(err, "Failed to scale coredns deployment back up")
			return &module.RemediateResult{Action: "scale coredns deployment", Success: false, Err: err}, err
		}

		msg := fmt.Sprintf("Scaled CoreDNS deployment back up to %d replicas", targetReplicas)
		log.Info(msg)
		return &module.RemediateResult{Action: msg, Success: true}, nil
	}

	// Remediation 2: Fix Corrupted ConfigMap Upstream Forward Directive
	if strings.Contains(reason, "upstream resolver corrupted") || strings.Contains(reason, "forward directive") {
		var cm corev1.ConfigMap
		cmKey := types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}
		if err := m.Client.Get(ctx, cmKey, &cm); err != nil {
			return &module.RemediateResult{Action: "fetch coredns configmap", Success: false, Err: err}, err
		}

		corefile := cm.Data["Corefile"]
		// Replace any forward . <bad-target> with forward . /etc/resolv.conf
		repairedCorefile := forwardRegex.ReplaceAllString(corefile, "forward . /etc/resolv.conf")
		cm.Data["Corefile"] = repairedCorefile

		if err := m.Client.Update(ctx, &cm); err != nil {
			log.Error(err, "Failed to update coredns configmap Corefile")
			return &module.RemediateResult{Action: "repair coredns configmap", Success: false, Err: err}, err
		}

		// Rollout restart CoreDNS deployment so pods pick up the corrected ConfigMap
		var deploy appsv1.Deployment
		deployKey := types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}
		if err := m.Client.Get(ctx, deployKey, &deploy); err == nil {
			if deploy.Spec.Template.Annotations == nil {
				deploy.Spec.Template.Annotations = map[string]string{}
			}
			deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
			_ = m.Client.Update(ctx, &deploy)
		}

		msg := "Repaired CoreDNS ConfigMap forward directive to /etc/resolv.conf and initiated rolling restart"
		log.Info(msg)
		return &module.RemediateResult{Action: msg, Success: true}, nil
	}

	// Remediation 3: Fix CPU Throttling (restore baseline CPU resources)
	if strings.Contains(reason, "throttled") {
		var deploy appsv1.Deployment
		deployKey := types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}
		if err := m.Client.Get(ctx, deployKey, &deploy); err != nil {
			return &module.RemediateResult{Action: "fetch coredns deployment", Success: false, Err: err}, err
		}

		if len(deploy.Spec.Template.Spec.Containers) > 0 {
			deploy.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("70Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("170Mi"),
				},
			}
		}

		if err := m.Client.Update(ctx, &deploy); err != nil {
			log.Error(err, "Failed to restore CoreDNS CPU resources")
			return &module.RemediateResult{Action: "restore coredns resources", Success: false, Err: err}, err
		}

		msg := "Restored CoreDNS CPU resources (100m CPU limit/request) to remove throttling"
		log.Info(msg)
		return &module.RemediateResult{Action: msg, Success: true}, nil
	}

	// Remediation 4: Pods CrashLooping - restart deployment
	if strings.Contains(reason, "crashlooping") {
		var deploy appsv1.Deployment
		deployKey := types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}
		if err := m.Client.Get(ctx, deployKey, &deploy); err != nil {
			return &module.RemediateResult{Action: "fetch coredns deployment", Success: false, Err: err}, err
		}

		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
		if err := m.Client.Update(ctx, &deploy); err != nil {
			return &module.RemediateResult{Action: "restart coredns deployment", Success: false, Err: err}, err
		}

		msg := "Triggered rolling restart of CoreDNS deployment to recover pods"
		log.Info(msg)
		return &module.RemediateResult{Action: msg, Success: true}, nil
	}

	return &module.RemediateResult{
		Action:  "none",
		Success: true,
	}, nil
}

// queryPrometheusLatency queries Prometheus for CoreDNS query duration metric.
func queryPrometheusLatency(ctx context.Context) (float64, error) {
	urls := []string{
		"http://prometheus.monitoring.svc.cluster.local:9090/api/v1/query?query=rate(coredns_dns_request_duration_seconds_sum[1m])/rate(coredns_dns_request_duration_seconds_count[1m])",
		"http://127.0.0.1:30090/api/v1/query?query=rate(coredns_dns_request_duration_seconds_sum[1m])/rate(coredns_dns_request_duration_seconds_count[1m])",
	}

	httpClient := &http.Client{Timeout: 1 * time.Second}
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			continue
		}
		resp, err := httpClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		defer resp.Body.Close()

		var result struct {
			Data struct {
				Result []struct {
					Value []any `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && len(result.Data.Result) > 0 {
			if len(result.Data.Result[0].Value) >= 2 {
				valStr, ok := result.Data.Result[0].Value[1].(string)
				if ok {
					var sec float64
					if _, err := fmt.Sscanf(valStr, "%f", &sec); err == nil {
						return sec * 1000.0, nil // return ms
					}
				}
			}
		}
	}
	return 0, fmt.Errorf("prometheus query unavailable")
}
