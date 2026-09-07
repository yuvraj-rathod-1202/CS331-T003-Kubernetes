# Experiment 3: NetworkPolicy Failures

These experiments demonstrate that Kubernetes stores NetworkPolicies but does not verify their enforcement. It has no awareness of whether traffic rules are actually being applied.

---

## Experiment 3.1: Baseline → Deny-All → Selective Allow

**Goal:** Establish a working NetworkPolicy setup to understand the baseline behavior before we break it in experiments 3.2 and 3.3.

### Steps

**1. Deploy the NetworkPolicy test pods:**

```bash
kubectl apply -f experiments/manifests/netpolicy-server-deployment.yaml
kubectl apply -f experiments/manifests/netpolicy-clients-deployment.yaml
```

Wait for all pods to be Running:

```bash
kubectl get pods -l app=netpolicy-server
kubectl get pods -l app=netpolicy-client
```

**2. Verify baseline - both clients can reach the server (no policy applied yet):**

```bash
kubectl logs netpolicy-client-allowed --tail=3
kubectl logs netpolicy-client-blocked --tail=3
```

Both should show `status=200`.

**3. Apply deny-all ingress policy:**

```bash
kubectl apply -f experiments/manifests/networkpolicy-deny-all.yaml
```

**4. Verify - both clients are now blocked:**

```bash
kubectl logs -f netpolicy-client-allowed
kubectl logs -f netpolicy-client-blocked
```

Both should show `status=000` (connection timeout) since all ingress to `netpolicy-server` is denied.

**5. Apply selective allow policy:**

```bash
kubectl apply -f experiments/manifests/networkpolicy-allow-frontend.yaml
```

**6. Verify - only allowed client can connect:**

```bash
kubectl logs netpolicy-client-allowed --tail=3
# Should show: status=200

kubectl logs netpolicy-client-blocked --tail=3
# Should show: status=000 (still blocked)
```

This confirms that Calico is correctly enforcing both policies. The `deny-all` blocks everything, and `allow-frontend-only` opens a hole for pods with `role: allowed`.

---

## Experiment 3.2: Delete NetworkPolicy While Traffic Flows

**Goal:** Show that when a NetworkPolicy is deleted, Kubernetes does NOT alert, detect, or prevent the enforcement posture change. Traffic that was previously blocked now silently flows through.

### What breaks

- Security posture silently changes - blocked traffic becomes allowed
- No audit trail or alert from K8s

### What Kubernetes does

- Removes the resource from etcd and notifies the CNI

### What Kubernetes does NOT do

- Does NOT alert that a security policy was removed
- Does NOT verify that the intended enforcement posture is maintained
- Does NOT provide a "policy drift" detection mechanism

### Steps

**1. Confirm the blocked client is currently denied (from experiment 3.1):**

```bash
kubectl logs netpolicy-client-blocked --tail=3
# status=000 (blocked)
```

**2. Open a terminal to watch the blocked client's logs:**

```bash
kubectl logs -f netpolicy-client-blocked
```

**3. In another terminal, delete the deny-all policy:**

```bash
kubectl delete networkpolicy deny-all-ingress
```

**4. Watch the blocked client's logs:**

Within seconds, the blocked client starts succeeding:

```
11:05:01 | blocked-client -> server | status=000
11:05:04 | blocked-client -> server | status=000
11:05:07 | blocked-client -> server | status=200   <-- policy removed, traffic flows
11:05:10 | blocked-client -> server | status=200
```

**5. Check K8s for any warning or event:**

```bash
kubectl get events --sort-by='.lastTimestamp' | tail -10
```

No warning about the policy deletion's security impact.

### Key Takeaway

K8s is a declarative store - it doesn't understand the security intent behind a NetworkPolicy. Deleting a policy (accidentally or maliciously) silently opens traffic. Our operator will detect NetworkPolicy drift by periodically verifying that expected deny rules are still in place.

### Recovery

Re-apply the policy:

```bash
kubectl apply -f experiments/manifests/networkpolicy-deny-all.yaml
```

---

## Experiment 3.3: Kill calico-felix Process

**Goal:** Show that when Calico's enforcement agent (felix) is killed, existing iptables rules remain (stale enforcement) but new policy changes are NOT applied. Kubernetes restarts the pod but does NOT verify that policies are actually being enforced.

### What breaks

- New NetworkPolicy changes (create/update/delete) are NOT programmed into iptables
- Stale rules from before the crash remain active - traffic is still blocked/allowed based on old state
- Policy state drifts from desired state

### What Kubernetes does

- DaemonSet restarts the calico-node pod (felix runs inside it)

### What Kubernetes does NOT do

- Does NOT verify that iptables rules match the declared NetworkPolicies
- Does NOT detect the gap between felix crash and restart
- Does NOT alert that enforcement is stale

### Steps

**1. Ensure both NetworkPolicies are applied (deny-all + allow-frontend-only):**

```bash
kubectl apply -f experiments/manifests/networkpolicy-deny-all.yaml
kubectl apply -f experiments/manifests/networkpolicy-allow-frontend.yaml
```

Verify: allowed client → 200, blocked client → 000.

**2. Find the calico-node pod on the node where netpolicy-server is running:**

```bash
# Find which node the server is on
kubectl get pod netpolicy-server -o wide

# Find the calico-node pod on that node
kubectl get pods -n kube-system -l k8s-app=calico-node -o wide
```

**3. Kill the felix process inside calico-node:**

```bash
kubectl exec -n kube-system <calico-node-pod> -- pkill -f felix
```

**4. Quickly (before calico-node restarts) delete the deny-all policy:**

```bash
kubectl delete networkpolicy deny-all-ingress
```

**5. Check the blocked client:**

```bash
kubectl logs -f netpolicy-client-blocked
```

The blocked client may STILL be blocked because the old iptables rules are stale in the kernel - the policy was deleted from K8s but felix wasn't running to remove the iptables rules.

```
11:10:07 | blocked-client -> server | status=000   <-- stale rule still blocking
```

**6. Once calico-node restarts, felix syncs and removes the stale rules:**

```bash
kubectl logs -f netpolicy-client-blocked
```

```
11:10:25 | blocked-client -> server | status=200   <-- felix resynced, stale rule removed
```

**7. Check calico-node pod status:**

```bash
kubectl get pods -n kube-system -l k8s-app=calico-node
```

Pod shows `RESTARTS: 1`. K8s restarted it but never verified enforcement correctness.

### Key Takeaway

There are two problems:
1. During felix downtime, policy changes are silently not enforced
2. K8s has no mechanism to verify that declared policies match actual iptables rules

Our operator will verify enforcement by running test probes and comparing actual connectivity against expected policy state.

### Recovery

```bash
# Re-apply the policies to restore desired state
kubectl apply -f experiments/manifests/networkpolicy-deny-all.yaml
kubectl apply -f experiments/manifests/networkpolicy-allow-frontend.yaml
```
