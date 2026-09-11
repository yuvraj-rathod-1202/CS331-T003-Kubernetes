# Tools

## Primary AI Tool: Antigravity IDE

**Tool used:** Antigravity IDE — with **Gemini 3.1 Pro** and **Claude Sonnet** models (switched between sessions).

**What it is:** Antigravity IDE is an AI-powered coding assistant deeply integrated into the development environment. It can read and edit files, execute shell commands, watch terminal output, interact with Kubernetes clusters, and assist with debugging and code generation in real-time.

**How I accessed it:** Through the Antigravity IDE interface on my macOS setup, directly connected to the local development environment where the project repository was open.

**Where I used it:** Primarily for my module, the **CoreDNS Remediation** module. I used it to:
- Set up local environment (Minikube, Calico CNI, Prometheus stack)
- Implement the CoreDNS controller logic (`operator/internal/controller/coredns/controller.go`)
- Debug and fix issues with the CoreDNS remediation operator (race conditions, predicate filters, cooldown logic)
- Fix CI/CD lint failures (GitHub Actions lint-test-build pipeline)
- Fix simulation bugs in the k8s-visualizer React/Vite frontend
- Diagnose `kubectl proxy` connection errors and CRD `404` errors
- Generate documentation and workflow diagrams (`operator/docs/coredns-remediation.md`)

---

## Prompt-Refiner Tool

**Tool:** [Prompt-Refiner](https://github.com/arpangupta1805/prompt-refiner) — a local CLI/pipeline tool I built.

**What it does:** Converts vague, short prompts into well-structured, high-quality prompts using prompt engineering techniques. It is integrated as a middleware layer: I type my rough intent → Prompt-Refiner rewrites it into a properly structured prompt → that refined prompt is passed to the AI model.

**Why it matters:** All the "vague prompts" listed in `Prompts.md` are what I actually typed. The refined versions are what the model actually received. This means the AI worked from structured, detailed prompts even when I phrased things casually, which significantly improved output quality and reduced back-and-forth correction rounds.

**Example:** My vague prompt *"I need to implement the CoreDNS remediation module..."* became a detailed multi-phase specification covering environment setup, architecture scaffolding, metrics to monitor, remediation strategies, and verification steps — all structured by the Prompt-Refiner automatically.

---

## Other Tools

- **ChatGPT (OpenAI):** Used occasionally for quick conceptual questions and Kubernetes internals reference (e.g., understanding Calico Felix behaviour and iptables rule propagation). Not used for any code generation in the operator codebase.
- **kubectl / Minikube / Helm:** Local cluster management and deployment tools used throughout testing.
- **golangci-lint:** Code quality enforcement (triggered via `make lint-config` and the GitHub Actions CI pipeline).
