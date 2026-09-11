# Step-by-Step Details: How AI Contributed at Each Stage - Viraj Solanki

This document outlines the step-by-step progression of how AI was used to research, dissect, and understand the **Kubernetes Network Self-Healing Operator** codebase.

---

## Step 1: Project Discovery & Context Mapping
* **Action**: Provided high-level queries about repository purpose and overarching architecture.
* **AI Contribution**: 
  - Read `PROJECT.md`, `README.md`, and `DEMO_GUIDE.md`.
  - Mapped the four target failure subsystems (CNI, CoreDNS, Pod Connectivity, NetworkPolicy).
  - Outlined the common 3-Phase Module Pipeline (`Check` $\to$ `Evaluate` $\to$ `Remediate`).

---

## Step 2: Breaking Down Core Primitives
* **Action**: Clarified the roles of Nodes, Calico CNI, and Kubernetes Operators.
* **AI Contribution**:
  - Explained that Nodes are physical/VM hosts running Kubelet and container runtimes.
  - Explained that Calico is the CNI engine responsible for IPAM, `veth` interfaces, BGP routing, and Felix `iptables`/`eBPF` firewall rules.
  - Defined Operators as custom control loops extending Kubernetes with domain-specific automation.

---

## Step 3: Deep Dive into Module 1 — CNI Subsystem
* **Action**: Inquired about CNI failure modes, root causes, and remediation logic.
* **AI Contribution**:
  - Traced `calico-node` DaemonSet unreadiness, IPAM pool exhaustion, and stuck `ContainerCreating` pods.
  - Inspected [`operator/internal/controller/cni/controller.go`](../../operator/internal/controller/cni/controller.go) to explain:
    1. `checkCalicoDaemonSet()` & `checkIPAMUsage()` (Telemetry gathering).
    2. Threshold evaluation ($>80\%$ warning vs. $100\%$ critical exhaustion).
    3. Targeted remediation actions (`ActionRestartCalicoNode`, `ActionReenableIPPool`, `ActionEvictStuckPods`).

---

## Step 4: Deep Dive into Module 2 — CoreDNS Subsystem
* **Action**: Explored DNS resolution degradation, CPU throttling, and configuration corruption.
* **AI Contribution**:
  - Analyzed [`operator/internal/controller/coredns/controller.go`](../../operator/internal/controller/coredns/controller.go).
  - Explained how scale-to-zero is auto-scaled back to 2 replicas, how regex detects test IPs (`192.0.2.1`) in `Corefile` forward directives, how Prometheus latency metrics ($>150\text{ms}$) trigger resource restorations (`100m CPU`), and how a 30-second cooldown prevents restart-thrashing.

---

## Step 5: Deep Dive into Module 3 — Pod-to-Pod Connectivity
* **Action**: Inquired why Kubernetes liveness/readiness probes fail to detect broken tunnels or virtual bridges, and how our operator detects and heals them.
* **AI Contribution**:
  - Walked through [`operator/internal/controller/podconnectivity/`](../../operator/internal/controller/podconnectivity/).
  - Explained the $O(1)$ Intra-Node Canary, the $O(N)$ Inter-Node Pingmesh Ring, and the Three-Tier Pattern Triangulation algorithm (`triangulation.go`).
  - Explained the 3-Tier progressive hierarchical remediation:
    $$\text{Tier 1: CNI Restart} \longrightarrow \text{Tier 2: Node Taint/Cordon} \longrightarrow \text{Tier 3: Node Drain/Workload Evacuation}$$

---

## Step 6: Deep Dive into Module 4 — NetworkPolicy Subsystem
* **Action**: Explored declarative policy storage vs. Felix data-plane enforcement.
* **AI Contribution**:
  - Inspected [`operator/internal/controller/networkpolicy/controller.go`](../../operator/internal/controller/networkpolicy/controller.go).
  - Explained how Felix crashes lead to silent policy drift, and how the operator detects failing enforcement pods and restarts them to resynchronize kernel `iptables`/`eBPF` firewall rules.

---

## Step 7: Architecture & Dispatcher Reconciler Synthesis
* **Action**: Requested a complete explanation of the `NetworkRemediation` CRD and Dispatcher controller.
* **AI Contribution**:
  - Analyzed [`operator/api/v1alpha1/networkremediation_types.go`](../../operator/api/v1alpha1/networkremediation_types.go) and [`operator/internal/controller/networkremediation_controller.go`](../../operator/internal/controller/networkremediation_controller.go).
  - Mapped how the Dispatcher watches the single CR, dynamically iterates through enabled modules, runs the 3-phase pipeline, and updates `.status.phase` and module messages every 30 seconds.

