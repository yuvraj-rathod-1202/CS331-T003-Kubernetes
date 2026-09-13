# Thought Process & AI Integration Workflow - Viraj Solanki

## Context & Objectives
In this project, my objective was to **deeply learn and master the full architectural, theoretical, and implementation details of the Kubernetes Network Self-Healing Operator built by the team**. Rather than writing code blindly, I used AI as an expert pair-programming tutor and architectural researcher to explore the codebase, understand Kubernetes networking internals, and master the failure modes and remediation algorithms.

---

## Strategic Thought Process While Using AI

```
                        ┌──────────────────────────────┐
                        │   1. Establish Core Domain   │
                        │      Scope & Vocabulary      │
                        └──────────────┬───────────────┘
                                       │
                                       ▼
                        ┌──────────────────────────────┐
                        │   2. Modular Failure Mode    │
                        │      Root Cause Analysis     │
                        └──────────────┬───────────────┘
                                       │
                                       ▼
                        ┌──────────────────────────────┐
                        │   3. Code-Level Inspection   │
                        │   (Types, Logic, Tests)      │
                        └──────────────┬───────────────┘
                                       │
                                       ▼
                        ┌──────────────────────────────┐
                        │   4. System-Wide Integration │
                        │      (CRD & Reconciler)      │
                        └──────────────────────────────┘
```

### 1. Conceptual Framing First
Before diving into hundreds of lines of Go code, I prompted the AI to break down the high-level concepts into digestible building blocks:
- *What is a Node, what is Calico CNI, and what is an Operator?*
- *Why does native Kubernetes self-healing fall short when network-layer components degrade?*

### 2. Socratic Exploration: "What Was Broken" vs. "How We Fixed It"
For every single module (CNI, CoreDNS, Pod Connectivity, NetworkPolicy), I enforced a strict two-stage inquiry:
- **Phase A ("What Was / Failure")**: Identify the exact failure mode, why it happens in Linux/Kubernetes, and why native Kubernetes fails to detect or heal it.
- **Phase B ("What We Did / Fix")**: Analyze the exact Go implementation in the repository, tracing the signal from telemetry gathering (`Check`) to severity classification (`Evaluate`) and corrective execution (`Remediate`).

### 3. Rigorous Code Verification
Whenever AI explained a feature, I ensured it tied directly back to:
- The actual CRD types in `operator/api/v1alpha1/`.
- The controller algorithms in `operator/internal/controller/`.
- The unit test scenarios in `operator/internal/controller/*/*_test.go`.
- The manual chaos experiments in `experiments/`.

### 4. Synthesizing the Central Control Plane
Finally, I explored the unifying architecture: how the single `NetworkRemediation` CR acts as a declarative specification while the Dispatcher controller orchestrates the reconciliation loop cleanly and safely.

