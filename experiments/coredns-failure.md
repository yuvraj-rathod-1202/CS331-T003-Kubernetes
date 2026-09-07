# Experiment 2: CoreDNS Failures

These experiments demonstrate that Kubernetes does not detect or recover from DNS degradation, latency, or misconfiguration.

---

## Experiment 2.1: CoreDNS Scale to Zero

**Goal:** Show that when CoreDNS is completely unavailable, all service discovery breaks. Kubernetes does NOT auto-scale CoreDNS back.

### What breaks

- All DNS lookups from pods fail (both internal `*.svc.cluster.local` and external like `google.com`)
- Services cannot be reached by name (only by direct IP)
- New pod creation may hang if it needs to resolve image registries

### What Kubernetes does

- Nothing. CoreDNS is a Deployment - K8s respects the desired replica count you set.

### What Kubernetes does NOT do

- Does NOT detect that DNS resolution is failing cluster-wide
- Does NOT auto-scale CoreDNS back to a healthy replica count
- Does NOT alert that a critical cluster service is down

### Steps

**1. Open two terminals. Terminal 1 - watch dns-checker logs:**

```bash
kubectl logs -f -l app=dns-checker
```

You should see `INTERNAL | OK` and `EXTERNAL | OK`.

**2. Terminal 2 - scale CoreDNS to zero:**

```bash
kubectl scale deployment coredns -n kube-system --replicas=0
```

**3. Observe Terminal 1:**

Within seconds, all lookups start failing:

```
--- 10:40:01 ---
INTERNAL | FAIL | 5003ms
EXTERNAL | FAIL | 5002ms
```

**4. Check frontend-probe logs:**

```bash
kubectl logs -f -l app=frontend-probe
```

If the probe uses the service name `http://backend-api/`, it fails because DNS can't resolve the name. If you had probes using direct pod IPs, those would still work - proving the issue is DNS, not networking.

**5. Verify CoreDNS is gone:**

```bash
kubectl get pods -n kube-system -l k8s-app=kube-dns
```

No pods listed. K8s shows zero replicas and does nothing about it.

**6. Check in Prometheus:**

Query `coredns_dns_requests_total` - the metric stops being reported (scrape target gone).

### Key Takeaway

K8s treats CoreDNS like any other Deployment - if you (or a bug) scale it to 0, it stays at 0. There is no "minimum critical service" concept. Our operator will detect when CoreDNS health degrades and take corrective action.

### Recovery

```bash
kubectl scale deployment coredns -n kube-system --replicas=2
```

DNS resolution resumes within a few seconds.

---

## Experiment 2.2: CoreDNS Latency via CPU Throttle

**Goal:** Show that Kubernetes does not detect DNS latency. CoreDNS is "Running" but queries take 10-50x longer, degrading all service-to-service communication.

### What breaks

- DNS queries slow down from ~3ms to 200-1000ms+
- Every HTTP request that resolves a service name adds this latency
- Application performance degrades without any K8s-visible error

### What Kubernetes does

- Pod is `Running`, readiness/liveness probes pass (if default)
- K8s sees nothing wrong

### What Kubernetes does NOT do

- Does NOT monitor DNS query latency
- Does NOT detect degraded CoreDNS performance
- Does NOT auto-scale or adjust resources

### Steps

**1. Record baseline dns-checker latency:**

```bash
kubectl logs -l app=dns-checker --tail=10
```

Note the typical latency (usually 0-10ms).

**2. Deploy DNS load generator (creates enough query pressure to expose throttling):**

```bash
kubectl apply -f experiments/manifests/dns-load-generator.yaml
kubectl rollout status deployment/dns-load-generator
```

This runs 2 pods sending ~30 queries/second each, creating realistic DNS pressure.

**3. Throttle CoreDNS CPU to near-zero:**

```bash
kubectl patch deployment coredns -n kube-system --type='json' -p='[
  {"op": "replace", "path": "/spec/template/spec/containers/0/resources", "value": {
    "limits": {"cpu": "1m", "memory": "170Mi"},
    "requests": {"cpu": "1m", "memory": "70Mi"}
  }}
]'
```

**4. Wait 30 seconds for the rollout, then watch dns-checker:**

```bash
kubectl logs -f -l app=dns-checker
```

Latency spikes dramatically under load:

```
--- 10:45:01 ---
INTERNAL | OK   | 450ms    <-- was 0-10ms
EXTERNAL | OK   | 1200ms   <-- was 0-10ms
```

**5. Check frontend-probe latency:**

```bash
kubectl logs -f -l app=frontend-probe
```

HTTP latency increases because each request now includes DNS overhead:

```
10:45:03 | status=200 | latency=520ms   <-- was 2-3ms
```

