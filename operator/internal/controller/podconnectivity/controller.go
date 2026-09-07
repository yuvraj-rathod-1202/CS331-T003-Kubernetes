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

// PodConnectivityModule implements the module.Module interface for pod connectivity probing.
type PodConnectivityModule struct {
	// Client is the Kubernetes API client for interacting with cluster resources.
	Client client.Client
	// Config is the REST config needed for pod exec
	Config *rest.Config
	// Clientset is the standard kubernetes client needed for pod exec
	Clientset *kubernetes.Clientset

	consecutiveFailures int
	targetNamespace     string
	sourcePodLabel      string
	faultyPodName       string
	faultyPodType       string // "App" or "CNI"
	faultyNode          string
}

// New creates a new PodConnectivityModule instance.
func New(c client.Client, config *rest.Config) *PodConnectivityModule {
	clientset, _ := kubernetes.NewForConfig(config)
	return &PodConnectivityModule{
		Client:    c,
		Config:    config,
		Clientset: clientset,
	}
}

// Name returns the module name.
func (m *PodConnectivityModule) Name() string {
	return "podconnectivity"
}

// parseLabels converts "key=value" to a map
func parseLabels(selector string) map[string]string {
	labels := make(map[string]string)
	if selector == "" {
		return labels
	}
	parts := strings.Split(selector, "=")
	if len(parts) == 2 {
		labels[parts[0]] = parts[1]
	}
	return labels
}

// Check gathers raw health signals for pod-to-pod connectivity using Triangulation.
func (m *PodConnectivityModule) Check(ctx context.Context, spec *remediationv1alpha1.NetworkRemediationSpec) (*module.CheckResult, error) {
	log := logf.FromContext(ctx).WithName("podconnectivity")

	m.targetNamespace = spec.PodConnectivity.TargetNamespace
	if m.targetNamespace == "" {
		m.targetNamespace = "default"
	}
	m.sourcePodLabel = spec.PodConnectivity.SourcePodLabel

	targetIP := spec.PodConnectivity.TargetIP
	if targetIP == "" {
		targetIP = "8.8.8.8"
	}

	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(m.targetNamespace),
		client.MatchingLabels(parseLabels(m.sourcePodLabel)),
	}
	if err := m.Client.List(ctx, podList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(podList.Items) < 2 {
		return &module.CheckResult{
			Signals: map[string]any{"status": "insufficient_pods"},
		}, nil
	}

	var sourcePod, targetPod *corev1.Pod
	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Labels["crash-source"] == "true" {
			sourcePod = p
		}
		if p.Labels["crash-target"] == "true" {
			targetPod = p
		}
	}

	if sourcePod == nil || targetPod == nil {
		return &module.CheckResult{
			Signals: map[string]any{"status": "healthy"},
		}, nil
	}

	log.Info("Detected test edge", "source", sourcePod.Name, "target", targetPod.Name)

	// Step 1: Primary Test (A pings B)
	primarySuccess := m.pingIP(ctx, sourcePod, targetPod.Status.PodIP)
	if primarySuccess {
		return &module.CheckResult{
			Signals: map[string]any{"status": "healthy"},
		}, nil
	}

	log.Info("Primary ping failed. Running Triangulation Diagnostic...")

	// Step 2: Diagnostic A (A pings targetIP)
	diagASuccess := m.pingIP(ctx, sourcePod, targetIP)

	// Step 3: Diagnostic B (B pings targetIP)
	diagBSuccess := m.pingIP(ctx, targetPod, targetIP)

	faultyPod := ""
	faultyPodType := ""
	faultyNode := ""

	if !diagASuccess {
		log.Info("Diagnostic A failed. Source pod network is broken.", "pod", sourcePod.Name)
		faultyPod = sourcePod.Name
		faultyPodType = "App"
		faultyNode = sourcePod.Spec.NodeName
	} else if !diagBSuccess {
		log.Info("Diagnostic B failed. Target pod network is broken.", "pod", targetPod.Name)
		faultyPod = targetPod.Name
		faultyPodType = "App"
		faultyNode = targetPod.Spec.NodeName
	} else {
		log.Info("Both pods can reach internet. Running Reverse Ping (Triangulation C)...")

		reverseSuccess := true
		if targetPod.Labels["simulate-tunnel-crash"] == "true" {
			log.Info("Simulation flag detected. Faking Reverse Ping failure for demo purposes.")
			reverseSuccess = false
		} else {
			reverseSuccess = m.pingIP(ctx, targetPod, sourcePod.Status.PodIP)
		}

		if reverseSuccess {
			log.Info("Reverse Ping SUCCESS. Tunnel is healthy, Target Pod ingress is broken.", "target", targetPod.Name)
			faultyPod = targetPod.Name
			faultyPodType = "App"
			faultyNode = targetPod.Spec.NodeName
		} else {
			log.Info("Reverse Ping FAILED. Cross-node tunnel is broken!", "targetNode", targetPod.Spec.NodeName)
			faultyPod = targetPod.Name
			faultyPodType = "CNI"
			faultyNode = targetPod.Spec.NodeName
		}
	}

	return &module.CheckResult{
		Signals: map[string]any{
			"status":        "unhealthy",
			"faultyPod":     faultyPod,
			"faultyPodType": faultyPodType,
			"faultyNode":    faultyNode,
		},
	}, nil
}

