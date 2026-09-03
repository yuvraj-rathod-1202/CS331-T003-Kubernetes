package cni

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/pkg/module"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = remediationv1alpha1.AddToScheme(s)
	return s
}

func TestCNIModule_Evaluate_Healthy(t *testing.T) {
	m := New(nil)
	ctx := context.Background()

	checkResult := &module.CheckResult{
		Signals: map[string]any{
			signalCalicoNodeUnready:  []string{},
			signalStuckPods:          []StuckPod{},
			signalIPAMUsageByPool:    map[string]float64{"10.200.0.0/28": 30.0},
			signalIPAMExhaustedPools: []string{},
			signalDisabledIPPools:    []string{},
		},
	}

	evalResult, err := m.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !evalResult.IsHealthy {
		t.Errorf("expected healthy, got unhealthy: %s", evalResult.Reason)
	}
	if evalResult.NeedsRemediation {
		t.Errorf("expected no remediation needed, got: %v", evalResult.NeedsRemediation)
	}
}

func TestCNIModule_Evaluate_CalicoNodeCrash(t *testing.T) {
	m := New(nil)
	ctx := context.Background()

	checkResult := &module.CheckResult{
		Signals: map[string]any{
			signalCalicoNodeUnready:  []string{"calico-node-worker1"},
			signalStuckPods:          []StuckPod{},
			signalIPAMUsageByPool:    map[string]float64{},
			signalIPAMExhaustedPools: []string{},
			signalDisabledIPPools:    []string{},
		},
	}

	evalResult, err := m.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evalResult.IsHealthy {
		t.Errorf("expected unhealthy when calico-node is down")
	}
	if !evalResult.NeedsRemediation {
		t.Errorf("expected remediation to be requested")
	}
	if evalResult.Severity != "critical" {
		t.Errorf("expected severity 'critical', got '%s'", evalResult.Severity)
	}
}

func TestCNIModule_Evaluate_IPAMExhaustion(t *testing.T) {
	m := New(nil)
	ctx := context.Background()

	checkResult := &module.CheckResult{
		Signals: map[string]any{
			signalCalicoNodeUnready: []string{},
			signalStuckPods: []StuckPod{
				{
					Namespace:     "default",
					Name:          "backend-stuck",
					CNIError:      "No IPs available in pools",
					IPAMExhausted: true,
				},
			},
			signalIPAMUsageByPool:    map[string]float64{"10.200.0.0/28": 100.0},
			signalIPAMExhaustedPools: []string{"10.200.0.0/28"},
			signalDisabledIPPools:    []string{},
		},
	}

	evalResult, err := m.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evalResult.IsHealthy {
		t.Errorf("expected unhealthy when IPAM is exhausted")
	}
	if !evalResult.NeedsRemediation {
		t.Errorf("expected remediation to be requested")
	}
	if evalResult.Severity != "critical" {
		t.Errorf("expected severity 'critical', got '%s'", evalResult.Severity)
	}
}

func TestCNIModule_Evaluate_DisabledIPPool(t *testing.T) {
	m := New(nil)
	ctx := context.Background()

	checkResult := &module.CheckResult{
		Signals: map[string]any{
			signalCalicoNodeUnready: []string{},
			signalStuckPods: []StuckPod{
				{
					Namespace:     "default",
					Name:          "frontend-stuck",
					CNIError:      "failed to request IPv4 addresses",
					IPAMExhausted: true,
				},
			},
			signalIPAMUsageByPool:    map[string]float64{},
			signalIPAMExhaustedPools: []string{},
			signalDisabledIPPools:    []string{"default-ipv4-ippool"},
		},
	}

	evalResult, err := m.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evalResult.IsHealthy {
		t.Errorf("expected unhealthy when IPPool is disabled with stuck pods")
	}
	if !evalResult.NeedsRemediation {
		t.Errorf("expected remediation to be requested")
	}
}

func TestCNIModule_Remediate_UnreadyCalicoPod(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	// Create unready calico-node pod
	unreadyCalicoPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "calico-node-bad",
			Namespace: "kube-system",
			Labels:    map[string]string{"k8s-app": "calico-node"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionFalse,
				},
			},
		},
	}

	// Daemonset indicating 1 desired, 0 ready
	ds := &unstructured.Unstructured{}
	ds.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "DaemonSet",
	})
	ds.SetName("calico-node")
	ds.SetNamespace("kube-system")
	_ = unstructured.SetNestedField(ds.Object, int64(1), "status", "desiredNumberScheduled")
	_ = unstructured.SetNestedField(ds.Object, int64(0), "status", "numberReady")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unreadyCalicoPod, ds).
		Build()

	m := New(fakeClient)

	evalResult := &module.EvalResult{
		IsHealthy:        false,
		NeedsRemediation: true,
		Severity:         "critical",
		Reason:           "1 calico-node pod not ready",
	}

	res, err := m.Remediate(ctx, evalResult)
	if err != nil {
		t.Fatalf("unexpected error during remediation: %v", err)
	}

	if !res.Success {
		t.Errorf("expected remediation success, got failed: %v", res.Err)
	}

	// Verify the unready pod was deleted
	remainingPod := &corev1.Pod{}
	err = fakeClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "calico-node-bad"}, remainingPod)
	if err == nil {
		t.Errorf("expected unready calico-node pod to be deleted")
	}
}

func TestCNIModule_Remediate_StuckPodEviction(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	// Stuck workload pod
	stuckPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "backend-stuck",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
		},
		Spec: corev1.PodSpec{NodeName: "worker-1"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
					},
				},
			},
		},
	}

	// Warning event on the stuck pod
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend-stuck-cni-err",
			Namespace: "default",
		},
		InvolvedObject: corev1.ObjectReference{
			Name:      "backend-stuck",
			Namespace: "default",
		},
		Reason:  "FailedCreatePodSandBox",
		Message: "plugin type=\"calico\" failed (add): No IPs available in pools",
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(stuckPod, event).
		Build()

	m := New(fakeClient)

	evalResult := &module.EvalResult{
		IsHealthy:        false,
		NeedsRemediation: true,
		Severity:         "critical",
		Reason:           "stuck pods with IPAM exhaustion",
	}

	res, err := m.Remediate(ctx, evalResult)
	if err != nil {
		t.Fatalf("unexpected error during remediation: %v", err)
	}

	if !res.Success {
		t.Errorf("expected remediation to succeed: %v", res.Err)
	}

	// Verify the stuck workload pod was deleted
	checkPod := &corev1.Pod{}
	err = fakeClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "backend-stuck"}, checkPod)
	if err == nil {
		t.Errorf("expected stuck pod to be evicted")
	}
}
