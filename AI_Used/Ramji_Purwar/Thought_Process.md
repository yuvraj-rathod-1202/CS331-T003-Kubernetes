# Thought Process — Integrating AI into My Workflow

This document details how AI was integrated into my workflow for the **CNI (Container Network Interface) Self-Healing Module**, how technical decisions were made, and the division of responsibility between myself and the AI assistant.

---

## 1. Starting Context & Objectives

My assigned module in the Kubernetes Operator was the **CNI Module**. 

In Kubernetes, CNI plugins (specifically Calico in our cluster) manage pod networking, IP address allocation (IPAM), and sandbox creation. When CNI components fail—such as when `calico-node` crashes, IP pools become exhausted or disabled, or pods get stuck in `ContainerCreating` with `FailedCreatePodSandBox` errors—native Kubernetes self-healing does not resolve the underlying network layer failure.

### The Initial Basic Flows I Provided:
I framed the fundamental failure modes and self-healing flows:
1. **Calico DaemonSet Unreadiness Flow:** When a `calico-node` pod enters a CrashLoop or unready state, identify which node is affected and trigger a pod restart specifically for that unready pod.
2. **IPAM Exhaustion & Disabled Pool Flow:** When IP pools run out of allocatable addresses or are set to `disabled: true`, identify the affected pools, re-enable them, and evict the pods waiting for IPs.
3. **Stuck Sandbox Flow:** When application pods are stuck in `ContainerCreating` due to CNI sandbox errors, selectively evict them so they reschedule once the CNI recovers.

---

## 2. Workflow Discussion & Architectural Decision-Making

Before writing the implementation, we had a detailed discussion on how to translate these basic flows into a robust operator workflow:

### A. The Challenge with Blanket Remediation
- **Discussion:** In a standard operator reconcile loop, if `Remediate()` does not receive the granular failure signals from `Check()`, it risks doing a "blanket sweep" (e.g. deleting all Calico pods or re-enabling all pools indiscriminately).
- **Agreed Workflow Solution:** We designed the `pendingTarget` state pattern on the `CNIModule` struct. During `Evaluate()`, the module evaluates the collected signals, categorizes the action (`restart_calico_node`, `reenable_ippool`, `evict_stuck_pods`), and records the exact list of targets (`unreadyPods`, `disabledPools`, `stuckPods`). When `Remediate()` executes, it operates strictly on those targeted items.

### B. Dynamic Client Queries for Calico Custom Resources
- **Discussion:** Calico CRDs (`IPAMBlock`, `IPPool`) use dynamic group `crd.projectcalico.org`. We discussed how to safely query unstructured objects, extract block allocations, and calculate utilization percentages against configurable warning thresholds (e.g. 80%).
- **Implementation:** Following this discussion, the AI generated the unstructured client queries and helper parsers in Go.

### C. Mocking & Unit Testing Architecture
- **Discussion:** To ensure all edge cases were verified, we established test cases for each defined flow: healthy clusters, single node DaemonSet crash, high IPAM usage warning, disabled pool recovery, and stuck pod eviction.
- **Implementation:** The AI wrote the corresponding unit tests using `sigs.k8s.io/controller-runtime/pkg/client/fake`.

### D. CI Linter Refactoring
- **Discussion:** When GitHub Actions CI reported `golangci-lint` errors (`gocyclo` > 30 in `Remediate`, `unused`, `goconst`, `modernize`), we reviewed the lint output and agreed on modularizing the switch cases into individual sub-functions.
- **Implementation:** The AI refactored `Remediate()` into `remediateCalicoNodeRestart`, `remediateReenableIPPool`, and `remediateEvictStuckPods`, dropping cyclomatic complexity well below the threshold.

### E. Codebase Mapping & Anti-Over-Engineering (Graphify & Ponytail)
- **Graphify:** Used to trace the relationships between the operator dispatcher, CRD definitions, and module interfaces across the codebase without getting lost in boilerplate.
- **Ponytail:** Enforced a pragmatic, minimalist approach to avoid over-engineering—prioritizing standard library constructs and direct Kubernetes client calls rather than introducing speculative abstractions.

---

## 3. Division of Responsibility: What I Did vs. What AI Did

| Area | My Role (Ramji Purwar) | AI's Role (Antigravity) |
|---|---|---|
| **Conceptual Flows & Requirements** | Defined the basic failure flows (DaemonSet crash, IPAM exhaustion, disabled IPPools, stuck pods); established the rule for targeted, non-blanket remediation. | Participated in architectural discussions, analyzed trade-offs, and suggested the `pendingTarget` state pattern. |
| **Workflow Design & Architecture** | Led the discussion on how `Check`, `Evaluate`, and `Remediate` should interact; reviewed data structures and CRD spec parameters. | Structured the Go implementation, wrote CRD schema definitions, and implemented dynamic client queries. |
| **Code Implementation & Testing** | Reviewed all Go code line-by-line; executed unit tests locally (`go test ./internal/controller/cni/...`); verified test coverage. | Generated controller functions, unit test suites, and mock fixtures. |
| **CI & Quality Assurance** | Monitored GitHub Actions PR checks; provided CI failure logs; requested cleanups. | Refactored code to eliminate cyclomatic complexity and all `golangci-lint` issues. |
| **Documentation & Policy** | Directed the documentation of the CNI remediation workflow and verified policy completeness. | Drafted Mermaid state diagrams, flowcharts, and technical markdown documents. |