**6. Check CoreDNS pod status:**

```bash
kubectl get pods -n kube-system -l k8s-app=kube-dns
```

Pod is `Running` and `1/1 Ready`. K8s sees nothing wrong.

**7. Check in Prometheus:**

Query:

```
rate(coredns_dns_request_duration_seconds_sum[1m]) / rate(coredns_dns_request_duration_seconds_count[1m])
```

This shows the average query duration spiking.

### Key Takeaway

K8s has no concept of "DNS is slow". The pod is running, probes pass, but every service call in the cluster is silently degraded. Our operator will monitor `coredns_dns_request_duration_seconds` and take action when latency exceeds a threshold.

### Recovery

```bash
# Restore CoreDNS resources
kubectl patch deployment coredns -n kube-system --type='json' -p='[
  {"op": "replace", "path": "/spec/template/spec/containers/0/resources", "value": {
    "limits": {"cpu": "100m", "memory": "170Mi"},
    "requests": {"cpu": "100m", "memory": "70Mi"}
  }}
]'

# Remove load generator
kubectl delete -f experiments/manifests/dns-load-generator.yaml
```

---

## Experiment 2.3: Corrupt Upstream DNS Configuration

**Goal:** Show that Kubernetes does not validate CoreDNS configuration. Internal DNS still works, but external DNS resolution silently breaks.

### What breaks

- External DNS resolution fails (e.g., `google.com`, `api.github.com`)
- Internal DNS still works (`backend-api.default.svc.cluster.local`)
- Any pod that needs to reach external services (pull images, call external APIs) fails

### What Kubernetes does

- Nothing. It applied the ConfigMap change. CoreDNS reloads the config.

### What Kubernetes does NOT do

- Does NOT validate that the upstream DNS server is reachable
- Does NOT detect that external resolution is failing
- Does NOT rollback the config change

### Steps

**1. Save the current CoreDNS ConfigMap:**

```bash
kubectl get configmap coredns -n kube-system -o yaml > /tmp/coredns-backup.yaml
```

**2. Edit the CoreDNS ConfigMap:**

```bash
kubectl edit configmap coredns -n kube-system
```

Find the `forward` directive (usually `forward . /etc/resolv.conf`) and change it to:

```
forward . 192.0.2.1
```

`192.0.2.1` is a TEST-NET address - it will never respond.

**3. Restart CoreDNS to pick up the change:**

```bash
kubectl rollout restart deployment coredns -n kube-system
```

**4. Watch dns-checker logs:**

```bash
kubectl logs -f -l app=dns-checker
```

Internal DNS still works, but external DNS fails:

```
--- 10:50:01 ---
INTERNAL | OK   | 3ms
EXTERNAL | FAIL | 5002ms   <-- timeout
```

**5. Verify from a test pod:**

```bash
kubectl exec -it <any-frontend-probe-pod> -- nslookup google.com
```

Times out. But:

```bash
kubectl exec -it <any-frontend-probe-pod> -- nslookup backend-api.default.svc.cluster.local
```

Works fine.

**6. Check CoreDNS pod status:**

```bash
kubectl get pods -n kube-system -l k8s-app=kube-dns
```

Pods are `Running` and `Ready`. K8s sees nothing wrong.

### Key Takeaway

K8s is a config store - it doesn't validate what's inside ConfigMaps. A typo or malicious change to CoreDNS upstream config silently breaks external DNS for the entire cluster. Our operator will detect spikes in external DNS failures and can auto-restore a known-good config.

### Recovery

```bash
kubectl apply -f /tmp/coredns-backup.yaml
kubectl rollout restart deployment coredns -n kube-system
```

---

## Experiment 2.4: CoreDNS Pod Crash with Replicas=2 (ReplicaSet Self-Healing & Transient Impact)

**Goal:** Determine whether Kubernetes creates another pod when expected CoreDNS replicas is set to 2 and a pod crashes or is terminated, observe how the ReplicaSet reconciles the state, and evaluate the impact on DNS resolution during the failure.

### What breaks / degrades

- When one of the two CoreDNS pods crashes or is terminated:
  - DNS query handling capacity drops by 50% immediately
  - In-flight queries sent to the dying pod before kube-proxy / endpoint updates may experience transient timeouts (~2-5 seconds)
  - The surviving replica experiences doubled query load
  - If a systemic crash occurs (e.g., OOM or bad plugin), the pod enters `CrashLoopBackOff` with exponential delays (up to 5 minutes)

### What Kubernetes does

