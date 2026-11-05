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

// Package failover moves Service traffic for pods on a broken node to their backup IPs.
//
// Kubernetes only knows a pod's primary IP, so for each EndpointSlice holding pods on
// the node we add a "shadow" slice listing their backup IPs, and turn off a readiness
// gate on those pods so Kubernetes itself stops sending traffic to the primary IP.
// A readiness gate is used because editing Ready in an EndpointSlice directly does
// not last: the EndpointSlice controller rewrites it within about a second.
//
// All state lives in the cluster (shadow slices and pod conditions), so an operator
// restart loses nothing.
package failover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"slices"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// ReadinessGate is the pod condition this package controls. A workload joins
	// failover by listing it under spec.readinessGates.
	ReadinessGate corev1.PodConditionType = "cniresilience.cn-operator/primary-path"

	// shadowLabelKey identifies EndpointSlices managed by this controller.
	shadowLabelKey = "cniresilience.cn-operator/shadow"
	shadowLabelVal = "true"

	// shadowNodeLabelKey records which node a shadow slice is for.
	shadowNodeLabelKey = "cniresilience.cn-operator/node"

	serviceNameLabel = "kubernetes.io/service-name"
	managedByLabel   = "endpointslice.kubernetes.io/managed-by"
	managedByValue   = "cniresilience.cn-operator"

	multusNetworkStatusAnnotation = "k8s.v1.cni.cncf.io/network-status"
)

// Manager handles traffic failover between primary and backup CNI paths.
type Manager struct {
	client client.Client
	// backupNet is the backup CNI's address space. The network-status annotation
	// can be written by anyone who can edit a pod, so a backup IP outside this range
	// is ignored rather than put into a Service. Nil means unknown: no check.
	backupNet *net.IPNet
}

// SetBackupNetwork sets the backup CNI's address space from the CR. An empty or
// unparseable value turns the check off.
func (m *Manager) SetBackupNetwork(cidr string) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		network = nil
	}
	m.backupNet = network
}

// NewManager creates a new failover manager.
func NewManager(c client.Client) *Manager {
	return &Manager{client: c}
}

// FailedOverNodes returns the nodes that currently have shadow slices.
func (m *Manager) FailedOverNodes(ctx context.Context) (map[string]bool, error) {
	var shadows discoveryv1.EndpointSliceList
	if err := m.client.List(ctx, &shadows, client.MatchingLabels{shadowLabelKey: shadowLabelVal}); err != nil {
		return nil, fmt.Errorf("list shadow EndpointSlices: %w", err)
	}
	nodes := make(map[string]bool)
	for i := range shadows.Items {
		nodes[shadows.Items[i].Labels[shadowNodeLabelKey]] = true
	}
	return nodes, nil
}

