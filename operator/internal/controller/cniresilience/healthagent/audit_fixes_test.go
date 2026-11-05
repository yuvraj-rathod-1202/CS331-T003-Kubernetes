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

package healthagent

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

func nodeWithReport(name, raw string) client.Object {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if raw != "" {
		n.Annotations = map[string]string{ReportAnnotation: raw}
	}
	return n
}

func fakeNodes(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// A report naming a node that no longer exists must not make every verdict
// ambiguous (which blocked failback for good).
func TestEdgesToDeletedNodesAreIgnored(t *testing.T) {
	now := time.Now()
	rep := func(peers string) string { return fmt.Sprintf(`{"time":%d,"peers":{%s}}`, now.Unix(), peers) }
	c := fakeNodes(t,
		nodeWithReport("a", rep(`"b":{"primary":true},"gone":{"primary":false}`)),
		nodeWithReport("b", rep(`"a":{"primary":true}`)),
	)
	health, err := ReadNodeHealth(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range health {
		if h.Ambiguous || !h.Working {
			t.Fatalf("node %s: %+v", name, h)
		}
	}
}

// One unreadable report makes that node silent; it does not fail the whole read.
func TestUnreadableReportIsSilentNotFatal(t *testing.T) {
	c := fakeNodes(t, nodeWithReport("a", "{not json"), nodeWithReport("b", ""))
	health, err := ReadNodeHealth(context.Background(), c, time.Now())
	if err != nil {
		t.Fatalf("ReadNodeHealth failed: %v", err)
	}
	if len(health) != 2 {
		t.Fatalf("health = %v", health)
	}
}

func TestProbeSettingsAreClamped(t *testing.T) {
	interval, timeout, threshold := probeSettings(remediationv1alpha1.CNIResilienceSpec{})
	if interval != time.Second || timeout != time.Second || threshold != 1 {
		t.Fatalf("got %v %v %d", interval, timeout, threshold)
	}
	_, timeout, _ = probeSettings(remediationv1alpha1.CNIResilienceSpec{ProbeIntervalSeconds: 2, ProbeTimeoutSeconds: 10})
	if timeout != 2*time.Second {
		t.Fatalf("timeout %v not capped at the interval", timeout)
	}
}

func TestBackupIPMustBeInBackupNetwork(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		multusNetworkStatusAnnotation: `[{"interface":"eth0","ips":["10.244.0.5"]},{"interface":"net1","ips":["8.8.8.8"]}]`,
	}}}
	_, backupNet, _ := net.ParseCIDR("192.168.0.0/16")
	if ip := backupIP(pod, backupNet); ip != "" {
		t.Fatalf("accepted %s outside the backup network", ip)
	}
	if ip := backupIP(pod, nil); ip != "8.8.8.8" {
		t.Fatalf("with no known network got %q", ip)
	}
}

func TestOnlyDaemonSetPodsArePeers(t *testing.T) {
	yes := true
	owned := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "cni-health-agent", Controller: &yes}}}}
	if !ownedByDaemonSet(owned) || ownedByDaemonSet(&corev1.Pod{}) {
		t.Fatal("ownedByDaemonSet is wrong")
	}
}

func TestMissingPeersAreForgottenAfterAWhile(t *testing.T) {
	d := &Daemon{primary: map[string]*streak{"a": {}, "gone": {}}, backup: map[string]*streak{"gone": {}}, lastSeen: map[string]time.Time{}}
	t0 := time.Now()
	d.forgetMissingPeers([]string{"a", "gone"}, t0)
	d.forgetMissingPeers([]string{"a"}, t0.Add(time.Minute))
	if d.primary["gone"] == nil {
		t.Fatal("a short absence must keep the state")
	}
	d.forgetMissingPeers([]string{"a"}, t0.Add(forgetPeerAfter+time.Minute))
	if d.primary["gone"] != nil || d.backup["gone"] != nil || d.primary["a"] == nil {
		t.Fatalf("primary=%v backup=%v", d.primary, d.backup)
	}
}
