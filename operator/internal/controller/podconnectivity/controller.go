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

package podconnectivity

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/pkg/module"
)

const (
	defaultNamespace           = "default"
	anchorGateway              = "gateway"
	severityCritical           = "critical"
	severityWarning            = "warning"
	severityInfo               = "info"
	defaultCalicoDaemonSetName = "calico-node"
	kubeSystemNamespace        = "kube-system"
	networkDegradedTaintKey    = "network-degraded"
	actionNodeIsolation        = "node_isolation"
	actionCNIRestart           = "cni_restart"
	componentCNI               = "CNI"
)

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch

// PodConnectivityModule implements the module.Module interface for pod connectivity probing.
type PodConnectivityModule struct {
	// Client is the Kubernetes API client for interacting with cluster resources.
	Client client.Client
	// Config is the REST config needed for pod exec
	Config *rest.Config
	// Clientset is the standard kubernetes client needed for pod exec
	Clientset *kubernetes.Clientset
	// Remediator manages progressive recovery actions (CNI restart, taint, drain)
	Remediator *Remediator

	consecutiveFailures int
	failureThreshold    int
	targetNamespace     string
	latestDecision      *TriangulationDecision
	activePolicy        *remediationv1alpha1.RemediationPolicySpec
}

// New creates a new PodConnectivityModule instance.
func New(c client.Client, config *rest.Config) (*PodConnectivityModule, error) {
	var clientset *kubernetes.Clientset
	if config != nil {
		var err error
		clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes clientset for pod connectivity: %w", err)
		}
	}
	return &PodConnectivityModule{
		Client:     c,
		Config:     config,
		Clientset:  clientset,
		Remediator: NewRemediator(c),
	}, nil
}

// Name returns the module name.
func (m *PodConnectivityModule) Name() string {
	return "podconnectivity"
}

// Check gathers raw health signals for pod-to-pod connectivity using Pingmesh O(N) ring
// and local CNI canary probing.
func (m *PodConnectivityModule) Check(ctx context.Context, spec *remediationv1alpha1.NetworkRemediationSpec) (*module.CheckResult, error) {

	m.targetNamespace = spec.PodConnectivity.TargetNamespace
	if m.targetNamespace == "" {
		m.targetNamespace = defaultNamespace
	}

	m.activePolicy = &spec.PodConnectivity.RemediationPolicy
	m.failureThreshold = spec.PodConnectivity.EvaluatePolicy.ConsecutiveFailureThreshold
	if m.failureThreshold <= 0 {
		m.failureThreshold = 2
	}

	nodeList := &corev1.NodeList{}
	if err := m.Client.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list cluster nodes: %w", err)
	}

	if len(nodeList.Items) == 0 {
		return &module.CheckResult{
			Signals: map[string]any{"status": "no_nodes"},
		}, nil
	}

	ring := BuildRingTopology(nodeList.Items)

	podList := &corev1.PodList{}
	if err := m.Client.List(ctx, podList, client.InNamespace(m.targetNamespace)); err != nil {
		return nil, fmt.Errorf("failed to list pods in %s: %w", m.targetNamespace, err)
	}

	// Map pods to their hosting nodes
	nodePodMap := make(map[string]*corev1.Pod)
	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "" {
			_, exists := nodePodMap[p.Spec.NodeName]
			if !exists || strings.Contains(p.Name, "dns-checker") || strings.Contains(p.Name, "probe") {
				nodePodMap[p.Spec.NodeName] = p
			}
		}
	}

	localProbes := make(map[string]LocalProbeResult)
	var ringProbes []EdgeProbeResult
	var anchorProbes []AnchorProbeResult

	targetGateway := "8.8.8.8"
	if len(spec.PodConnectivity.CheckPolicy.Anchors) > 0 {
		targetGateway = spec.PodConnectivity.CheckPolicy.Anchors[0].Address
	}

	// 1. Intra-Node Local CNI canary checks (O(1) per node)
	for _, node := range ring.Nodes {
		pod, hasPod := nodePodMap[node]
		if !hasPod {
			// No canary pod on this node yet
			localProbes[node] = LocalProbeResult{NodeName: node, Success: true}
			continue
		}

		// Verify loopback / local CNI veth interface
		success := m.pingIP(ctx, pod, pod.Status.PodIP)
		localProbes[node] = LocalProbeResult{
			NodeName: node,
			Success:  success,
		}

		// Anchor check for this node
		anchorSuccess := m.pingIP(ctx, pod, targetGateway)
		anchorProbes = append(anchorProbes, AnchorProbeResult{
			NodeName:   node,
			AnchorName: anchorGateway,
			Success:    anchorSuccess,
		})
	}

	// 2. Inter-Node Ring Probes (O(N))
	for _, edge := range ring.Edges {
		srcPod, srcOk := nodePodMap[edge.SourceNode]
		dstPod, dstOk := nodePodMap[edge.TargetNode]

		if !srcOk || !dstOk {
			// Skip edge if pods are not scheduled yet
			continue
		}

		edgeSuccess := m.pingIP(ctx, srcPod, dstPod.Status.PodIP)
		ringProbes = append(ringProbes, EdgeProbeResult{
			SourceNode: edge.SourceNode,
			TargetNode: edge.TargetNode,
			Success:    edgeSuccess,
		})
	}

	// 3. Triangulate gathered signals
	decision := Triangulate(localProbes, ringProbes, anchorProbes)
	m.latestDecision = &decision

	return &module.CheckResult{
		Signals: map[string]any{
			"status":            string(decision.FailureType),
			"isHealthy":         decision.IsHealthy,
			"faultyNode":        decision.FaultyNode,
			"faultyComponent":   decision.FaultyComponent,
			"recommendedAction": decision.RecommendedAction,
			"reason":            decision.Reason,
			"severity":          decision.Severity,
		},
	}, nil
}

