# Complete Experiment Guide: Problem 2 — Dual-CNI Resilience, Failover, & Recovery

This document provides a **complete, step-by-step operational guide** to set up, run, evaluate, and demonstrate the CNI Resilience experiment from start to finish.

---

## 1. Prerequisites & System Requirements

Ensure you have the following installed on your machine (Linux / WSL2 on Windows / macOS):

* **Docker**: 24.0+ (Daemon running)
* **minikube**: v1.32+
* **kubectl**: v1.28+
* **Go**: 1.22+ (to run the operator)
* **Node.js & npm**: Node 18+ (to run the visualizer)

---

## 2. Phase 1: Cluster Provisioning

Start a clean, multi-node Minikube cluster using Docker and Calico as the primary CNI:

```bash
minikube start --nodes 2 --driver=docker --cni=calico --cpus=2 --memory=4096 -p k8s-experiments
```

### Verification
Verify that both nodes are `Ready` and Calico pods are active:
```bash
# 1. Check node readiness
kubectl get nodes -o wide

# 2. Check Calico primary CNI agents
kubectl get pods -n kube-system -l k8s-app=calico-node -o wide
```
*Expected Output*: Both nodes (`k8s-experiments` and `k8s-experiments-m02`) show status `Ready`, and one `calico-node` pod is running on each node.

---

## 3. Phase 2: Dual-CNI (Multus + Flannel) & CRD Installation

### Step 2.1: Install Multus Meta-CNI
Multus attaches secondary interfaces (`net1`) to pods:
```bash
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/deployments/multus-daemonset.yml
```

Verify Multus pods are running on all nodes:
```bash
kubectl rollout status daemonset kube-multus-ds -n kube-system
```

### Step 2.2: Configure Flannel as Secondary Network
Apply the Multus `NetworkAttachmentDefinition` for the backup Flannel network:
```bash
kubectl apply -f operator/deploy/multus-nad.yaml
```

Verify the network attachment definition:
```bash
kubectl get network-attachment-definitions -n kube-system
# Output should show: flannel-backup
```

### Step 2.3: Install CNIResilience Custom Resource Definition (CRD)
Register the `cniresiliences.remediation.cn-operator.yuvraj-rathod-1202.github.io` CRD into the cluster:
```bash
kubectl apply -f operator/config/crd/bases/remediation.cn-operator.yuvraj-rathod-1202.github.io_cniresiliences.yaml
```

### Step 2.4: Deploy the CNIResilience Configuration
Apply the cluster-wide resilience monitoring resource:
```bash
kubectl apply -f operator/config/samples/cniresilience_sample.yaml
```

Check the CR:
```bash
kubectl get cniresilience default-resilience
```

---

## 4. Phase 3: Deploy Dual-Homed Workload & Traffic Client

Deploy an application service configured with Multus so that every pod receives two interfaces:
* `eth0`: Primary Calico address (`10.244.x.x`)
* `net1`: Backup Flannel address (`192.168.x.x`)

### Step 3.1: Apply Deployment & Service Manifest
Create and apply `resilience-workload.yaml`:
```bash
kubectl apply -f experiments/manifests/resilience-workload.yaml
```

### Step 3.2: Verify Dual-IP Assignment on Pods
```bash
kubectl get pods -l app=resilience-target -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,PRIMARY_IP:.status.podIP,STATUS:.metadata.annotations."k8s\.v1\.cni\.cncf\.io/network-status"
```
*Expected Output*: Look at the `STATUS` column; each pod has both an `eth0` IP (`10.244.x.x`) and a `net1` IP (`192.168.x.x`).

---

## 5. Phase 4: Launch Operator & Visualizer Dashboard

Open **4 separate terminal windows** to run the complete environment:

### Terminal 1: Kubernetes API Proxy
Exposes the local Kubernetes API for the dashboard:
```bash
kubectl proxy
```
*(Leave running)*

### Terminal 2: Run the Kubernetes Operator
Runs the controller reconcile loop and health monitoring agent:
```bash
cd operator
make run
```
*(Leave running — logs will stream here)*

### Terminal 3: Run the Visualizer Web Dashboard
Runs the frontend web application:
```bash
cd k8s-visualizer
npm run dev
```
Open your browser and navigate to:
```
http://localhost:5173/project2
```

### Terminal 4: Continuous Service Traffic Stream
Start a live client generating continuous HTTP requests against the service:
```bash
kubectl run traffic-client --rm -i --tty --image=curlimages/curl -- sh -c '
  while true; do
    curl -s --connect-timeout 1 http://resilience-service.default.svc.cluster.local -I | head -n 1
    sleep 1
  done
'
```
*(You will see steady `HTTP/1.1 200 OK` messages)*

---

## 6. Phase 5: Execute Chaos Experiments & Observe Recovery

### Experiment 5.1: Calico Overlay Datapath Failure (VXLAN Drop)

**Goal**: Simulate a sudden partition in the primary CNI datapath on the worker node (`k8s-experiments-m02`) and observe sub-second failover via Shadow EndpointSlices followed by autonomous recovery.

#### Method 1: Via the Visualizer Dashboard
1. Go to `http://localhost:5173/project2`.
2. Under **Quadrant 3: Interactive Chaos Deck**, ensure Target Node is set to `k8s-experiments-m02` (or `minikube-m02`).
3. Click **`[ Sever Primary CNI (VXLAN Drop) ]`**.

