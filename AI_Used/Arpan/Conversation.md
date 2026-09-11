# Conversation Transcript (Curated)

> **Note on Prompt-Refiner:** I use a local tool [Prompt-Refiner](https://github.com/arpangupta1805/prompt-refiner) that converts my vague prompts into structured, high-quality prompts before passing them to the AI. In each exchange below, the **vague prompt** is what I actually typed; the **refined/passed prompt** is what the model received.

---

## Session 1: Kubernetes DNS Monitoring Setup

---

### Turn 1 — Initial Setup & Module Implementation

**Me (vague):**
> I need to implement the CoreDNS remediation module for our Kubernetes self-healing operator project. Help me build the controller logic in Go using controller-runtime.

**Passed to AI (after Prompt-Refiner):**
> # Goal: Local Environment Setup & CoreDNS Health Monitoring Component Initialization
>
> ## Context
> We are building a custom Kubernetes Self-Healing Network Operator/Controller to detect and remediate networking failures (CNI crashes, DNS issues, connectivity drops, and broken NetworkPolicies). The assigned component is **DNS Health Monitoring & Remediation (CoreDNS)**. Project workspace: `/Users/arpangupta/Desktop/Sem5/Computer Network/CS331-CN-Project-1`.
>
> ## Phase 1: Local Environment & Cluster Setup
> 1. Verify Prerequisites & Local Tooling (docker, kubectl, minikube/kind, helm, Go+kubebuilder)
> 2. Initialize Local Kubernetes Cluster (Minikube or KIND with standard networking)
> 3. Deploy Observability Stack (Prometheus via kube-prometheus-stack, verify CoreDNS targets are UP)
>
> ## Phase 2: Project Architecture & Scaffolding for CoreDNS Module
> 1. Scaffold Project Directory Structure + dependency management (go.mod)
> 2. Define DNS Health Specifications: metrics to monitor (crash loops, query latency, SERVFAIL/NXDOMAIN rates, in-cluster probe results), remediation strategies (pod restart, ConfigMap restore, replica scale-up)
> 3. Create Scaffolding Code & Configuration (RBAC definitions, controller skeleton)
>
> ## Phase 3: Verification & Smoke Test
> 1. Deploy a test probe pod, run sample DNS lookups, simulate a DNS fault (scale CoreDNS to 0), verify detection
> 2. Update README.md with local setup instructions

**AI:** Generated an implementation plan covering all three phases. Set up Minikube with Calico CNI, deployed the Prometheus stack, scaffolded the operator directory structure with RBAC definitions, and provided the initial CoreDNS controller skeleton with the Reconcile loop, CoreDNS pod watches, and health-check logic.

---

### Turn 2 — Completeness Check

**Me:**
> So basically the complete CoreDNS according to this is completed na? [pasted full project description]

**AI:** Compared the implementation against the project specification. Confirmed that all core requirements were covered: DNS failure detection, automatic remediation, health monitoring (replica loss, ConfigMap corruption, CPU throttling, CrashLoopBackOff states), and Prometheus metrics integration. Suggested minor improvements for edge case handling in the reconciliation logic.

---

### Turn 3 — Race Condition & Missing Cooldown

**Me:**
> Rolling restart racing the fix: when you patch the ConfigMap and immediately annotate for rolling restart, make sure the annotation-triggered restart happens after the ConfigMap write is confirmed committed — otherwise pods can restart and reload the old corrupted Corefile if there's any propagation lag.
> Remediation loops: nothing mentions a cooldown/backoff on Remediate(). If CoreDNS keeps crashing after a restart (bad node, not a config issue), does it loop infinitely?

**AI:** Fixed the ConfigMap-patch-then-restart ordering to wait for the ConfigMap write to be committed before triggering the rolling restart. Added a cooldown/backoff mechanism to the remediation loop to prevent infinite restart spirals on persistently failing nodes.

---

### Turn 4 — CI/CD Lint Failure

**Me:**
> Run make lint-config. This is failing — we have lint-test-build in GitHub that is failing.

**AI:** Ran `golangci-lint` across the operator codebase. Found and fixed: unused variables, missing error checks, and formatting issues. Confirmed the `make lint-config` target was passing locally.

---

### Turn 5 — Web App Visualizer Startup Error

**Me:**
> I am getting this error when trying to run the web app visualizer. Don't change any codebase because it is running on other devices as it is. Don't do the build part, I want only the dev one. [pasted terminal showing `kubectl proxy` connection refused errors and `@vitejs/plugin-react` missing error]

**AI:**
Diagnosed two separate issues:
1. **`@vitejs/plugin-react` missing**: `git pull` brought down the `k8s-visualizer` project but `node_modules` is gitignored — ran `npm install` inside `k8s-visualizer/` to install all 82 packages.
2. **`kubectl proxy` connection refused on port 49524**: Minikube had restarted and re-synced to a new API server port (`53433`). The old proxy session was pointing to a stale port. Restarted `kubectl proxy` — it then served cleanly on `http://127.0.0.1:8001`.

How to run the visualizer in dev mode:
```bash
# Terminal 1
kubectl proxy
# Terminal 2
cd k8s-visualizer && npm run dev
# Open http://localhost:5173
```

---

### Turn 6 — NetworkPolicy Testing Guide

**Me:**
> My team member has given me the work of testing. Explain me properly how I can test it in the web app. There is the module NetworkPolicy in which we have found one problem in Kubernetes and using our operator we have fixed the problem. I have to check: without the operator — what is the problem and what is showing in the web app, and is that correct; and with the operator — how it is fixed and is it correctly fixed.

**AI:** Provided a full step-by-step testing guide for the **NetworkPolicy Enforcement Module**:

**Architecture explanation:** In Kubernetes, `NetworkPolicy` resources are stored in etcd but enforced by Calico's Felix daemon on each node via `iptables`. If Felix crashes or enters `CrashLoopBackOff`, the kernel rules freeze — new policies are silently ignored while `kubectl get networkpolicy` still reports them as valid.

**Part A — Testing WITHOUT the operator (observing the problem):**
1. Keep operator OFF (do not run `go run cmd/main.go`)
2. In the visualizer → Module 3 (NetworkPolicy Enforcement) → Apply Deny-All Policy to `backend-api`
3. Select the `calico-node` pod on the node → Kill Felix Agent
4. During the ~30s Felix restart window — traffic that should be blocked will still flow (policy drift)
5. The visualizer should show Felix in degraded/restarting state and flag the policy enforcement gap

**Part B — Testing WITH the operator (verifying remediation):**
1. Enable the operator via the toggle in the visualizer
2. Repeat the steps above
3. The operator should detect Felix has restarted, re-verify that all NetworkPolicies are still being enforced by the kernel, and trigger remediation if it detects a drift

---

### Turn 7 — CRD 404 Error

**Me:**
> Failed to toggle operator: `networkremediations.remediation.cn-operator.yuvraj-rathod-1202.github.io "networkremediation-sample" not found` (404). Why is this not working?

**AI:** The 404 error means the `NetworkRemediation` CR (custom resource instance) named `networkremediation-sample` does not exist in the cluster — the CRD type is registered but no instance has been applied. Fixed by applying the sample CR manifest:
```bash
kubectl apply -f operator/config/samples/remediation_v1alpha1_networkremediation.yaml
```
Also noted the deployed CR was named `cluster-network-remediation`, not `networkremediation-sample` — updated the visualizer's API call to use the correct name.

---

### Turn 8 — Understanding the Web App

**Me:**
> I am new to this web app. I don't understand it. Please explain properly. I understand till Part A step 3-b. I am not able to find "Kill CNI / Felix Agent." What is this?

**AI:** Explained that "Kill CNI / Felix Agent" means deleting the `calico-node` pod on a specific node to simulate a CNI/Felix agent crash:
```bash
kubectl delete pod calico-node-<hash> -n kube-system
```
When this pod is deleted, Kubernetes will restart it automatically (usually in ~30 seconds), but during that window Felix is not enforcing NetworkPolicies — this is the policy drift window the operator is designed to catch.

---

### Turn 9 — Operator Log Analysis

**Me:**
> In the terminal see there are several execution fail debug statements. Check whether there is any issue in that or what. [pasted logs showing `INFO Remediation failed: Auto remediation disabled by policy for node k8s-experiments`]

**AI:** Clarified that `"Auto remediation disabled by policy"` is NOT an error — it is expected behavior when the operator's master toggle is set to disabled. The operator detects the issue but does not act because remediation is explicitly turned off. To enable it:
```bash
kubectl patch networkremediation cluster-network-remediation --type=merge \
  -p '{"spec":{"cni":{"enabled":true},"coreDNS":{"enabled":true},"networkPolicy":{"enabled":true},"podConnectivity":{"enabled":true}}}'
```

---

### Turn 10 — Web App Simulation Bugs

**Me:**
> Okay so now let's have testing for the NetworkPolicy issue because now ALL the pods are in the READY state. So I have disabled the operator and followed the steps but I waited for 1 minute and the CNI Agent pod comes back to Ready state even when the operator is disabled. I think there are some bugs in the web app. Please find out all the bugs and fix them, at last report to me.

**AI:** Identified and fixed multiple simulation bugs in the `k8s-visualizer` React frontend:
1. **CNI pod auto-recovery bug**: The simulation was using a hardcoded 15-second timer to restore the CNI pod to Ready, regardless of operator state — this made it appear as if the cluster self-healed without the operator. Fixed to only restore CNI pods when the operator is enabled.
2. **Operator toggle state not propagating**: The operator enabled/disabled state was not being passed down to the simulation engine properly — state was being read from a stale closure. Fixed with proper React state management.
3. **Felix restart window not visualized**: The 30-second policy drift window was not shown in the UI. Added a visual indicator showing the enforcement gap during Felix restart.
4. **Log terminal not showing all steps**: Some simulation steps were firing but not emitting log entries — fixed event emission in the simulation state machine.

---

### Turn 11 — Watching Terminal Logs

**Me:**
> `VITE v8.2.2 ready in 559 ms — 5:21:47 AM [vite] http proxy error: /api/v1/nodes Error: connect ECONNREFUSED 127.0.0.1:8001`

**AI:** Diagnosed that `kubectl proxy` was not running. The visualizer proxies Kubernetes API calls through `localhost:8001`. Fixed by ensuring `kubectl proxy` is started in a separate terminal before opening the visualizer.

---

### Turn 12 — NetworkPolicy Simulation Verified

**Me:**
> I got these responses in the log terminal:
> `[Felix Drift] Step 1: Applied Deny-All NetworkPolicy to 'backend-api'`
> `[Felix Drift] Step 2: Killed Felix (calico-node). During the ~30s Felix restart window, the deny-all policy MAY not be enforced — this is the policy drift!`
> `ℹ️ The operator's NetworkPolicy [remediation triggered]`

**AI:** Confirmed the simulation is working correctly. The sequence shows: policy applied → Felix killed → drift window → operator detects and remediates. This is exactly the expected behavior demonstrating both the Kubernetes limitation and the operator's fix.

---

## Session 2: CoreDNS Remediation Documentation Request

---

### Turn 1 — Documentation & Diagram

**Me:**
> Okay now in this project I am working on the CoreDNS module part. The module is completed but needs some additional things — add a md file which explains what issues it is failing and one diagram which will explain the complete remediation policy so we can easily understand it. Something like attached [showed reference diagram style].

**AI:** Created `operator/docs/coredns-remediation.md` with:

**Failure modes documented:**
1. **Scale to Zero / Total Replica Loss** — Kubernetes respects desired replica count of 0, never auto-scales CoreDNS back. Operator detects `availableReplicas < expectedReplicas` and scales back up.
2. **Corrupted Upstream Corefile** — When `Corefile` `forward` directive points to an invalid/unreachable IP (`192.0.2.1`), Kubernetes accepts the ConfigMap without validating upstream reachability. Operator detects the invalid forward target, rewrites ConfigMap to `/etc/resolv.conf`, and triggers a rolling restart.
3. **CPU Throttling & Latency Spikes** — Under CPU limits (≤5m), pods stay `Running` and `Ready` in Kubernetes while DNS latency spikes to >1000ms. Operator detects resource starvation and restores baseline CPU resources (100m).
4. **Prometheus Latency Monitoring** — Queries `rate(coredns_dns_request_duration_seconds_sum[1m]) / rate(...)` and triggers remediation when latency exceeds thresholds (>150ms).
5. **Pod CrashLooping / Unready States** — Automatically detects and deletes unready/crashlooping pods to force immediate re-creation.

**Remediation pipeline diagram (Mermaid flowchart):** Included showing the full detection → decision → remediation → verification loop.

---

## Note on My Own Contribution

The AI was used as an interactive pair programmer throughout both sessions. All architecture decisions (reconciliation strategy, failure thresholds, cooldown design), cluster setup and management (Minikube with Calico CNI), testing, deployment, and final verification were done by me. The race condition in Turn 3 was independently identified by me — the AI fixed it after I described the issue. I reviewed every line of code the AI generated before accepting changes.

The **[Prompt-Refiner](https://github.com/arpangupta1805/prompt-refiner)** tool was integral to the workflow — it structured my rough prompts into detailed specifications, which significantly improved output quality and reduced correction rounds throughout both sessions.
