# Experiment 4: Pod-to-Pod Connectivity Failures

These experiments demonstrate that Kubernetes has no network-level health probing. When the underlying network breaks, K8s does not detect, report, or recover from the failure.

Each experiment targets a different network layer to identify exactly which component causes the failure.

---

## Experiment 4.1: Bring Down a Pod's veth Interface

**Goal:** Show that disabling a single pod's virtual ethernet interface isolates only that pod. Kubernetes does NOT detect that the pod is network-dead.

### What breaks

- The targeted pod loses ALL network connectivity (no ingress or egress)
- Other pods on the same node are unaffected
- K8s still shows the pod as `Running` and `1/1 Ready`

### What causes it

Each pod gets a virtual ethernet (veth) pair. One end is inside the pod's network namespace (`eth0`), the other end is on the host. Bringing down the host-side end "unplugs the cable" for that pod only.

### Steps

**1. Open Terminal 1 - watch frontend-probe logs:**

```bash
kubectl logs -f -l app=frontend-probe
```

Confirm all requests succeed (`status=200`).

**2. SSH into node-2:**

```bash
minikube ssh -p k8s-experiments -n k8s-experiments-m02
```

**3. Find the backend-api pod's veth interface:**

```bash
# List all pods and their IPs
# (run this outside the SSH session)
kubectl get pods -o wide -l app=backend-api
```

Note the IP of the backend-api pod on node-2.

Inside the SSH session:

```bash
# Find the veth pair connected to that pod's IP
ip route | grep <pod-IP>
# Output like: 10.244.x.x dev caliXXXXXXX scope link

# The interface name is caliXXXXXXX (Calico veth)
```

**4. Bring the veth down:**

```bash
sudo ip link set <cali-interface> down
```

**5. Observe Terminal 1 (frontend-probe logs):**

Requests to the backend on node-2 start failing:

```
11:20:01 | status=200 | latency=8ms
11:20:03 | status=000 | latency=5003ms   <-- timeout to node-2 backend
11:20:05 | status=200 | latency=10ms     <-- request hit the node-1 backend (Service load-balanced)
11:20:07 | status=000 | latency=5001ms   <-- timeout again (hit node-2 backend)
```

**6. Check pod status:**

```bash
kubectl get pods -o wide -l app=backend-api
```

The pod is STILL `Running` and `1/1 Ready`. K8s has no idea its network is dead.

**7. Verify other pods on node-2 are fine:**

```bash
kubectl exec -it <dns-checker-on-node2> -- nslookup backend-api
```

Works - only the targeted pod's network is broken.

### Key Takeaway

Bringing down a single veth interface isolates exactly one pod. K8s has no network-layer health checks - it relies only on application-level liveness/readiness probes, which may not catch network isolation. Our operator will run periodic connectivity probes to detect this.

### Recovery

Inside the SSH session on node-2:

```bash
sudo ip link set <cali-interface> up
```

Exit the SSH session:

```bash
exit
```

---

## Experiment 4.2: Bring Down the Tunnel Interface (IPIP) (Thid already handled by the calico so not need to handle this in our operator)

**Goal:** Show that disabling the Calico tunnel interface breaks ALL cross-node pod traffic on that node. Kubernetes does NOT detect this.

### What breaks

- ALL pods on node-2 lose connectivity TO and FROM pods on node-1
- Intra-node traffic on node-2 still works (pods on same node can talk to each other)
- K8s still shows all pods as `Running`

### What causes it

Calico uses IPIP tunneling (`tunl0` interface) for cross-node traffic. Bringing it down breaks the overlay network for that node. This is more severe than a veth failure - it affects ALL pods on the node, but only for cross-node traffic.

### Steps

**1. Open Terminal 1 - watch frontend-probe logs:**

```bash
kubectl logs -f -l app=frontend-probe
```

**2. Verify cross-node connectivity baseline:**

```bash
# Get pod IPs
kubectl get pods -o wide

# From a pod on node-1, ping a pod on node-2
kubectl exec -it <dns-checker-on-node1> -- ping -c 3 <backend-pod-IP-on-node2>
```

All pings succeed.

**3. SSH into node-2:**

```bash
minikube ssh -p k8s-experiments -n k8s-experiments-m02
```

**4. Check the tunnel interface:**

```bash
ip link show tunl0
```

Should show `UP`.

**5. Bring the tunnel interface down:**

```bash
sudo ip link set tunl0 down
```

**6. Observe Terminal 1:**

ALL cross-node traffic fails:

