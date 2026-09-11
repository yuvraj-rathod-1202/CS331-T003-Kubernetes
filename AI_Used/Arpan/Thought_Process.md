# Thought Process — How I Integrated AI into My Workflow

I am being honest here about what the AI did and what I did.

---

## Starting Point

Our team is building a Kubernetes operator for *"Extending Kubernetes Self-Healing for Network Failures."* My assigned module is **CoreDNS Remediation**, which focuses on detecting and remediating DNS resolution failures within a Kubernetes cluster — specifically when CoreDNS pods become unhealthy, misconfigured, or resource-starved.

---

## How I Used the AI

I used **Antigravity IDE** (switching between Gemini 3.1 Pro and Claude Sonnet models) as an interactive pair programmer throughout the development of the CoreDNS module. I also used a local **Prompt-Refiner** tool (see `Tools.md`) to convert my rough intent into structured prompts before passing them to the AI.

### Phase 1 — Setup & Architecture (Session 1, early turns)
My first prompt was a high-level description of the project and what the CoreDNS module needed to do. After the Prompt-Refiner structured it, the AI:
- Verified my local toolchain (Docker, kubectl, Minikube, Helm, Go)
- Helped set up a local Kubernetes cluster with Calico CNI
- Deployed the Prometheus + kube-prometheus-stack for CoreDNS metrics scraping

**My role:** I described the architecture goals, made the decision to use Go + controller-runtime (not Python/kopf), and manually ran every command to confirm the environment was working.

### Phase 2 — CoreDNS Controller Implementation (Session 1, mid turns)
The AI generated the reconciliation loop, CoreDNS health-check logic, and remediation actions (pod restarts, ConfigMap validation, replica scale-up). I then asked it to review concurrency issues.

**What I caught:** I noticed the AI's initial design had a potential race condition — the ConfigMap patch and the rolling restart annotation were submitted without waiting for commit confirmation, which could cause pods to restart and reload an old corrupted Corefile. I described this issue and the AI fixed it. I also flagged that there was no cooldown/backoff on the remediation loop, which could cause infinite restart loops on bad nodes.

**My role:** I identified these architectural issues from my own understanding of Kubernetes propagation lag. The AI fixed them after I pointed them out.

### Phase 3 — CI/CD & Lint Fixes (Session 1)
The GitHub Actions `lint-test-build` pipeline was failing. I asked the AI to fix `make lint-config`. The AI ran `golangci-lint` across the codebase and fixed unused variables, missing error checks, and formatting issues.

**My role:** I triggered and monitored the GitHub Actions workflow and gave the AI the failure output.

### Phase 4 — Web App Visualizer Debugging (Session 1, late turns)
A team member's component — the `k8s-visualizer` React/Vite web dashboard — was failing to start. Issues included:
- Missing `node_modules` (not tracked in Git) — fixed by running `npm install`
- `kubectl proxy` connecting to a stale port from a previous Minikube session — fixed by restarting Minikube
- The web app's simulation state machine had bugs: the CNI agent pod was returning to Ready state even when the operator was disabled (incorrect simulation logic)
- A CRD `404` error when toggling the operator: `networkremediations "networkremediation-sample" not found` — this was because the CRD resource name in the UI didn't match the deployed CR name

**My role:** I was new to the web app and described observed behavior step by step. The AI diagnosed each issue and proposed fixes. I ran the commands and reported results back.

### Phase 5 — NetworkPolicy Testing (Session 1, final turns)
A team member assigned me to test the NetworkPolicy enforcement module. The AI walked me through a full testing procedure — disabling the operator, injecting a Felix (Calico CNI) crash, observing policy drift, then re-enabling the operator and verifying remediation.

**My role:** I executed each step and reported the terminal logs and visual outputs back to the AI so it could confirm whether the behavior was correct.

### Phase 6 — Documentation (Session 2)
I needed a documentation file for the CoreDNS module with a workflow diagram. The AI generated `operator/docs/coredns-remediation.md` with:
- Failure mode descriptions (replica loss, corrupted Corefile, CPU throttling, CrashLoopBackOff)
- A Mermaid flowchart showing the full detection-to-remediation pipeline

**My role:** I reviewed the documentation for accuracy and confirmed it matched the actual implementation.

---

## What I Decided / Did vs. What the AI Did

| Area | My contribution | AI's contribution |
|------|----------------|-------------------|
| Architecture | Defined the CoreDNS remediation strategy and reconciliation approach | Generated scaffolding code and initial reconciliation loop |
| Bug identification | Spotted the ConfigMap race condition and missing cooldown logic myself | Fixed the issues after I described them |
| Cluster management | Set up and managed the local Minikube cluster, ran all kubectl/helm commands | N/A — cannot interact with my cluster |
| CI/CD | Monitored GitHub Actions, provided failure output | Fixed lint errors in the codebase |
| Visualizer | Tested UI behavior, reported bugs, verified fixes | Diagnosed and fixed simulation logic bugs |
| Testing | Executed all test steps manually in my cluster | Provided testing procedure and explained expected behavior |
| Documentation | Reviewed and verified accuracy | Generated the markdown and Mermaid diagrams |

---

## Why I Used It

To accelerate the implementation of a complex Go-based Kubernetes operator. The CoreDNS module involves deep understanding of Kubernetes controller-runtime patterns, DNS networking internals, and Calico CNI behavior. The AI served as an expert pair programmer — helping me write boilerplate, debug subtle controller-runtime issues, and generate documentation — while I focused on architecture decisions, testing, and ensuring the operator worked correctly in my local cluster.

The **Prompt-Refiner** was key to getting high-quality responses from the start rather than spending many rounds correcting misunderstood requests.
