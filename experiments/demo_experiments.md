# Complete Presentation & Demo Guide: 12 Network Failure Experiments

This document provides a comprehensive step-by-step setup and presentation guide for demonstrating all the experiments detailed in the project research report (`report/report.tex`).

---

## 1. Environment Setup (Before Running Web App & Experiments)

Follow these steps in order to set up your environment before opening the web app and executing the demo.

### Step 1: Start 2-Node Minikube Cluster with Calico CNI
Ensure Docker Desktop is running, then launch the 2-node cluster:

```bash
minikube start --nodes 2 --driver=docker --cni=calico --cpus=2 --memory=4096 -p k8s-experiments
```

Verify that both nodes and Calico CNI pods are ready:
```bash
kubectl get nodes
kubectl get pods -n kube-system -l k8s-app=calico-node
```

### Step 2: Install Custom Resource Definitions (CRDs) & Sample Policy
Install the `NetworkRemediation` CRD and apply the sample configuration:

```bash
# Apply CRD definitions
kubectl apply -k operator/config/crd

# Apply sample remediation specification
kubectl apply -f operator/config/samples/remediation_v1alpha1_networkremediation.yaml
```

Verify that the resource is created:
```bash
kubectl get networkremediations.remediation.cn-operator.yuvraj-rathod-1202.github.io
```

### Step 3: Deploy Test Workload Pods
Deploy the workload applications (`backend-api`, `frontend-probe`, `dns-checker`) spread across both nodes:

```bash
kubectl apply -f experiments/manifests/backend-api-deployment.yaml
kubectl apply -f experiments/manifests/frontend-probe-deployment.yaml
kubectl apply -f experiments/manifests/dns-checker-deployment.yaml
```

Verify all pods are running:
```bash
kubectl get pods -o wide
```

### Step 4: Start Kubernetes API Proxy (CRITICAL for Web App!)
The `k8s-visualizer` web app communicates with Kubernetes via `kubectl proxy` on port `8001`.

**In Terminal 1 (keep running in background):**
```bash
kubectl proxy --port=8001
```

### Step 5: Run the Network Self-Healing Operator
Start the Go operator controller manager:

**In Terminal 2 (keep running in background):**
```bash
cd operator
go run ./cmd/main.go
```

### Step 6: Start the Web App (`k8s-visualizer`)
Start the Vite development server for the visualizer dashboard:

**In Terminal 3:**
```bash
cd k8s-visualizer
npm install
npm run dev
```

Open your browser at `http://localhost:5173`. You will see the cluster topology, node/pod status grid, operator module health, and real experiment action controls!

---

## 2. Overview of Experiments

| Module | Experiment Name | Fault Injected | Native K8s Behavior | Operator Auto-Remediation |
|---|---|---|---|---|
| **CNI** | `calico-node` Agent Crash | Delete `calico-node` pod on worker | Node marked `NotReady`; pods stuck | Restarts `calico-node` DaemonSet pod |
| **CNI** | IPAM Pool Exhaustion | Allocate all IPs in subnet pool | New pods stuck in `ContainerCreating` | Detects IPAM threshold breach; alerts & evicts stuck pods with anti-churn cooldown |
| **CNI** | IPPool Disabled | Set `spec.disabled: true` on IPPool | All new IP allocations fail cluster-wide | Patches IPPool `spec.disabled: false` |
| **CoreDNS** | Scale Replicas to Zero | `kubectl scale deployment coredns --replicas=0` | Cluster DNS completely down | Patches deployment replicas back to expected count (2) |
| **CoreDNS** | CPU Throttling Latency Spike | Set CoreDNS CPU limit to `5m` | DNS query latency spikes >1000ms | Detects latency breach; resets CPU limit to 100m |
| **CoreDNS** | Corrupted Upstream Forward | Point forward address to RFC 5737 bad IP | External domain resolution fails | Repairs Corefile ConfigMap & triggers rolling restart |
| **NetPolicy** | Felix Agent Crash (Policy Drift) | Crash Felix while policy applied | K8s shows policy applied, but rules un-enforced | Restarts Felix agent; reprograms kernel iptables/ipset |
| **PodConn** | Local `veth` Interface Down | `ip link set vethXXXX down` | Pod appears `1/1 Ready`, but traffic drops | Tier 1 Triangulation: Restarts local CNI subsystem |
| **PodConn** | IPIP Tunnel Interface Crash | `ip link set tunl0 down` | Cross-node overlay pod traffic drops | Tier 2 Triangulation: Resets `tunl0` & CNI agent |
| **PodConn** | Host `iptables` FORWARD DROP | `iptables -I FORWARD -j DROP` | Silently drops forwarded pod packets | Ring prober fails; restarts CNI agent / taints node |
| **PodConn** | Inter-Node Overlay Break | Drop IPIP/VXLAN port between nodes | Pod ping times out across nodes | Pingmesh Ring Triangulation: Isolates path & heals link |