// pingIP executes an ICMP ping from the pod to targetIP.
func (m *PodConnectivityModule) pingIP(ctx context.Context, pod *corev1.Pod, targetIP string) bool {
	log := logf.FromContext(ctx).WithName("podconnectivity")

	if m.Clientset == nil || m.Config == nil {
		// Allows seamless mocking in unit tests while logging so misconfiguration is visible
		log.Info("Kubernetes Clientset or Config is nil; bypassing exec ping probe (mock mode enabled)", "pod", pod.Name, "targetIP", targetIP)
		return true
	}

	req := m.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"sh", "-c", fmt.Sprintf("ping -c 1 -W 1 %s || nc -z -w 1 %s 80 || wget -q -O- --timeout=1 %s:80 >/dev/null 2>&1", targetIP, targetIP, targetIP)},
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(m.Config, "POST", req.URL())
	if err != nil {
		log.Error(err, "Failed to create SPDY executor for pod ping probe", "pod", pod.Name, "targetIP", targetIP)
		return false
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		log.V(1).Info("Pod ping probe execution failed", "pod", pod.Name, "targetIP", targetIP, "stderr", stderr.String(), "error", err)
		return false
	}

	return true
}

// Evaluate analyzes the connectivity check results to determine if there is an issue.
func (m *PodConnectivityModule) Evaluate(ctx context.Context, checkResult *module.CheckResult) (*module.EvalResult, error) {
	if checkResult.Err != nil {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: false,
			Reason:           "Check error: " + checkResult.Err.Error(),
			Severity:         severityWarning,
		}, nil
	}

	isHealthy, _ := checkResult.Signals["isHealthy"].(bool)
	status, _ := checkResult.Signals["status"].(string)

	if isHealthy || status == "healthy" || status == "insufficient_pods" || status == "no_nodes" {
		m.consecutiveFailures = 0
		if m.latestDecision != nil && m.latestDecision.FaultyNode != "" {
			m.Remediator.ResetNodeHistory(m.latestDecision.FaultyNode)
		}
		return &module.EvalResult{
			IsHealthy:        true,
			NeedsRemediation: false,
			Reason:           "Pod-to-pod network connectivity is healthy",
			Severity:         severityInfo,
		}, nil
	}

	m.consecutiveFailures++
	reason, _ := checkResult.Signals["reason"].(string)
	severity, _ := checkResult.Signals["severity"].(string)
	if severity == "" {
		severity = severityCritical
	}

	threshold := m.failureThreshold
	if threshold <= 0 {
		threshold = 2
	}

	// Trigger remediation when consecutive failures meet threshold
	return &module.EvalResult{
		IsHealthy:        false,
		NeedsRemediation: m.consecutiveFailures >= threshold,
		Reason:           reason,
		Severity:         severity,
	}, nil
}

// Remediate executes progressive hierarchical recovery actions (CNI restart, taint, drain).
func (m *PodConnectivityModule) Remediate(ctx context.Context, evalResult *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName("podconnectivity")

	if m.latestDecision == nil || m.latestDecision.FaultyNode == "" {
		return &module.RemediateResult{
			Success: false,
			Action:  "No faulty node identified for remediation",
		}, nil
	}

	log.Info("Executing progressive automated remediation",
		"faultyNode", m.latestDecision.FaultyNode,
		"failureType", m.latestDecision.FailureType,
		"recommendedAction", m.latestDecision.RecommendedAction)

	actionMsg, success, err := m.Remediator.ExecuteRemediation(ctx, m.latestDecision, m.activePolicy)
	if err != nil {
		log.Error(err, "Remediation failed", "action", actionMsg)
		return &module.RemediateResult{
			Action:  actionMsg,
			Success: false,
			Err:     err,
		}, nil
	}

	m.consecutiveFailures = 0
	return &module.RemediateResult{
		Action:  actionMsg,
		Success: success,
	}, nil
}
