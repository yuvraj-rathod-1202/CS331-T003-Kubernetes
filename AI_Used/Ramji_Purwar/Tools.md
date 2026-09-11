# Tools

**Primary Platform / Tool Used:** Google Antigravity (Advanced Agentic Coding Assistant)

---

## 1. AI Models Used
- **Claude Sonnet 4.6 (Thinking / Extended Reasoning):** Used for complex CNI operator module architecture, designing the targeted self-healing state pattern (`pendingTarget`), unit test generation with controller-runtime fake client mocks, and resolving CI `golangci-lint` violations.
- **Gemini 3.7 Flash:** Used for rapid workspace exploration, searching files, analyzing build logs, and running terminal commands.

---

## 2. Specialized Skills & Plugins Integrated

- **Graphify:**
  - *Purpose:* Codebase knowledge graph and architecture mapping.
  - *Usage:* Used to quickly navigate and understand the relationship between the central operator dispatcher, CRD definitions (`remediation.cn-operator...`), controller interfaces, and module packages across the repo without getting lost in the boilerplate.

- **Ponytail:**
  - *Purpose:* Pragmatic, minimalist coding and anti-over-engineering plugin (YAGNI & clean code philosophy).
  - *Usage:* Used to keep the CNI controller logic concise and robust—prioritizing standard library constructs, native `k8s.io` client methods, and minimal data structures rather than introducing redundant abstractions or boilerplate dependencies.

---

## 3. Access & Environment
- **How I accessed it:** Running locally in my development workspace on Linux (`~/iitgn/CS331-CN-Project-1`), integrated directly with the project repository, local Go toolchain, and Git.

---

## 4. Where I Used It
Strictly for my assigned part of the project — the **CNI (Container Network Interface) Self-Healing Module** in the Kubernetes Operator:
- Defining CRD fields in `cni_types.go` and generating manifests.
- Implementing the `Check -> Evaluate -> Remediate` controller logic for Calico CNI daemonsets, IPAM block monitoring, and stuck pods.
- Designing targeted remediation policies to prevent blanket restarts.
- Writing comprehensive unit tests in `controller_test.go`.
- Resolving `golangci-lint` issues (cyclomatic complexity, unused functions, constants, modernization) to pass GitHub Actions CI.
- Writing technical documentation for the CNI self-healing policy.