---

## 3. Detailed Step-by-Step Demo Flow

### Initial Setup & Baseline View
1. Open the **k8s-visualizer** web app (`http://localhost:5173`).
2. Verify all cluster nodes (`minikube`, `minikube-m02`) show **Ready**.
3. Verify all 4 operator modules (`CNI`, `CoreDNS`, `NetPolicy`, `PodConn`) show **Healthy / Active**.

---

### CNI Subsystem Experiments

#### **Experiment: `calico-node` Agent Crash**
* **Goal**: Show that when a node's CNI agent crashes, new pods fail to schedule and the node becomes unready without native K8s auto-recovery.
* **Demo Steps**:
  1. **Disable Operator** or show native behavior first: Click `[ Kill Calico Pod ]` on Node-2.
  2. **Observe Native K8s**: Node-2 transitions to `NotReady` or `CNI Unready`. Pods scheduled on Node-2 remain stuck. Native K8s does not auto-recover IP/interface setup.
  3. **Enable Operator**: The operator's CNI Check phase detects `numberReady < desiredNumberScheduled`, evaluates `Severity: Critical`, deletes the unready CNI pod, triggering a fresh DaemonSet respawn.
  4. **Outcome**: Node returns to `Ready` status automatically within 30–45s.

#### **Experiment: IPAM Pool Exhaustion**
* **Goal**: Show that native Kubernetes scheduler has zero awareness of IP subnet capacity when IPAM pool is full.
* **Demo Steps**:
  1. **Inject Fault**: Deploy pods until the `/28` IPPool has 0 free IPs remaining.
  2. **Observe Native K8s**: Subsequent pod creations stay stuck in `ContainerCreating` with warning event `FailedCreatePodSandBox: No IPs available in pools`.
  3. **Operator Response**: The CNI module monitors IPAM block utilization, detects threshold/exhaustion breaches, updates CR status to `Degraded`, and evicts stuck workload pods while throttling re-evictions per workload (`EvictionCooldownSeconds`) to prevent ReplicaSet churn.

#### **Experiment: IPPool Disabled (`spec.disabled: true`)**
* **Goal**: Show recovery from an inadvertently disabled Calico IPPool.
* **Demo Steps**:
  1. **Inject Fault**: Run `kubectl patch ippool default-ipv4-ippool --type=merge -p '{"spec":{"disabled":true}}'`.
  2. **Observe Native K8s**: New pods fail IP allocation cluster-wide. K8s leaves the IPPool disabled.
  3. **Operator Response**: The CNI evaluator checks for `disabled: true` IPPools, patches `spec.disabled` back to `false`, unblocking IP allocation cluster-wide.

---

### CoreDNS Subsystem Experiments

