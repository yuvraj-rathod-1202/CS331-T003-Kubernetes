# Presentation & Demo Guide

This guide covers **two separate projects**:

- **Part A: Project 1**, the network self-healing operator (dashboard page `/`).
- **Part B: Project 2**, IPAM efficiency (`/project1`) and CNI resilience (`/project2`).
  The page names follow the problem numbers inside Project 2, not the project numbers.

Start the dashboard with `cd k8s-visualizer && npm install && npm run dev` and open
`http://localhost:5173`.

---

# Part A: Project 1, Network Self-Healing Operator

Run the operator with `cd operator && go run ./cmd/main.go --controllers=networkremediation`
(with no flag only the Project 2 controllers run). Full setup:
[`experiments/demo_experiments.md`](experiments/demo_experiments.md).

## 1. Dashboard UI & Layout Recommendations

To make the demo clear, visual, and easy to follow during a presentation, the web dashboard should follow a **simple, terminal-inspired developer design** (dark mode, monospace typography, high contrast status indicators).

### Key UI Features

1. **Visual Cluster Topology (Left Panel)**:
   - **Nodes & Pods Grid**: Cards showing node status (`Ready` / `NotReady`) and child pods (`calico-node`, `coredns`, application/canary pods) with status badges (`Ready`, `Unready`, `Degraded`).
   - **Pod Connectivity Ring Indicator**: Visual edge connections between nodes (Green = Pingmesh probes passing, Red = Link broken).

2. **Interactive Chaos Action Panel (Top Right Panel)**:
   - Simple action buttons to trigger each failure experiment.
   - Toggle switch: `[ Enable Operator Auto-Healing: ON / OFF ]`.

3. **Terminal Console & Operator Log Stream (Bottom Right Panel)**:
   - **Command Console**: When a button is clicked, it shows the exact CLI command executed (e.g., `kubectl delete pod ...`) and its raw terminal output.
   - **Operator Log View**: Displays live logs in real-time as the operator responds.

---

## 2. Step-by-Step Demo Presentation Flow

---

### Step 0: Baseline Setup Demonstration
- **Dashboard View**: Show all nodes (`minikube`, `minikube-m02`) in `Ready` status, all CNI and CoreDNS pods running, and the active Pod-to-Pod connectivity link green.
- **Explanation**: Present the cluster baseline and introduce the 4 failure modules managed by the operator.

---

### Step 1: CNI Subsystem Failures

#### Demo A: Node Marked `NotReady` (CNI Crash)
1. **Operator Disabled (Native K8s Behavior)**:
   - **Action**: Click `[ Break CNI Node-2 ]` (simulates Calico CNI failure/crash on worker node).
   - **Dashboard View**: Node-2 transitions to `NotReady` or `CNI Unready`.
   - **Observation**: Show that native Kubernetes leaves the node in `NotReady` state indefinitely without auto-restarting the failing CNI container.
2. **Operator Enabled (Self-Healing)**:
   - **Action**: Click `[ Enable Operator ]` or re-run with operator active.
   - **Dashboard View**:
     - Operator log shows `Check` phase detecting unready `calico-node` pod.
     - `Evaluate` phase flags `Severity: Critical`.
     - `Remediate` phase automatically deletes/restarts the unready `calico-node` pod.
     - Node-2 status recovers to `Ready` on the dashboard.

#### Demo B: IPAM Pool Exhaustion
1. **Failure Injection**:
   - **Action**: Click `[ Exhaust IPAM Pool ]`.
   - **Dashboard View**: Pod creation fails with CNI IPAM errors (`No IPs available in pool`).
2. **Operator Response**:
   - **Show**: The operator detects 100% pool utilization and stuck pending pods.
   - **Alert Verification**: Show that the operator updates the Custom Resource status to `Degraded` and fires a system alert notification (preventing dangerous auto-deletion of workload pods).

---

### Step 2: Pod-to-Pod Connectivity Failures

#### Demo A: Intra-Node Connectivity Break (Local `veth` / CNI Bridge)
1. **Baseline**: Show `pod-a` pinging local canary endpoint on the same node successfully.
2. **Operator Disabled**:
   - **Action**: Click `[ Break Intra-Node veth ]` (drops local container virtual interface).
   - **Dashboard View**: Native Kubernetes still displays `pod-a` as `Running (1/1 Ready)`, but pings time out.
   - **Key Point**: Native Kubernetes readiness/liveness probes fail to detect intra-node virtual bridge drops.