- **YES, Kubernetes creates another pod.**
- CoreDNS is managed by a Kubernetes `Deployment`, which manages a `ReplicaSet`.
- The ReplicaSet controller detects that actual healthy replicas (1) is less than desired replicas (2) and immediately issues an API call to schedule and create a replacement pod (`Pending` -> `ContainerCreating` -> `Running` -> `Ready`).
- Once the new pod passes its readiness probe (`:8181/ready`), the Endpoints / EndpointSlice controller adds its IP back to the `kube-dns` service endpoints.

### What Kubernetes does NOT do

- Does NOT prevent dropped in-flight DNS queries between the moment the pod crashes and the moment endpoints/iptables rules are updated
- Does NOT detect if the surviving replica is overwhelmed or CPU-throttled by the redirected traffic
- Does NOT provide application-level alerting that DNS redundancy has been degraded to a single point of failure
- Does NOT fix underlying root causes if the pod keeps crashing (enters `CrashLoopBackOff` with long delays)

### Steps

**1. Set expected CoreDNS replicas to 2 and verify both are running:**

```bash
kubectl scale deployment coredns -n kube-system --replicas=2
kubectl rollout status deployment coredns -n kube-system
```

Verify two healthy pods and check their names and IPs:

```bash
kubectl get pods -n kube-system -l k8s-app=kube-dns -o wide
```

You should see 2 pods in `Running` status with `1/1 Ready`.

**2. Inspect current endpoints of the kube-dns Service:**

```bash
kubectl get endpoints kube-dns -n kube-system
```

You will see 2 endpoint IPs corresponding to the two CoreDNS pods.

**3. Open two terminals.**

**Terminal 1 - watch dns-checker logs continuously:**

```bash
kubectl logs -f -l app=dns-checker
```

Verify that queries are succeeding (`INTERNAL | OK` and `EXTERNAL | OK`).

**Terminal 2 - stream CoreDNS pod status changes in real-time:**

```bash
kubectl get pods -n kube-system -l k8s-app=kube-dns -w
```

**4. In Terminal 3, stop/crash one of the CoreDNS pods:**

Simulate an abrupt crash / pod termination:

```bash
# Capture the name of one of the running CoreDNS pods
TARGET_POD=$(kubectl get pods -n kube-system -l k8s-app=kube-dns -o jsonpath='{.items[0].metadata.name}')
echo "Crashing pod: $TARGET_POD"

# Delete the pod immediately (simulates pod kill / crash)
kubectl delete pod $TARGET_POD -n kube-system --now
```

*(Optional alternative)*: You can also simulate an in-container process crash by killing the process:
```bash
kubectl exec -n kube-system $TARGET_POD -- kill -9 1
```

**5. Observe Terminal 2 (Pod Watcher) - Does it create another pod?**

Watch the lifecycle events unfold immediately:

```
NAME                       READY   STATUS        RESTARTS   AGE
coredns-668d6bf9bc-abcde   1/1     Terminating   0          5m
coredns-668d6bf9bc-xyz12   0/1     Pending       0          0s     <-- NEW POD CREATED INSTANTLY
coredns-668d6bf9bc-xyz12   0/1     ContainerCreating   0    1s
coredns-668d6bf9bc-xyz12   1/1     Running             0    3s
coredns-668d6bf9bc-abcde   0/1     Terminating   0          5m
```

- **Result:** The ReplicaSet controller detected `replicas = 1 < 2` and **immediately created a new pod** (`coredns-668d6bf9bc-xyz12`).
- Inspect the ReplicaSet events to confirm:

```bash
kubectl describe replicaset -n kube-system -l k8s-app=kube-dns
```

Look for the event:
```
Normal  SuccessfulCreate  ...  Created pod: coredns-...
```

**7. Verify Service Endpoints updated:**

```bash
kubectl get endpoints kube-dns -n kube-system
```

The dead pod IP is removed and replaced by the newly created pod IP once it becomes `1/1 Ready`.

### Key Takeaway

- **Does Kubernetes create another pod? Yes.** CoreDNS is backed by a Deployment/ReplicaSet. When configured with `replicas = 2`, K8s actively reconciles pod deaths by creating replacement pods.
- **Where Kubernetes falls short:**
  1. During the failover window, cluster DNS runs with 50% capacity on a single pod. If cluster traffic is high, the surviving pod may experience latency degradation or OOM kills.
  2. If the crash is recurring (e.g., OOM, poisoned cache, kernel issue), K8s enters `CrashLoopBackOff` with delays up to 300 seconds, and does not perform active root-cause remediation or alert operators.
  3. Our custom operator continuously tracks CoreDNS replica health and query error rates to detect degraded redundancy and trigger faster remediation before cluster-wide DNS breaks.

### Recovery

Verify that both CoreDNS pods are healthy and running:

```bash
kubectl get deployment coredns -n kube-system
kubectl get pods -n kube-system -l k8s-app=kube-dns
```