#### Method 2: Via Terminal Command
Run inside the worker node container:
```bash
docker exec -it k8s-experiments-m02 iptables -I INPUT -p udp --dport 4789 -j DROP -m comment --comment "cniresilience-drop"
docker exec -it k8s-experiments-m02 iptables -I FORWARD -s 10.244.0.0/16 -j DROP -m comment --comment "cniresilience-drop"
```

---

### Observations to Verify During Failover (Within 5–15 Seconds)

1. **Terminal 2 (Operator Logs)**:
   ```text
   Health agent probe cycle detected primary down on k8s-experiments-m02
   AutoFailover triggered: collected backup IPs [192.168.1.105]
   Created shadow EndpointSlice for resilience-service with backup IPs
   Original endpoint 10.244.1.42 marked Ready: false
   ```

2. **EndpointSlice Verification**:
   Check EndpointSlices across the cluster:
   ```bash
   kubectl get endpointslices -l cniresilience.cn-operator/shadow=true
   ```
   *Observation*: A shadow slice appears with the `192.168.1.x` Flannel IP!

3. **Terminal 4 (Traffic Continuity)**:
   *Observation*: The continuous curl stream **does NOT drop**. After a brief sub-second blip, requests continue returning `HTTP/1.1 200 OK` over the backup path! Pods were **not restarted**.

4. **Self-Healing Progression**:
   * **Node Taint Applied**:
     ```bash
     kubectl describe node k8s-experiments-m02 | grep Taints
     ```
     Shows: `Taints: cniresilience.cn-operator/recovering:NoSchedule`.
   * **CNI Pod Re-spun**: Operator restarts `calico-node` on the failed node.
   * **Failback**: Once primary probes turn green:
     - The operator untaints the node.
     - Shadow EndpointSlice is deleted.
     - Original EndpointSlice is restored to `Ready: true`.
     - `kubectl get cniresilience default-resilience` transitions back to `Phase: Healthy`.

---

### Experiment 5.2: Calico Agent Crash (Felix Kill)

**Goal**: Test recovery when the node's CNI agent process dies completely.

1. **Inject Failure**:
   ```bash
   kubectl delete pod -n kube-system -l k8s-app=calico-node --field-selector spec.nodeName=k8s-experiments-m02 --now
   ```
   *OR click `[ Kill Calico Felix Agent Pod ]` in the `/project2` dashboard.*

2. **Verify Detection & Recovery**:
   * Health agent local check detects `http://localhost:9099/liveness` refused.
   * DaemonSet controller recreates the `calico-node` pod.
   * Operator validates route re-injection and clears recovery status.

---

### Experiment 5.3: Silent MTU Blackhole (Large Packet Drops)

**Goal**: Demonstrate that standard pings (56 bytes) pass while large packets drop, and show our large-packet probe catches it.

1. **Inject MTU Drop Rule**:
   ```bash
   docker exec -it k8s-experiments-m02 iptables -I FORWARD -m length --length 1400:65535 -j DROP -m comment --comment "mtu-drop"
   ```

2. **Observe**:
   * Standard ICMP ping still passes.
   * The operator's **Large-Packet / MTU probe (1450 bytes, DF bit set)** detects packet drops immediately and flags `Phase: Degraded`.

3. **Restore**:
   ```bash
   docker exec -it k8s-experiments-m02 iptables -D FORWARD -m length --length 1400:65535 -j DROP -m comment --comment "mtu-drop"
   ```

---

## 7. Phase 6: Post-Experiment Reset & Cleanup

### Reset Network Rules
If any manual iptables rules were inserted, clear them:
```bash
docker exec -it k8s-experiments-m02 iptables -D INPUT -p udp --dport 4789 -j DROP -m comment --comment "cniresilience-drop" 2>/dev/null || true
docker exec -it k8s-experiments-m02 iptables -D FORWARD -s 10.244.0.0/16 -j DROP -m comment --comment "cniresilience-drop" 2>/dev/null || true
```
*(Or click `[ Reset & Restore All Paths ]` on the `/project2` dashboard).*

### Clean Up Workload & Test Resources
```bash
kubectl delete deployment resilience-target
kubectl delete service resilience-service
kubectl delete cniresilience default-resilience
```

### Stop the Minikube Cluster
When finished with the demo:
```bash
minikube stop -p k8s-experiments
# Or completely delete:
# minikube delete -p k8s-experiments
```

---

## 8. Summary Checklist for Live Demo / Presentation

| Step | Action | Expected Visual in Dashboard (`/project2`) |
| :---: | :--- | :--- |
| **1** | Open `http://localhost:5173/project2` | Topology shows Calico & Flannel green; traffic at 100% |
| **2** | Start continuous curl client | Green heartbeat bars streaming in Quadrant 2 |
| **3** | Click `[ Sever Primary CNI ]` | Calico card turns RED; traffic hops to Flannel (`net1`) |
| **4** | Point to Quadrant 4 (EndpointSlices) | Amber `SHADOW SLICE (ACTIVE)` appears with `192.168.x.x` |
| **5** | Point to Quadrant 1 (Node Card) | Node shows `TAINTED: NoSchedule` during self-healing |
| **6** | Wait ~15 seconds | Calico restarts, green routes re-verified, state returns to `HEALTHY` |
