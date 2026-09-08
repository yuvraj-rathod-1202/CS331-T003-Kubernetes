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
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

func TestBuildRingTopology(t *testing.T) {
	nodes := []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-c"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.3"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-b"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.2"}},
			},
		},
	}

	ring := BuildRingTopology(nodes)

	// Verify lexicographical order
	expectedNodes := []string{"node-a", "node-b", "node-c"}
	for i, name := range ring.Nodes {
		if name != expectedNodes[i] {
			t.Errorf("expected node %d to be %s, got %s", i, expectedNodes[i], name)
		}
	}

	// 3 nodes with bidirectional edges = 6 edges total
	if len(ring.Edges) != 6 {
		t.Fatalf("expected 6 ring edges (3 forward, 3 reverse), got %d", len(ring.Edges))
	}

	// Forward edge: node-a -> node-b
	if ring.Edges[0].SourceNode != "node-a" || ring.Edges[0].TargetNode != "node-b" {
		t.Errorf("expected edge 0 to be node-a -> node-b, got %s -> %s", ring.Edges[0].SourceNode, ring.Edges[0].TargetNode)
	}
}

func TestTriangulate_Tier1_LocalCNIFailure(t *testing.T) {
	localProbes := map[string]LocalProbeResult{
		"node-a": {NodeName: "node-a", Success: false}, // Local canary fails
		"node-b": {NodeName: "node-b", Success: true},
	}
	ringProbes := []EdgeProbeResult{
		{SourceNode: "node-a", TargetNode: "node-b", Success: true}, // External works!
	}
	anchorProbes := []AnchorProbeResult{
		{NodeName: "node-a", AnchorName: "gateway", Success: true},
	}

	decision := Triangulate(localProbes, ringProbes, anchorProbes)

	if decision.IsHealthy {
		t.Errorf("expected decision to be unhealthy")
	}
	if decision.FailureType != FailureTypeLocalCNI {
		t.Errorf("expected FailureType %s, got %s", FailureTypeLocalCNI, decision.FailureType)
	}
	if decision.FaultyNode != "node-a" {
		t.Errorf("expected faulty node to be node-a, got %s", decision.FaultyNode)
	}
	if decision.RecommendedAction != "cni_restart" {
		t.Errorf("expected cni_restart action, got %s", decision.RecommendedAction)
	}
}

func TestTriangulate_Tier2_TargetIngressDead(t *testing.T) {
	localProbes := map[string]LocalProbeResult{
		"node-a": {NodeName: "node-a", Success: true},
		"node-b": {NodeName: "node-b", Success: true},
		"node-c": {NodeName: "node-c", Success: true},
	}
	// All nodes fail to reach node-b
	ringProbes := []EdgeProbeResult{
		{SourceNode: "node-a", TargetNode: "node-b", Success: false},
		{SourceNode: "node-c", TargetNode: "node-b", Success: false},
		{SourceNode: "node-a", TargetNode: "node-c", Success: true},
	}
	anchorProbes := []AnchorProbeResult{
		{NodeName: "node-a", AnchorName: "gateway", Success: true},
		{NodeName: "node-b", AnchorName: "gateway", Success: true},
	}

	decision := Triangulate(localProbes, ringProbes, anchorProbes)

	if decision.FailureType != FailureTypeNodeIngress {
		t.Errorf("expected FailureType %s, got %s", FailureTypeNodeIngress, decision.FailureType)
	}
	if decision.FaultyNode != "node-b" {
		t.Errorf("expected faulty node node-b, got %s", decision.FaultyNode)
	}
}

func TestTriangulate_Tier3_AnchorCorroboration(t *testing.T) {
	localProbes := map[string]LocalProbeResult{
		"node-a": {NodeName: "node-a", Success: true},
		"node-b": {NodeName: "node-b", Success: true},
	}
	// node-a cannot reach node-b
	ringProbes := []EdgeProbeResult{
		{SourceNode: "node-a", TargetNode: "node-b", Success: false},
	}
	// node-a CAN reach gateway (proving physical NIC is alive)
	anchorProbes := []AnchorProbeResult{
		{NodeName: "node-a", AnchorName: "gateway", Success: true},
	}

	decision := Triangulate(localProbes, ringProbes, anchorProbes)

	// Since Node A can reach gateway, this isolates an overlay tunnel crash
	if decision.FailureType != FailureTypeTunnelCrash && decision.FailureType != FailureTypeNodeIngress {
		t.Errorf("expected TunnelCrash or NodeIngress, got %s", decision.FailureType)
	}
	if decision.FaultyNode != "node-a" && decision.FaultyNode != "node-b" {
		t.Errorf("expected faulty node node-a or node-b, got %s", decision.FaultyNode)
	}
}

