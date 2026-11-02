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

package ipam

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	kubeProxyNamespace = "kube-system"
	kubeProxyName      = "kube-proxy"
	kubeProxyConfigKey = "config.conf"
	// restartAnnotation is the one `kubectl rollout restart` uses.
	restartAnnotation = "kubectl.kubernetes.io/restartedAt"
)

// kubeProxyLocalMode is what kube-proxy needs for pods in discontiguous pools
// (Phase 0, assumption 6). With a clusterCIDR it masquerades Service traffic
// from any pod outside that range, which hides the source of pods in the second
// pool. Detecting local pods by interface name has no such range.
func kubeProxyLocalMode(conf map[string]any) bool {
	changed := false
	set := func(m map[string]any, k string, v any) {
		if cur, ok := m[k]; !ok || fmt.Sprint(cur) != fmt.Sprint(v) {
			m[k] = v
			changed = true
		}
	}
	set(conf, "clusterCIDR", "")
	set(conf, "detectLocalMode", "InterfaceNamePrefix")
	dl, _ := conf["detectLocal"].(map[string]any)
	if dl == nil {
		dl = map[string]any{}
	}
	prev := fmt.Sprint(dl)
	dl["interfaceNamePrefix"] = "veth"
	if fmt.Sprint(dl) != prev {
		changed = true
	}
	conf["detectLocal"] = dl
	return changed
}

// EnsureKubeProxyLocalMode sets the kube-proxy local-traffic detection that
// buddy pools need and, only if that changed the ConfigMap, restarts the
// kube-proxy DaemonSet so it takes effect. It is idempotent. reader should read
// straight from the API server: the manager cache does not watch kube-system.
func EnsureKubeProxyLocalMode(ctx context.Context, reader client.Reader, c client.Client) (bool, error) {
	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: kubeProxyNamespace, Name: kubeProxyName}
	if err := reader.Get(ctx, key, &cm); err != nil {
		return false, fmt.Errorf("get kube-proxy ConfigMap: %w", err)
	}
	raw, ok := cm.Data[kubeProxyConfigKey]
	if !ok {
		return false, fmt.Errorf("kube-proxy ConfigMap has no %s", kubeProxyConfigKey)
	}
	conf := map[string]any{}
	if err := yaml.Unmarshal([]byte(raw), &conf); err != nil {
		return false, fmt.Errorf("parse kube-proxy config: %w", err)
	}
	if !kubeProxyLocalMode(conf) {
		return false, nil
	}
	out, err := yaml.Marshal(conf)
	if err != nil {
		return false, err
	}
	cm.Data[kubeProxyConfigKey] = string(out)
	if err := c.Update(ctx, &cm); err != nil {
		return false, fmt.Errorf("update kube-proxy ConfigMap: %w", err)
	}

	var ds appsv1.DaemonSet
	if err := reader.Get(ctx, key, &ds); err != nil {
		return true, fmt.Errorf("get kube-proxy DaemonSet: %w", err)
	}
	patch := client.MergeFrom(ds.DeepCopy())
	if ds.Spec.Template.Annotations == nil {
		ds.Spec.Template.Annotations = map[string]string{}
	}
	ds.Spec.Template.Annotations[restartAnnotation] = time.Now().Format(time.RFC3339)
	if err := c.Patch(ctx, &ds, patch); err != nil {
		return true, fmt.Errorf("restart kube-proxy: %w", err)
	}
	return true, nil
}