// EnsureGates turns the gate on for pods that have it but no condition yet. Without
// this a gated pod could never become Ready. On nodes in failover, a pod with a
// backup IP is left for FailoverNode; one without has nowhere else to go, so it
// keeps its primary place like the pods FailoverNode does not touch.
func (m *Manager) EnsureGates(ctx context.Context, failedOver map[string]bool) error {
	var pods corev1.PodList
	if err := m.client.List(ctx, &pods); err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !hasGate(pod) || gateCondition(pod) != nil {
			continue
		}
		if failedOver[pod.Spec.NodeName] && m.backupIP(pod) != "" {
			continue
		}
		// A pod deleted since the list must not stop the round for every other pod.
		if err := m.setGate(ctx, pod, corev1.ConditionTrue); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// FailoverNode moves traffic for the node's pods to their backup IPs. Only pods with
// a backup IP are touched: a pod without one has nowhere to go, so it keeps its place.
// It returns how many shadow slices it wrote. Zero means no Service sends traffic to
// these pods, so nothing moved and no gate is touched: the node is not failed over.
func (m *Manager) FailoverNode(ctx context.Context, nodeName string) (int, error) {
	logger := logf.FromContext(ctx).WithName("failover").WithValues("node", nodeName)

	pods, err := m.podsOnNode(ctx, nodeName)
	if err != nil {
		return 0, err
	}
	backupIPs := make(map[string]string)
	for i := range pods {
		if ip := m.backupIP(&pods[i]); ip != "" && pods[i].Status.PodIP != "" {
			backupIPs[pods[i].Status.PodIP] = ip
		}
	}
	if len(backupIPs) == 0 {
		return 0, fmt.Errorf("no pods with backup IPs on node %s; is Multus configured?", nodeName)
	}

	var slicesAll discoveryv1.EndpointSliceList
	if err := m.client.List(ctx, &slicesAll); err != nil {
		return 0, fmt.Errorf("list EndpointSlices: %w", err)
	}
	written := 0
	for i := range slicesAll.Items {
		slice := &slicesAll.Items[i]
		if slice.Labels[shadowLabelKey] == shadowLabelVal {
			continue
		}
		wrote, err := m.applyShadow(ctx, slice, nodeName, backupIPs)
		if err != nil {
			return written, err
		}
		if wrote {
			written++
		}
	}
	if written == 0 {
		return 0, nil
	}

	for i := range pods {
		pod := &pods[i]
		if _, ok := backupIPs[pod.Status.PodIP]; !ok {
			continue
		}
		if !hasGate(pod) {
			logger.Info("Pod has no readiness gate, so its primary IP stays in the Service", "pod", pod.Name)
			continue
		}
		if err := m.setGate(ctx, pod, corev1.ConditionFalse); err != nil {
			return written, err
		}
	}
	return written, nil
}

// FailbackNode undoes FailoverNode for one node. Gates go back on before the shadows
// are removed, so the Service is never left without the pods.
func (m *Manager) FailbackNode(ctx context.Context, nodeName string) error {
	pods, err := m.podsOnNode(ctx, nodeName)
	if err != nil {
		return err
	}
	for i := range pods {
		if hasGate(&pods[i]) {
			if err := m.setGate(ctx, &pods[i], corev1.ConditionTrue); err != nil {
				return err
			}
		}
	}

	return m.deleteShadows(ctx, nodeName)
}

// IsolateNode takes the node's gated pods out of their Services: the gate goes off
// and the node's shadow slices are removed. It is for a node that neither path
// reaches, so traffic goes only to pods elsewhere instead of to a dead address.
// FailbackNode or FailoverNode brings the pods back. It returns how many pods are out.
func (m *Manager) IsolateNode(ctx context.Context, nodeName string) (int, error) {
	pods, err := m.podsOnNode(ctx, nodeName)
	if err != nil {
		return 0, err
	}
	isolated := 0
	for i := range pods {
		if hasGate(&pods[i]) {
			if err := m.setGate(ctx, &pods[i], corev1.ConditionFalse); err != nil {
				return isolated, err
			}
			isolated++
		}
	}
	return isolated, m.deleteShadows(ctx, nodeName)
}

func (m *Manager) deleteShadows(ctx context.Context, nodeName string) error {
	var shadows discoveryv1.EndpointSliceList
	if err := m.client.List(ctx, &shadows, client.MatchingLabels{
		shadowLabelKey: shadowLabelVal, shadowNodeLabelKey: nodeName,
	}); err != nil {
		return fmt.Errorf("list shadow EndpointSlices: %w", err)
	}
	for i := range shadows.Items {
		if err := m.client.Delete(ctx, &shadows.Items[i]); err != nil {
			return fmt.Errorf("delete shadow slice %s: %w", shadows.Items[i].Name, err)
		}
	}
	return nil
}

func (m *Manager) podsOnNode(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := m.client.List(ctx, &pods, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return nil, fmt.Errorf("list pods on node %s: %w", nodeName, err)
	}
	return pods.Items, nil
}

// applyShadow creates or updates this node's shadow of one slice, and reports whether
// the slice had pods to shadow. Each node has its own shadow, so two broken nodes in
// one slice cannot overwrite each other.
func (m *Manager) applyShadow(ctx context.Context, original *discoveryv1.EndpointSlice, nodeName string, backupIPs map[string]string) (bool, error) {
	var endpoints []discoveryv1.Endpoint
	for _, ep := range original.Endpoints {
		if ep.NodeName == nil || *ep.NodeName != nodeName {
			continue
		}
		var addresses []string
		for _, address := range ep.Addresses {
			if ip, ok := backupIPs[address]; ok {
				addresses = append(addresses, ip)
			}
		}
		if len(addresses) > 0 {
			ready := true
			endpoints = append(endpoints, discoveryv1.Endpoint{
				Addresses:  addresses,
				NodeName:   ep.NodeName,
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			})
		}
	}
	if len(endpoints) == 0 {
		return false, nil
	}

	shadow := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      shadowName(original.Name, nodeName),
			Namespace: original.Namespace,
			Labels: map[string]string{
				serviceNameLabel:   original.Labels[serviceNameLabel],
				managedByLabel:     managedByValue,
				shadowLabelKey:     shadowLabelVal,
				shadowNodeLabelKey: nodeName,
			},
		},
		AddressType: original.AddressType,
		Ports:       original.Ports,
		Endpoints:   endpoints,
	}

	err := m.client.Create(ctx, shadow)
	if apierrors.IsAlreadyExists(err) {
		existing := &discoveryv1.EndpointSlice{}
		if err := m.client.Get(ctx, client.ObjectKeyFromObject(shadow), existing); err != nil {
			return false, fmt.Errorf("get shadow slice %s: %w", shadow.Name, err)
		}
		existing.Endpoints = endpoints
		existing.Ports = original.Ports
		err = m.client.Update(ctx, existing)
	}
	if err != nil {
		return false, fmt.Errorf("apply shadow slice %s: %w", shadow.Name, err)
	}
	return true, nil
}

