# Kubernetes Network Self-Healing Operator - Presentation & Demo

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