func (m *PodConnectivityModule) pingIP(ctx context.Context, pod *corev1.Pod, targetIP string) bool {
	req := m.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"ping", "-c", "1", "-W", "1", targetIP},
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(m.Config, "POST", req.URL())
	if err != nil {
		return false
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return err == nil
}

// Evaluate analyzes the connectivity check results to determine if there is an issue.
func (m *PodConnectivityModule) Evaluate(ctx context.Context, checkResult *module.CheckResult) (*module.EvalResult, error) {
	if checkResult.Err != nil {
		return &module.EvalResult{
			IsHealthy:        false,
			NeedsRemediation: false,
			Reason:           "Check error: " + checkResult.Err.Error(),
			Severity:         "warning",
		}, nil
	}

	status, _ := checkResult.Signals["status"].(string)
	if status == "healthy" || status == "insufficient_pods" {
		m.consecutiveFailures = 0
		m.faultyPodName = ""
		return &module.EvalResult{
			IsHealthy:        true,
			NeedsRemediation: false,
			Reason:           "Pod connectivity is healthy",
			Severity:         "info",
		}, nil
	}

	m.consecutiveFailures++
	faultyPod, _ := checkResult.Signals["faultyPod"].(string)
	faultyPodType, _ := checkResult.Signals["faultyPodType"].(string)
	faultyNode, _ := checkResult.Signals["faultyNode"].(string)

	m.faultyPodName = faultyPod
	m.faultyPodType = faultyPodType
	m.faultyNode = faultyNode

	return &module.EvalResult{
		IsHealthy:        false,
		NeedsRemediation: m.consecutiveFailures >= 1,
		Reason:           fmt.Sprintf("Triangulation identified faulty %s on node %s", faultyPodType, faultyNode),
		Severity:         "critical",
	}, nil
}

// Remediate executes corrective actions for connectivity failures.
func (m *PodConnectivityModule) Remediate(ctx context.Context, evalResult *module.EvalResult) (*module.RemediateResult, error) {
	log := logf.FromContext(ctx).WithName("podconnectivity")

	if m.faultyPodName == "" {
		return &module.RemediateResult{Success: false, Action: "No faulty pod identified"}, nil
	}

	log.Info("Remediating by restarting faulty component", "pod", m.faultyPodName, "type", m.faultyPodType)

	var actionMsg string

	if m.faultyPodType == "CNI" {
		log.Info("Remediating by restarting CNI Agent on Node", "node", m.faultyNode)
		podList := &corev1.PodList{}
		if err := m.Client.List(ctx, podList); err == nil {
			for _, p := range podList.Items {
				if p.Spec.NodeName == m.faultyNode && (strings.Contains(p.Name, "calico-node") || strings.Contains(p.Name, "kindnet")) {
					log.Info("Found CNI pod, deleting it", "pod", p.Name, "namespace", p.Namespace)
					m.Client.Delete(ctx, &p)
					break
				}
			}
		}
		actionMsg = fmt.Sprintf("Deleted CNI agent on Node %s to force tunnel recreation", m.faultyNode)
	} else {
		pod := &corev1.Pod{}
		err := m.Client.Get(ctx, client.ObjectKey{Name: m.faultyPodName, Namespace: m.targetNamespace}, pod)
		if err == nil {
			if err := m.Client.Delete(ctx, pod); err != nil {
				log.Error(err, "Failed to delete faulty app pod", "pod", m.faultyPodName)
				return nil, err
			}
		}
		actionMsg = fmt.Sprintf("Deleted faulty application pod %s to force interface recreation", m.faultyPodName)
	}

	m.consecutiveFailures = 0
	m.faultyPodName = ""
	m.faultyPodType = ""
	m.faultyNode = ""

	return &module.RemediateResult{
		Action:  actionMsg,
		Success: true,
	}, nil
}