```
11:25:01 | status=200 | latency=9ms      <-- hit local backend
11:25:03 | status=000 | latency=5002ms   <-- hit remote backend (FAIL)
11:25:05 | status=200 | latency=8ms      <-- hit local backend
11:25:07 | status=000 | latency=5001ms   <-- hit remote backend (FAIL)
```

**7. Verify intra-node traffic still works:**

SSH into node-2 and test:

```bash
# From inside node-2, curl a pod on node-2
curl -s --connect-timeout 3 http://<backend-pod-IP-on-node2>:8080
# Should work

# Curl a pod on node-1
curl -s --connect-timeout 3 http://<backend-pod-IP-on-node1>:8080
# Should FAIL (timeout)
```

This proves the issue is specifically the IPIP tunnel, not general networking.

**8. Check pod and node status:**

```bash
kubectl get pods -o wide
kubectl get nodes
```

All pods `Running`, all nodes `Ready`. K8s is completely unaware.

### Key Takeaway

The tunnel interface is the critical link for cross-node traffic. When it goes down:
- K8s sees nothing wrong (pods are running, node is ready)
- Only cross-node traffic is affected - making it hard to diagnose manually
- Intra-node traffic masks the problem

Our operator will detect internode connectivity failures by running cross-node probes and triggering CNI agent restart when the overlay is broken.

### Recovery

Inside the SSH session on node-2:

```bash
sudo ip link set tunl0 up
```

If routes are lost, restart calico-node on that node:

```bash
# Exit SSH session first
exit

kubectl delete pod <calico-node-pod-on-node2> -n kube-system
```

---

## Experiment 4.3: iptables DROP Rule

**Goal:** Show that injecting a firewall rule at the kernel level silently breaks pod egress traffic. Kubernetes does NOT detect or recover from this.

### What breaks

- Pods on node-2 cannot send traffic out (responses to incoming requests are dropped)
- From the caller's perspective, requests time out (packets arrive at node-2 but responses never come back)
- K8s shows everything as `Running` and `Ready`

### What causes it

An `iptables` FORWARD chain DROP rule blocks all forwarded traffic from the pod subnet. This simulates a firewall misconfiguration or a leftover rule from a crashed CNI plugin.

### Steps

**1. Open Terminal 1 - watch frontend-probe logs:**

```bash
kubectl logs -f -l app=frontend-probe
```

**2. Find the pod subnet on node-2:**

```bash
minikube ssh -p k8s-experiments -n k8s-experiments-m02 -- ip route
```

Look for the pod CIDR (e.g., `10.244.1.0/24` or similar Calico range).

**3. SSH into node-2 and add the DROP rule:**

```bash
minikube ssh -p k8s-experiments -n k8s-experiments-m02
```

```bash
# Replace with actual pod subnet from step 2
sudo iptables -I FORWARD -s 10.244.1.0/24 -j DROP
```

**4. Observe Terminal 1:**

Requests to backends on node-2 start timing out:

```
11:30:01 | status=200 | latency=10ms
11:30:03 | status=000 | latency=5003ms   <-- response dropped by iptables
11:30:05 | status=200 | latency=9ms
11:30:07 | status=000 | latency=5002ms   <-- dropped again
```

**5. Test from inside a pod on node-2:**

```bash
kubectl exec -it <dns-checker-on-node2> -- nslookup google.com
```

Fails - egress from the pod is dropped by the iptables rule.

```bash
kubectl exec -it <dns-checker-on-node2> -- nslookup backend-api.default.svc.cluster.local
```

Also fails - even DNS queries to CoreDNS (which may be on node-1) are blocked.

**6. Check pod and node status:**

```bash
kubectl get pods -o wide
kubectl get nodes
```

All `Running`, all `Ready`. K8s is unaware.

**7. Verify the rule is the cause:**

Inside SSH on node-2:

```bash
sudo iptables -L FORWARD -n --line-numbers | head -5
```

You'll see your DROP rule at position 1.

### Key Takeaway

A single iptables rule can silently break all pod traffic on a node. This can happen from:
- Crashed CNI leaving stale rules
- Misconfigured firewall scripts
- Conflicting security tools

K8s has no visibility into the iptables state. Our operator will detect egress failures through active probing and can alert or restart the CNI agent to flush stale rules.

### Recovery

Inside the SSH session on node-2:

```bash
# Remove the DROP rule (find its line number first)
sudo iptables -L FORWARD -n --line-numbers | head -5
sudo iptables -D FORWARD <line-number>
```

Or remove by specification:

```bash
sudo iptables -D FORWARD -s 10.244.1.0/24 -j DROP
```

Exit SSH:

```bash
exit
```