3. **Operator Enabled**:
   - **Show**: Operator local probe detects local namespace failure $\to$ Evaluates `FailureTypeLocalCNI` $\to$ Restarts local CNI agent $\to$ Pod-to-pod traffic restored.

#### Demo B: Inter-Node Ring Connectivity Break (Overlay Tunnel / `tunl0`)
1. **Baseline**: Show cross-node ping between `node-1` pod and `node-2` pod (`Forward Edge: node-1 -> node-2`).
2. **Operator Disabled**:
   - **Action**: Click `[ Break Inter-Node Tunnel ]` (simulates IPIP/VXLAN overlay tunnel drop between nodes).
   - **Dashboard View**: Both nodes remain `Ready`, but cross-node pod traffic completely drops.
3. **Operator Enabled**:
   - **Show**:
     - Operator's $O(N)$ Pingmesh active ring detects edge drop ($node_1 \not\to node_2$).
     - **Three-Tier Triangulation**: Operator checks if $node_3 \to node_2$ passes to distinguish asymmetric path drop from total node crash.
     - **Remediation**: Operator restarts CNI overlay subsystem / taints degraded node $\to$ Topology link recovers to `Healthy`.

---

### Step 3: CoreDNS Failures & Degradation

#### Demo A: CoreDNS Pod Crash & Self-Healing
1. **Operator Disabled**:
   - **Action**: Click `[ Crash CoreDNS Pods ]`.
   - **Show**: Application DNS requests fail immediately.
2. **Operator Enabled**:
   - **Show**: Operator detects DNS resolution failure $\to$ Restarts/re-creates CoreDNS deployment $\to$ DNS resolution restored.

#### Demo B: CoreDNS Latency Spike
1. **Failure Injection**:
   - **Action**: Click `[ Inject CoreDNS Latency ]` (simulates CPU throttling / slow upstream response).
   - **Show**: DNS response latency spikes above configured threshold (e.g. > 500ms).
2. **Operator Response**:
   - **Show**: Operator detects latency breach in `Check` telemetry $\to$ Evaluates `Severity: Warning` $\to$ Triggers scale-up or pod rotation to restore normal DNS latency.

#### Demo C: External Egress Connectivity Failure
1. **Failure Injection**:
   - **Action**: Click `[ Block External DNS Egress ]`.
   - **Show**: Cluster internal DNS works, but external domain resolution (e.g. `google.com` or cloud endpoints) fails.
2. **Operator Response**:
   - **Show**: Operator's anchor corroboration probe identifies external egress failure $\to$ Logs root cause and applies remediation policy.

---

### Step 4: NetworkPolicy Enforcement Failures

#### Demo A: Broken Calico Felix Engine (Silent Policy Drift)
1. **Context Explanation**:
   - Kubernetes stores NetworkPolicy YAML definitions declaratively, but **Felix (Calico CNI agent)** enforces the actual `iptables`/`eBPF` rules.
2. **Operator Disabled**:
   - **Action**: Click `[ Break Felix Agent ]` (simulates Felix daemon crash or frozen socket).
   - **Dashboard View**: `kubectl get networkpolicy` still shows the policy as active/valid in Kubernetes, but restricted pods can now illegally communicate (policy enforcement silent failure).
3. **Operator Enabled**:
   - **Show**:
     - Operator probes policy enforcement boundaries.
     - Operator detects Felix engine drop / rule drift.
     - Operator restarts Felix / re-synchronizes policy engine $\to$ NetworkPolicy enforcement restored.
---

# Part B: Project 2, IPAM Efficiency & CNI Resilience

## Problem 1: Buddy-style dynamic IPAM demo (`/project1`)

