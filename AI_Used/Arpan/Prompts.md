# Prompts

> **Note on Prompt Refiner**: I use a local tool called [Prompt-Refiner](https://github.com/arpangupta1805/prompt-refiner) which is integrated in my AI pipeline. It converts my vague, short prompts into well-structured, high-quality prompts using prompt engineering techniques. The **vague prompt** I typed is listed first, followed by the **refined prompt** that was actually passed to the model.

---

## Session 1: Kubernetes DNS Monitoring Setup (main session)

### 1. Initial Setup & CoreDNS Module Implementation

**My vague prompt:**
> I need to implement the CoreDNS remediation module for our Kubernetes self-healing operator project. The module should detect when CoreDNS pods become unhealthy or degraded and trigger automatic remediation — like restarting pods, rescheduling, or reconfiguring DNS. Help me build the controller logic in Go using controller-runtime.

**Refined prompt passed to AI (by Prompt-Refiner):**
> # Goal: Local Environment Setup & CoreDNS Health Monitoring Component Initialization
>
> ## Context
> We are building a custom Kubernetes Self-Healing Network Operator/Controller to detect and remediate networking failures (CNI crashes, DNS issues, connectivity drops, and broken NetworkPolicies). The assigned component for this module is **DNS Health Monitoring & Remediation (CoreDNS)**.
>
> The project runs in the workspace: `/Users/arpangupta/Desktop/Sem5/Computer Network/CS331-CN-Project-1`.
>
> ## Phase 1: Local Environment & Cluster Setup
> 1. Verify Prerequisites & Local Tooling (docker, kubectl, minikube/kind, helm, Go+kubebuilder)
> 2. Initialize Local Kubernetes Cluster
> 3. Deploy Observability Stack (Prometheus, CoreDNS metrics)
>
> ## Phase 2: Project Architecture & Scaffolding for CoreDNS Module
> 1. Scaffold Project Directory Structure
> 2. Define DNS Health Specifications (metrics, failure signals, remediation strategies)
> 3. Create Scaffolding Code & Configuration (RBAC, controller skeleton)
>
> ## Phase 3: Verification & Smoke Test
> 1. Validate Baseline Setup with test probe pod
> 2. Documentation

---

### 2. Verification of Project Completeness

**My vague prompt:**
> So basically the complete CoreDNS according to this is completed na?

**Context passed:** The full project description was pasted inline asking the AI to confirm coverage of all required features for the CoreDNS module.

---

### 3. Debugging Concurrency & Race Conditions

**My prompt:**
> Rolling restart racing the fix: when you patch the ConfigMap and immediately annotate for rolling restart, make sure the annotation-triggered restart happens after the ConfigMap write is confirmed committed (not just submitted) — otherwise pods can restart and reload the old corrupted Corefile if there's any propagation lag.
> Remediation loops: nothing in the table mentions a cooldown/backoff on Remediate(). If CoreDNS keeps crashing right after a restart (e.g. bad node, not a config issue), does [it loop infinitely?]

---

### 4. Lint / CI Fix

**My vague prompt:**
> Run make lint-config. This is failing like we have lint-test-build in the GitHub that is failing.

---

### 5. Web App Visualizer Fix

**My vague prompt:**
> I am getting this error when trying to run the web app visualizer please see this and fix it. Don't change any codebase because it is running on other devices as it is. Like don't do build part for now, I want only the dev one.

---

### 6. NetworkPolicy Testing Guide

**My vague prompt:**
> Okay now my team member has given me the work of the testing. Explain me properly how can I test it in the web app. There is the module network policy in which we have found one problem in the kubernetes and using our this operator we have fixed the problem so I have to check that without the operator what is the problem and what is showing in the web app and that is correct or not and with our operator how it is fixed and it is correctly fixed or not.

---

### 7. Debugging CRD Not Found Error

**My vague prompt (error pasted directly):**
> Failed to toggle operator: networkremediations.remediation.cn-operator.yuvraj-rathod-1202.github.io "networkremediation-sample" not found (404). Why is this not working?

---

### 8. Understanding the Web App Modules

**My vague prompt:**
> Like listen I am new to this web app I don't understand this web app so please explain me properly. I understand till Part A 3-b. I am not able to find that "Kill CNI / Felix Agent". What is this?

---

### 9. Debugging Operator Logs

**My vague prompt (logs pasted directly):**
> In the terminal see there are several execution fail debug statements I am seeing. Check whether there is any issue in that or what. [INFO] Remediation failed: Auto remediation disabled by policy for node k8s-experiments...

---

### 10. End-to-End Bug Discovery & Fix

**My vague prompt:**
> Okay so now let's have testing for the issue in the network policy because now ALL the pods are in the READY state. So I have disabled the operator and followed the steps but I waited for 1 min and the CNI Agent pod comes back in the ready state even when the operator is disabled. I think there are some bugs in the web app, please find out all the bugs and fix them, at last report to me.

---

## Session 2: CoreDNS Remediation Documentation Request

### 11. Documentation & Diagram Generation

**My vague prompt:**
> Okay now in this project I am working on the CoreDNS module part so now you have to do the following things: the module is completed but needs some additional things — add a md file which explains what issues it is failing and one diagram which will explain the complete remediation policy so that we can easily understand it.