func (m *Manager) setGate(ctx context.Context, pod *corev1.Pod, status corev1.ConditionStatus) error {
	if current := gateCondition(pod); current != nil && current.Status == status {
		return nil
	}
	original := pod.DeepCopy()
	condition := corev1.PodCondition{
		Type:               ReadinessGate,
		Status:             status,
		Reason:             "CNIResilience",
		LastTransitionTime: metav1.Now(),
	}
	if current := gateCondition(pod); current != nil {
		*current = condition
	} else {
		pod.Status.Conditions = append(pod.Status.Conditions, condition)
	}
	if err := m.client.Status().Patch(ctx, pod, client.StrategicMergeFrom(original)); err != nil {
		return fmt.Errorf("set readiness gate on pod %s: %w", pod.Name, err)
	}
	return nil
}

func hasGate(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Spec.ReadinessGates, func(g corev1.PodReadinessGate) bool {
		return g.ConditionType == ReadinessGate
	})
}

func gateCondition(pod *corev1.Pod) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == ReadinessGate {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

// backupIP is the pod's net1 address, if it lies in the backup address space.
func (m *Manager) backupIP(pod *corev1.Pod) string {
	ip := backupIP(pod)
	if ip == "" || m.backupNet == nil {
		return ip
	}
	if parsed := net.ParseIP(ip); parsed == nil || !m.backupNet.Contains(parsed) {
		return ""
	}
	return ip
}

func backupIP(pod *corev1.Pod) string {
	var entries []struct {
		Interface string   `json:"interface"`
		IPs       []string `json:"ips"`
	}
	if err := json.Unmarshal([]byte(pod.Annotations[multusNetworkStatusAnnotation]), &entries); err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.Interface == "net1" && len(entry.IPs) > 0 {
			return entry.IPs[0]
		}
	}
	return ""
}

// shadowName is unique per slice and node, using a hash so it always fits the name limit.
func shadowName(sliceName, nodeName string) string {
	sum := sha256.Sum256([]byte(sliceName + "/" + nodeName))
	return "shadow-" + hex.EncodeToString(sum[:8])
}