#### **Experiment: Scale Replicas to Zero**
* **Goal**: Demonstrate recovery when CoreDNS replicas are accidentally scaled to 0.
* **Demo Steps**:
  1. **Inject Fault**: Scale CoreDNS deployment down to 0 replicas (`kubectl scale deployment coredns -n kube-system --replicas=0`).
  2. **Observe Native K8s**: Cluster DNS completely down. All inter-pod requests and external lookups fail immediately.
  3. **Operator Response**: CoreDNS Check phase flags `availableReplicas (0) < expectedReplicas (2)`. Operator automatically patches deployment replicas back to `2`.

#### **Experiment: CPU Throttling Latency Spike ($\le$5m)**
* **Goal**: Show detection and remediation of DNS query latency degradation caused by CPU starvation.
* **Demo Steps**:
  1. **Inject Fault**: Patch CoreDNS deployment CPU limit to `5m` (`kubectl set resources deployment coredns -n kube-system --limits=cpu=5m`).
  2. **Observe Native K8s**: CoreDNS pods remain `Running (1/1 Ready)`, but DNS query duration spikes from ~2ms to >1000ms.
  3. **Operator Response**: CoreDNS Check phase measures average query duration over 1 min, evaluates metric breach (>150ms threshold), and resets CPU limits to baseline `100m`.

#### **Experiment: Corrupted Upstream Forwarding**
* **Goal**: Demonstrate repair of corrupted external DNS forwarding in Corefile.
* **Demo Steps**:
  1. **Inject Fault**: Edit `coredns` ConfigMap to forward external queries to unroutable IP `192.0.2.1` (RFC 5737).
  2. **Observe Native K8s**: Internal DNS works, but external name resolution (`ping google.com`) hangs and fails.
  3. **Operator Response**: Operator parses Corefile `forward` directives, detects RFC 5737 invalid upstream subnet, replaces it with fallback `/etc/resolv.conf`, and performs a rolling restart.

---

### Pod-to-Pod Connectivity Experiments

#### **Experiment 1: Local `veth` Interface Down (Intra-Node Failure)**
* **Goal**: Isolate and fix local container virtual interface drops on a single node.
* **Demo Steps**:
  1. **Select Pod**: Click on any workload pod (e.g. `dns-checker` or `frontend-probe`) in the visualizer graph.
  2. **Inject Fault**: In the control panel under **4. Pod Connectivity**, click **`[ Down veth (calixxx) ]`**.
  3. **Observe Native K8s**: Pod remains `1/1 Ready` in Kubernetes, but local traffic to/from that pod drops. The visualizer shows the warning badge `veth: DOWN (calixxx) ⚠️`.
  4. **Operator Response**: **Tier 1 Triangulation**: Operator local probe fails while external gateway anchors pass $\to$ Diagnoses `FailureTypeLocalCNI` $\to$ Restarts local CNI agent to rebuild the veth interface and restore routing.
  5. **Manual Restore**: Click **`[ Up veth (calixxx) ]`** to bring the interface back online immediately.

#### **Experiment 2: IPIP Tunnel Interface Crash (`tunl0` Down - Inter-Node Failure)**
* **Goal**: Detect and recover broken overlay tunnels across multi-node topology.
* **Demo Steps**:
  1. **Select Pod**: Click on any pod hosted on Node-1 or Node-2.
  2. **Inject Fault**: In the control panel under **4. Pod Connectivity**, click **`[ Down tunl0 ]`**.
  3. **Observe Native K8s**: Both nodes remain `Ready`. Intra-node traffic on each node continues to work, but all cross-node pod traffic across nodes drops with 100% packet loss.
  4. **Operator Response**: **Tier 2 Triangulation**: Operator Pingmesh ring prober detects cross-node drop ($node_1 \not\to node_2$), isolates `tunl0` tunnel failure (`FailureTypeTunnelCrash`), and restarts CNI overlay subsystem to re-establish the tunnel interface.
  5. **Manual Restore**: Click **`[ Up tunl0 ]`** to bring the tunnel interface back online immediately.