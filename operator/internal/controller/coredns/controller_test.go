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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = remediationv1alpha1.AddToScheme(scheme)
	return scheme
}

func TestCoreDNSModule_Name(t *testing.T) {
	mod := New(nil)
	if mod.Name() != corednsName {
		t.Fatalf("expected module name 'coredns', got %s", mod.Name())
	}
}

func TestCoreDNSModule_CheckAndEvaluate_Healthy(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	replicas := int32(2)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: corednsNamespace,
			Name:      corednsName,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: corednsName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100m"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          2,
			ReadyReplicas:     2,
			AvailableReplicas: 2,
		},
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: corednsNamespace,
			Name:      corednsName,
		},
		Data: map[string]string{
			corefileName: ".:53 {\n    forward . /etc/resolv.conf\n}\n",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy, cm).Build()
	mod := New(fakeClient)

	spec := &remediationv1alpha1.NetworkRemediationSpec{
		CoreDNS: remediationv1alpha1.CoreDNSSpec{
			Enabled:          true,
			ExpectedReplicas: 2,
		},
	}

	checkResult, err := mod.Check(ctx, spec)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	evalResult, err := mod.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	if !evalResult.IsHealthy {
		t.Fatalf("expected healthy, got unhealthy with reason: %s", evalResult.Reason)
	}
	if evalResult.NeedsRemediation {
		t.Fatalf("expected NeedsRemediation=false")
	}
}

func TestCoreDNSModule_ScaleToZero_DetectionAndRemediation(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	zeroReplicas := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: corednsNamespace,
			Name:      corednsName,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zeroReplicas,
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          0,
			ReadyReplicas:     0,
			AvailableReplicas: 0,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).Build()
	mod := New(fakeClient)

	spec := &remediationv1alpha1.NetworkRemediationSpec{
		CoreDNS: remediationv1alpha1.CoreDNSSpec{
			Enabled:          true,
			ExpectedReplicas: 2,
			AutoHealReplicas: true,
		},
	}

	checkResult, err := mod.Check(ctx, spec)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	evalResult, err := mod.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	if evalResult.IsHealthy {
		t.Fatalf("expected unhealthy for 0 replicas")
	}
	if !evalResult.NeedsRemediation {
		t.Fatalf("expected NeedsRemediation=true")
	}
	if evalResult.Severity != severityCritical {
		t.Fatalf("expected severity critical, got %s", evalResult.Severity)
	}

	// Remediate
	remedResult, err := mod.Remediate(ctx, evalResult)
	if err != nil {
		t.Fatalf("Remediate failed: %v", err)
	}
	if !remedResult.Success {
		t.Fatalf("expected remediation to succeed")
	}

	// Verify deployment was scaled back up in fake client
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}, &updatedDeploy); err != nil {
		t.Fatalf("failed to fetch updated deployment: %v", err)
	}
	if updatedDeploy.Spec.Replicas == nil || *updatedDeploy.Spec.Replicas != 2 {
		t.Fatalf("expected replicas to be scaled to 2, got %v", updatedDeploy.Spec.Replicas)
	}
}

func TestCoreDNSModule_CorruptedUpstreamConfigMap_DetectionAndRemediation(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	twoReplicas := int32(2)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: corednsNamespace,
			Name:      corednsName,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &twoReplicas,
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          2,
			ReadyReplicas:     2,
			AvailableReplicas: 2,
		},
	}

	// Bad configmap pointing to unreachable test-net IP 192.0.2.1
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: corednsNamespace,
			Name:      corednsName,
		},
		Data: map[string]string{
			corefileName: ".:53 {\n    forward . 192.0.2.1 {\n       max_concurrent 1000\n    }\n}\n",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy, cm).Build()
	mod := New(fakeClient)

	spec := &remediationv1alpha1.NetworkRemediationSpec{
		CoreDNS: remediationv1alpha1.CoreDNSSpec{
			Enabled:           true,
			ExpectedReplicas:  2,
			AutoHealConfigMap: true,
		},
	}

	checkResult, err := mod.Check(ctx, spec)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	evalResult, err := mod.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	if evalResult.IsHealthy {
		t.Fatalf("expected unhealthy for corrupted upstream")
	}
	if !evalResult.NeedsRemediation {
		t.Fatalf("expected NeedsRemediation=true")
	}

	// Remediate
	remedResult, err := mod.Remediate(ctx, evalResult)
	if err != nil {
		t.Fatalf("Remediate failed: %v", err)
	}
	if !remedResult.Success {
		t.Fatalf("expected remediation to succeed")
	}

	// Verify ConfigMap was restored
	var updatedCM corev1.ConfigMap
	if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}, &updatedCM); err != nil {
		t.Fatalf("failed to fetch updated configmap: %v", err)
	}
	if !forwardRegex.MatchString(updatedCM.Data[corefileName]) {
		t.Fatalf("repaired Corefile missing forward directive")
	}
	match := forwardRegex.FindStringSubmatch(updatedCM.Data[corefileName])
	if len(match) < 2 || match[1] != "/etc/resolv.conf" {
		t.Fatalf("expected forward target /etc/resolv.conf, got %v", match)
	}
}

func TestCoreDNSModule_CPUThrottled_DetectionAndRemediation(t *testing.T) {
	scheme := newTestScheme()
	ctx := context.Background()

	twoReplicas := int32(2)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: corednsNamespace,
			Name:      corednsName,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &twoReplicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: corednsName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("1m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("1m"),
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          2,
			ReadyReplicas:     2,
			AvailableReplicas: 2,
		},
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: corednsNamespace,
			Name:      corednsName,
		},
		Data: map[string]string{
			corefileName: ".:53 {\n    forward . /etc/resolv.conf\n}\n",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy, cm).Build()
	mod := New(fakeClient)

	spec := &remediationv1alpha1.NetworkRemediationSpec{
		CoreDNS: remediationv1alpha1.CoreDNSSpec{
			Enabled:          true,
			ExpectedReplicas: 2,
		},
	}

	checkResult, err := mod.Check(ctx, spec)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	evalResult, err := mod.Evaluate(ctx, checkResult)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	if evalResult.IsHealthy {
		t.Fatalf("expected unhealthy for CPU throttling")
	}

	// Remediate
	remedResult, err := mod.Remediate(ctx, evalResult)
	if err != nil {
		t.Fatalf("Remediate failed: %v", err)
	}
	if !remedResult.Success {
		t.Fatalf("expected remediation to succeed")
	}

	// Verify CPU resources were restored
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: corednsNamespace, Name: corednsName}, &updatedDeploy); err != nil {
		t.Fatalf("failed to fetch updated deployment: %v", err)
	}
	cpuReq := updatedDeploy.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	if cpuReq.MilliValue() != 100 {
		t.Fatalf("expected CPU request restored to 100m, got %v", cpuReq.String())
	}
}
