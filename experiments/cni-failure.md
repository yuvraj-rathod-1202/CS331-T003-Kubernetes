# Experiment 1: CNI Plugin Failures

These experiments demonstrate that Kubernetes does not automatically recover from CNI-level networking failures.

---

## Experiment 1.1: Kill calico-node Pod

**Goal:** Show that killing the CNI agent pod on a node breaks networking for pods on that node, and while K8s restarts the DaemonSet pod, there is a connectivity gap that K8s does not detect or report at the application level.

### What breaks

When `calico-node` is killed on a node:
- Existing pods on that node lose their network routes (BGP routes withdrawn)
- New pods scheduled on that node get stuck in `ContainerCreating`
- Cross-node traffic to/from that node fails

### What Kubernetes does

- DaemonSet controller detects the missing pod and recreates it
- Node may briefly be marked `NotReady` if the kubelet loses its CNI

### What Kubernetes does NOT do

- Does NOT detect the connectivity loss for existing running pods
- Does NOT alert that pod-to-pod traffic is failing
- Does NOT prevent traffic from being routed to unreachable pods

### Steps

**1. Open two terminals. Terminal 1 — watch frontend-probe logs:**

```bash
kubectl logs -f -l app=frontend-probe
```

You should see continuous `status=200` responses.

**2. Terminal 2 — identify the calico-node pod on node-2:**

```bash
kubectl get pods -n kube-system -l k8s-app=calico-node -o wide
```

Note the pod running on `k8s-experiments-m02`.

**3. Delete it:**

```bash
kubectl delete pod <calico-node-pod-on-node2> -n kube-system
```

**4. Observe in Terminal 1:**

Within seconds, frontend-probe requests to the backend on node-2 start timing out:

```
10:35:01 | status=200 | latency=10ms
10:35:03 | status=000 | latency=5003ms   <-- timeout
10:35:08 | status=000 | latency=5002ms   <-- timeout
```

**5. Check pod status:**

```bash
kubectl get pods -n kube-system -l k8s-app=calico-node -o wide
```

The DaemonSet is restarting calico-node. Once it's `Running` again, connectivity returns.

**6. Check in Prometheus:**

Query `coredns_dns_requests_total` or check the `up` metric for the calico-node target to see the gap.

### Key Takeaway

K8s restarts the calico-node pod (DaemonSet self-healing), but during the gap:
- Running pods silently lose connectivity
- No alert is raised
- Application-level failures go undetected by K8s

This is the gap our custom operator will fill — detecting connectivity loss and triggering faster remediation.

### Recovery

Automatic — DaemonSet restarts the pod. If you need to force it:

```bash
kubectl rollout restart daemonset calico-node -n kube-system
```

---

## Experiment 1.2: IP Pool Exhaustion

**Goal:** Show that Kubernetes has no awareness of CNI IP address space. When the IP pool is exhausted, pods fail to start and K8s does nothing to detect or recover.

### What breaks

When the IPAM pool runs out:
- New pods get stuck in `ContainerCreating`
- Events show `failed to allocate for range 0: no IP addresses available in range set`
- Existing pods keep running (they already have IPs)

### What Kubernetes does

- Nothing. It keeps trying to schedule pods. They stay stuck.

### What Kubernetes does NOT do

- Does NOT detect IP pool exhaustion
- Does NOT alert about IPAM capacity
- Does NOT clean up leaked IPs
- Does NOT prevent scheduling to an exhausted pool

### Steps

**1. Check current Calico IP pool:**

```bash
calicoctl get ippool -o wide
```

Note the current default pool CIDR.

**2. Disable the default pool and apply the tiny pool:**

```bash
# Disable the default pool (don't delete it — we'll restore it later)
calicoctl patch ippool default-ipv4-ippool --patch '{\"spec\":{\"disabled\":true}}'

# Apply the tiny /28 pool (14 usable IPs)
calicoctl apply -f experiments/manifests/tiny-ippool.yaml
```

**3. Create a batch of pods to exhaust the pool:**

```bash
kubectl create deployment ip-exhaust --image=busybox --replicas=20 -- sleep 3600
```

**4. Watch pods:**

```bash
kubectl get pods -o wide -w
```

The first ~12 pods will get IPs and start. The remaining pods will be stuck:

```
ip-exhaust-xxx   0/1   ContainerCreating   0   30s   <none>   k8s-experiments-m02
```

**5. Check events:**

```bash
kubectl describe pod <stuck-pod-name>
```

Look for:

```
Warning  FailedCreatePodSandBox  ...  failed to allocate for range 0: no IP addresses available
```

**6. Verify with calicoctl:**

```bash
calicoctl ipam show
```
Shows the pool is at 100% usage.

### Key Takeaway

K8s has zero visibility into IPAM. It keeps scheduling pods that can never start. There is no built-in alert, no auto-cleanup of leaked IPs, no capacity awareness. Our operator will monitor IPAM usage and alert when thresholds are exceeded.

### Recovery

```bash
# Delete the test deployment
kubectl delete deployment ip-exhaust

# Remove the tiny pool and re-enable the default
calicoctl delete ippool experiment-tiny-pool
calicoctl patch ippool default-ipv4-ippool --patch '{\"spec\":{\"disabled\":false}}'
```