`/project1` shows the live cluster: every block, address count and log line comes from the cluster
(read with kubectl by the dashboard's dev server). The demo uses a real workload: a Deployment of
real pods, so each pod takes a real address from its node's block. `/project1/sim` is an offline
simulation of the planner, for when no cluster is available.

### Setup
```bash
export KUBECONFIG=$HOME/buddy.kubeconfig                    # every terminal, including the dashboard's
kubectl apply -f operator/config/crd/bases/ && kubectl apply -f operator/config/samples/ipam_sample.yaml
kubectl apply -f operator/deploy/ipam-agent.yaml            # node agents
(cd operator && go run ./cmd --controllers=ipam --health-probe-bind-address=:18081 --metrics-bind-address=0)
(cd k8s-visualizer && npm run dev)                           # then open http://localhost:5173/project1
```
The header must show Cluster connected, Controller running, Node agents 3/3 and Pool Ready.

Demo settings used on the `buddy-ipam` cluster (cluster only; the repo defaults are unchanged):
- IPPool `cidrs: ["10.244.0.0/24"]`: a small pool of 256 addresses, so the demo can fill it. With the
  usual `/16` the nodes' capacity cap (120% of max pods: 720 addresses per node here, where nodes allow 600 pods, so about 2,160 for 3 nodes) stops growth
  long before the pool could run out.
- IPPool `initialBlockPercent: 5`, `shrinkCooldownSeconds: 20`, `quarantineSeconds: 10`,
  `shrinkThresholdPercent: 70` (a node gives space back while it stays at most 70% full; the default 50%
  keeps more spare room, which looks like waste in a short demo).
- Agent DaemonSet args: `--interval=5s --promote-timeout=15s --leak-min-age=20s --drain-settle=3s`.
The defaults in `config/samples` and `deploy/ipam-agent.yaml` are the safer production values.

Changing the pool CIDR on a running cluster (how the /24 was set, or to go back to the /16):
```bash
kubectl delete deployment -l app=ipam-demo                     # no demo pods may hold old addresses
kubectl patch ippool buddy-pool --type merge -p '{"spec":{"cidrs":["10.244.0.0/24"]}}'
kubectl delete nodeblocks --all                                # the controller gives every node a new first block
# once the new blocks are Active, restart pods that still hold old addresses (CoreDNS):
kubectl delete pod -n kube-system -l k8s-app=kube-dns
```

### The page
One screen, three panels:
- **Inject a fault** (left): **Fill the IP pool**, **Free the pool**, **Reset**, and steps that tick off
  as each stage is seen in the cluster.
- **IP addresses** (middle): the pool as one line, with each node's ranges in its colour, and every
  node's blocks with one square per address. Each node has a pods box and **Set**, to run exactly that
  many demo pods on it. Click a pod's square to stop that pod; click a free square to start a pod on
  that node (it takes the node's next free address).
- **What is happening** (right): every request and every change in the cluster, written as sentences.

### Demo flow (1–2 minutes)
1. **Fill the IP pool**: 150 real pods across the nodes. Nodes fill, the controller gives them more
   blocks (the free space next to their own block first, so ranges merge), the agents add routes, the
   blocks turn Active and all pods run.
2. **Free the pool**: the pods are removed and the controller takes the extra blocks back (drain, a
   short rest, back to the pool). Each node keeps its starting block.
3. **One node by hand**: set a node to, say, 29 pods and watch it grow by just what it needs; set it
   lower and watch it give back a whole block or the empty half of one.
4. To show a full pool, set the nodes high enough together (the pool holds 256 addresses): the pool
   turns **Full**, the extra pods wait for an address, and it recovers by itself when pods are removed.

Wait until each node is back to one block between runs (about a minute). **Reset** deletes all demo
pods.

## Problem 2: Dual-CNI resilience demo (`/project2`)

This page reads the live cluster; nothing is simulated. Set up the cluster first with
[`experiments/cni-resilience-experiment.md`](experiments/cni-resilience-experiment.md), then
run the operator (`cd operator && make run`) and the dashboard, and open `/project2`.

1. **Healthy start**: operator `Running`, phase `Healthy`, every node card shows its data
   path working, active path `primary`, and the traffic bars (one request per second from
   `traffic-client`) are all green.
2. **Block pod traffic** on a worker: about half the requests fail until the node is blamed
   (10–31 s). Then phase `FailedOver`, active path `backup`, a shadow EndpointSlice with the
   pod's Flannel (`net1`) address appears, the node gets the recovery taint, and all requests
   succeed again.
3. **Reset the lab**: the operator fails back within about 10 s with no failed request;
   shadow and taint are removed.
4. **Auto failover off**, then block again: the node is blamed and tainted but traffic is
   not moved, so about half the requests keep failing. This is what plain Kubernetes does.
5. **Delete calico-node pod**: the pod is replaced, no request fails and the operator does
   nothing, because existing routes stay in the kernel.
6. **Drop IPIP tunnel**: another primary-path fault (Calico's `tunl0`).
7. The **EndpointSlices** and **Observed changes** tabs show the shadow slices and every change the
   page saw in the cluster.

Measured results for all ten experiments, and manual faults (blocking the backup path too,
freezing a node), are in the experiment guide. Design: [`operator/docs/cni-resilience.md`](operator/docs/cni-resilience.md).
