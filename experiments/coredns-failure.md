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
