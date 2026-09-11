# Prompts Log - Viraj Solanki

This document records the exact sequence of prompts given to the AI assistant to explore, dissect, and comprehend every component of the **Kubernetes Network Self-Healing Operator** project:

---

### Prompt 1: Project Overview & Motivation
```text
what is this project is about
```
* **Intent**: Understand the high-level purpose of the repository, the core problem it solves, the tools involved (Go, Kubebuilder, Calico, Minikube), and the 4 failure domains.

---

### Prompt 2: Core Primitives & Vocabulary Clarification
```text
what is nodes, caligo, operator.
what are the four core system and what their use
```
* **Intent**: Establish clear definitions of fundamental Kubernetes concepts (Nodes, Calico CNI, Operators/CRDs) and summarize the use cases of the 4 core subsystems.

---

### Prompt 3: CNI Module Deep Dive
```text
okay, we will go for CNI module first , everything about it.
what was ,and what we have done
```
* **Intent**: Analyze the CNI module's architecture, including what failure states existed previously and what logic was implemented in Go.

---

### Prompt 4: CNI Failure Modes
```text
okay, what are the CNI failures , and why it happening
```
* **Intent**: Unpack the root causes of CNI agent crashes, IPAM pool exhaustion, and stuck `ContainerCreating` sandbox states.

---

### Prompt 5: CNI Remediation Solutions
```text
and how we fixed that
```
* **Intent**: Understand how the operator's Check $\to$ Evaluate $\to$ Remediate pipeline repairs CNI failures and why targeted branching avoids collateral damage.

---

### Prompt 6: CoreDNS Module & Resolution Failures
```text
second module (DNS) and its failure, solutioons
```
* **Intent**: Understand CoreDNS operations, scale-to-zero issues, CPU throttling latency spikes, corrupted ConfigMap forward directives, and the operator's auto-healing actions.

---

### Prompt 7: Pod-to-Pod Connectivity & Triangulation
```text
third module pod connectivity , and its failure , solution
```
* **Intent**: Explore why native Kubernetes misses virtual interface (`veth`) and overlay tunnel (`tunl0`) drops, and how $O(N)$ Pingmesh rings, 3-tier triangulation, and progressive remediation solve this.

---

### Prompt 8: Custom Operator CRD Deep Dive
```text
everything about our custom operator CRD and, how it is working and how it is helpfull in every module
```
* **Intent**: Understand the schema, design rationale, and benefits of the unified `NetworkRemediation` CRD across all 4 modules.

---

### Prompt 9: Dispatcher Reconciler & Failure Detection
```text
what is NetworkRemediat and dipatcher . how are we detecting the failure
```
* **Intent**: Clarify how the Dispatcher loop orchestrates module execution and understand the exact telemetry mechanisms used to detect failures.

---

### Prompt 10: NetworkPolicy Module & Silent Drift
```text
next module, understanding network policy"
```
* **Intent**: Learn why Kubernetes decouples policy declaration from Felix enforcement, how silent policy drift occurs, and how the operator resynchronizes kernel firewall rules.

