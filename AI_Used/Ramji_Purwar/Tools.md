# Tools

**Tool Used:** Google Antigravity (Advanced Agentic Coding Assistant)

**Models Used:**
- Claude Sonnet 4.6 (Thinking / Extended Reasoning) — for operator module architecture, targeted remediation logic, unit test generation, and CI linter refactoring.
- Gemini 3.7 Flash — for file searching, codebase exploration, and command execution.

**What it is:**
Google Antigravity is an agentic AI coding environment and assistant. It can read, analyze, and edit files across the workspace, execute terminal and build commands, run unit tests, and assist in designing, implementing, and debugging code.

**How I accessed it:**
Running locally in my development workspace on Linux (`~/iitgn/CS331-CN-Project-1`), with direct integration into the repository and local environment.

**Where I used it:**
Only for my assigned part of the project — the **CNI (Container Network Interface) Self-Healing Module** in the Kubernetes Operator, including:
- Defining CRD fields in `cni_types.go` and generating manifests.
- Implementing the `Check -> Evaluate -> Remediate` controller logic for Calico CNI daemonsets, IPAM block monitoring, and stuck pods.
- Designing targeted remediation policies to prevent blanket restarts.
- Writing comprehensive unit tests in `controller_test.go`.
- Resolving `golangci-lint` issues (cyclomatic complexity, unused functions, constants, modernization) to pass GitHub Actions CI.
- Writing technical documentation for the CNI self-healing policy.