func TestRemediator_Tier1_CNIRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cniPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "calico-node-12345",
			Namespace: "kube-system",
		},
		Spec: corev1.PodSpec{
			NodeName: "node-2",
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cniPod).Build()
	remediator := NewRemediator(client)

	decision := &TriangulationDecision{
		FaultyNode:        "node-2",
		RecommendedAction: "cni_restart",
	}

	policy := &remediationv1alpha1.RemediationPolicySpec{
		CNISubsystem: remediationv1alpha1.CNISubsystemRemediation{
			Enabled:            true,
			DaemonSetName:      "calico-node",
			Namespace:          "kube-system",
			MaxRestartAttempts: 2,
		},
	}

	actionMsg, success, err := remediator.ExecuteRemediation(context.Background(), decision, policy)
	if err != nil {
		t.Fatalf("unexpected error during remediation: %v", err)
	}
	if !success {
		t.Fatalf("expected remediation to succeed")
	}

	if remediator.RestartAttempts["node-2"] != 1 {
		t.Errorf("expected restart attempts to be 1, got %d", remediator.RestartAttempts["node-2"])
	}

	t.Logf("Action message: %s", actionMsg)
}

func TestRemediator_Tier2_NodeIsolation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-2",
		},
		Spec: corev1.NodeSpec{
			Unschedulable: false,
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
	remediator := NewRemediator(client)

	decision := &TriangulationDecision{
		FaultyNode:        "node-2",
		RecommendedAction: "node_isolation",
	}

	policy := &remediationv1alpha1.RemediationPolicySpec{
		NodeIsolation: remediationv1alpha1.NodeIsolationRemediation{
			TaintNode:   true,
			TaintKey:    "network-degraded",
			TaintValue:  "true",
			TaintEffect: "NoSchedule",
			CordonNode:  true,
		},
	}

	_, success, err := remediator.ExecuteRemediation(context.Background(), decision, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !success {
		t.Fatalf("expected success")
	}

	updatedNode := &corev1.Node{}
	_ = client.Get(context.Background(), types.NamespacedName{Name: "node-2"}, updatedNode)

	if !updatedNode.Spec.Unschedulable {
		t.Errorf("expected node to be cordoned (unschedulable = true)")
	}

	hasTaint := false
	for _, taint := range updatedNode.Spec.Taints {
		if taint.Key == "network-degraded" && taint.Effect == corev1.TaintEffectNoSchedule {
			hasTaint = true
			break
		}
	}
	if !hasTaint {
		t.Errorf("expected node to have taint network-degraded=true:NoSchedule")
	}
}

func TestRemediator_Tier3_EscalationToDrain(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
	}
	appPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "frontend-app",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: "node-2",
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, appPod).Build()
	remediator := NewRemediator(client)

	// Simulate that node-2 has already failed 2 CNI restarts
	remediator.RestartAttempts["node-2"] = 2

	decision := &TriangulationDecision{
		FaultyNode:        "node-2",
		RecommendedAction: "cni_restart",
	}

	policy := &remediationv1alpha1.RemediationPolicySpec{
		CNISubsystem: remediationv1alpha1.CNISubsystemRemediation{
			MaxRestartAttempts: 2,
		},
		NodeDrain: remediationv1alpha1.NodeDrainRemediation{
			Enabled:                     true,
			EscalateAfterFailedRestarts: 2,
		},
	}

	actionMsg, success, err := remediator.ExecuteRemediation(context.Background(), decision, policy)
	if err != nil {
		t.Fatalf("unexpected error during drain escalation: %v", err)
	}
	if !success {
		t.Fatalf("expected drain escalation to succeed")
	}

	t.Logf("Drain escalation outcome: %s", actionMsg)

	// Check that application pod was deleted/evicted
	podList := &corev1.PodList{}
	_ = client.List(context.Background(), podList)
	if len(podList.Items) != 0 {
		t.Errorf("expected application pod to be evicted, found %d pods", len(podList.Items))
	}
}

func TestPodConnectivityModule_FullPipeline(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = remediationv1alpha1.AddToScheme(scheme)

	node1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
		},
	}
	node2 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.2"}},
		},
	}
	cniPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "calico-node-abc",
			Namespace: "kube-system",
		},
		Spec: corev1.PodSpec{
			NodeName: "node-2",
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node1, node2, cniPod).Build()
	module := New(client, nil)

	spec := &remediationv1alpha1.NetworkRemediationSpec{
		PodConnectivity: remediationv1alpha1.PodConnectivitySpec{
			Enabled:         true,
			TargetNamespace: "default",
			CheckPolicy: remediationv1alpha1.CheckPolicySpec{
				Topology: "ring",
			},
			RemediationPolicy: remediationv1alpha1.RemediationPolicySpec{
				AutoRemediationEnabled: true,
				CNISubsystem: remediationv1alpha1.CNISubsystemRemediation{
					Enabled:       true,
					DaemonSetName: "calico-node",
					Namespace:     "kube-system",
				},
			},
		},
	}

	// 1. Check phase
	checkRes, err := module.Check(context.Background(), spec)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if checkRes == nil || checkRes.Signals == nil {
		t.Fatalf("expected valid CheckResult")
	}

	// 2. Evaluate phase
	evalRes, err := module.Evaluate(context.Background(), checkRes)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if evalRes == nil {
		t.Fatalf("expected valid EvalResult")
	}

	// 3. Remediate phase if needed
	if evalRes.NeedsRemediation {
		remRes, err := module.Remediate(context.Background(), evalRes)
		if err != nil {
			t.Fatalf("Remediate failed: %v", err)
		}
		if remRes == nil || !remRes.Success {
			t.Fatalf("expected successful remediation")
		}
	}
}
