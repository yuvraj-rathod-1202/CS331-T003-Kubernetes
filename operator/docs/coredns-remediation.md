# CoreDNS Module: Failure Modes & Remediation Policy Guide

This document provides a comprehensive explanation of the CoreDNS health monitoring and remediation module within the **Network Remediation Operator**. It outlines the specific DNS failure scenarios handled by the operator, why native Kubernetes mechanisms fail to recover from them, and details the exact **Remediation Policy & Operator Workflow**.

---

## 1. Overview of CoreDNS Failure Modes

CoreDNS is the foundational DNS server for Kubernetes service discovery. While Kubernetes manages container lifecycles, it lacks application-level DNS monitoring and fails to recover from silent degradation, misconfigurations, or scaling issues.

The CoreDNS module detects and auto-heals five major failure scenarios:

| Failure Scenario | Root Cause | Kubernetes Built-in Behavior | Operator Remediation Action |
| :--- | :--- | :--- | :--- |
| **1. Scale to Zero / Total Availability Loss** | Deployment replicas set to `0` or below expected count. | K8s respects desired replicas and **does nothing**. Cluster DNS breaks completely. | Detects `availableReplicas < expectedReplicas` and automatically scales Deployment back to expected count (default: 2). |
| **2. Corrupted Upstream DNS Configuration** | `Corefile` ConfigMap `forward` directive patched to invalid/unreachable IP (e.g. `192.0.2.1`). | K8s applies ConfigMap without checking upstream reachability. Internal DNS works, external lookups fail. | Parses `Corefile`, replaces invalid `forward` target with healthy upstream (`/etc/resolv.conf`), updates ConfigMap, and triggers a rolling restart. |
| **3. CPU Throttling / Resource Starvation** | CPU request/limit patched to near-zero (e.g., `<= 5m`). | Pod status remains `Running` & `Ready`, but DNS latency spikes from ~2ms to >1000ms. | Detects CPU limit `<= 5m`, updates container spec back to baseline resources (100m CPU limit/request). |
| **4. High DNS Latency Spikes** | Heavy DNS load, network congestion, or slow resolution. | K8s does not track application-level metrics or DNS query duration. | Queries Prometheus metric (`rate(coredns_dns_request_duration_seconds_sum...)/rate(...)`), flags latency exceeding threshold (`> 150ms`), and triggers remediation. |
| **5. Pod CrashLooping / Unready Pods** | Memory leaks, corrupted binaries, or container crash loops. | Pod enters `CrashLoopBackOff` or `UnReady` state; K8s retries backoff slowly. | Detects UnReady / CrashLooping pods and deletes failed pods or triggers rolling restart to restore full ready replica count. |

---

## 2. CoreDNS Custom Resource Specification (`CoreDNSSpec`)

The `CoreDNSSpec` struct defined in [`operator/api/v1alpha1/coredns_types.go`](../api/v1alpha1/coredns_types.go) configures the monitoring parameters, thresholds, and automated remediation guardrails for the CoreDNS module within the `NetworkRemediation` custom resource definition (CRD).

### 2.1 Specification Field Reference Table

| Field | JSON Tag | Type | Default | Kubebuilder Marker | Purpose & Operational Usage |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `Enabled` | `enabled` | `bool` | `true` | `+kubebuilder:default=true` | **Master Module Switch:** Enables or disables CoreDNS health monitoring and remediation. When `false`, the reconciliation loop skips the CoreDNS subsystem entirely. |
| `ExpectedReplicas` | `expectedReplicas` | `int32` | `2` | `+kubebuilder:default=2`<br>`+optional` | **Target Replica Count:** Defines the minimum desired number of healthy, running CoreDNS replicas. Detects total availability loss (e.g., scale-to-zero) or degraded replica redundancy. |
| `LatencyThresholdMs` | `latencyThresholdMs` | `int64` | `150` | `+kubebuilder:default=150`<br>`+optional` | **DNS Query Latency Threshold:** The maximum acceptable average DNS resolution latency in milliseconds. Triggers warnings and remediation if Prometheus query duration exceeds this threshold (e.g., under CPU throttling). |
| `UpstreamDNSValidation` | `upstreamDnsValidation` | `bool` | `true` | `+kubebuilder:default=true`<br>`+optional` | **Corefile Forward Directive Audit:** Enables inspection of upstream forward resolvers in the `Corefile` ConfigMap to detect blackholed, non-routable, or invalid upstream IPs. |
| `AutoHealReplicas` | `autoHealReplicas` | `bool` | `true` | `+kubebuilder:default=true`<br>`+optional` | **Replica Self-Healing Guardrail:** When `true`, automatically scales the CoreDNS Deployment back up if replicas are scaled to zero or below `expectedReplicas`. When `false`, acts in alert-only mode. |
| `AutoHealConfigMap` | `autoHealConfigMap` | `bool` | `true` | `+kubebuilder:default=true`<br>`+optional` | **ConfigMap Self-Healing Guardrail:** When `true`, automatically repairs corrupted `forward` directives in the Corefile and restarts CoreDNS. When `false`, reports the defect without mutating cluster state. |
| `FallbackUpstreamServers` | `fallbackUpstreamServers` | `[]string` | `["/etc/resolv.conf"]` | `+optional` | **Fallback Resolver Pool:** An ordered list of reliable upstream DNS servers to inject into the `forward` block during ConfigMap auto-repair (e.g., node resolver `/etc/resolv.conf` or public DNS `8.8.8.8`). |

---

### 2.2 In-Depth Field Usage & Operational Impact

#### 1. `enabled` (`bool`)
* **Usage:** Master switch to activate or deactivate CoreDNS health management.
* **Why it matters:** Allows cluster administrators to temporarily silence automated CoreDNS interventions (for example, during scheduled maintenance, cluster upgrades, or CoreDNS version rollouts) without needing to delete the entire `NetworkRemediation` custom resource or disable other modules (CNI, NetworkPolicy, PodConnectivity).
* **Controller Behavior:** If `spec.CoreDNS.Enabled == false`, the reconciler immediately exits the CoreDNS phase without running any Kubernetes API queries, Prometheus scrapes, or ConfigMap evaluations.

#### 2. `expectedReplicas` (`int32`)
* **Usage:** Establishes the cluster's high-availability (HA) Service Level Objective (SLO) for DNS redundancy (default: `2`).
* **Why it matters:** Native Kubernetes only reconciles against whatever number is written in `deployment.spec.replicas`. If an engineer, automated script, or CI pipeline accidentally scales CoreDNS to zero (`kubectl scale --replicas=0`) as demonstrated in **Experiment 2.1**, Kubernetes leaves it at zero indefinitely.
* **Controller Behavior:**
  * **Check Phase:** Inspects `availableReplicas`, `readyReplicas`, and `desiredReplicas` of the `kube-system/coredns` Deployment.
  * **Evaluate Phase:** If `availableReplicas == 0` or `desiredReplicas == 0`, it flags a **Critical** severity outage. If `availableReplicas < expectedReplicas`, it flags a **Warning** severity degraded state.
  * **Remediate Phase:** Patches `deploy.Spec.Replicas = expectedReplicas` to restore DNS availability cluster-wide.

#### 3. `latencyThresholdMs` (`int64`)
* **Usage:** Configures the SLA threshold for DNS query round-trip time in milliseconds (default: `150ms`).
* **Why it matters:** In **Experiment 2.2**, CoreDNS CPU was throttled to `1m`. Pods remained in the `Running` state and passed Kubernetes readiness probes, but queries spiked from ~2ms to >1000ms, silently stalling microservice communications. Kubernetes has no native metric for application DNS query duration.
* **Controller Behavior:**
  * **Check Phase:** Queries Prometheus for the average query duration metric:
    ```promql
    rate(coredns_dns_request_duration_seconds_sum[1m]) / rate(coredns_dns_request_duration_seconds_count[1m])
    ```
  * **Evaluate Phase:** If Prometheus reports query duration exceeding `latencyThresholdMs`, the operator marks the cluster DNS as degraded.
  * **Remediate Phase:** Identifies if CPU throttling or resource starvation is present on container 0 and restores baseline CPU requests/limits (`100m`).

#### 4. `upstreamDnsValidation` (`bool`)
* **Usage:** Toggles automated syntax and upstream IP address validation inside the CoreDNS `Corefile` ConfigMap.
* **Why it matters:** Kubernetes ConfigMaps are arbitrary text stores with no schema validation. In **Experiment 2.3**, a forward directive pointing to a TEST-NET IP (`forward . 192.0.2.1`) completely broke external name resolution (`google.com`, external APIs) while internal resolution (`*.svc.cluster.local`) appeared healthy.
* **Controller Behavior:**
  * **Check Phase:** Fetches ConfigMap `kube-system/coredns`, extracts the `Corefile` data, and uses regex matching to extract the target destination of the `forward . <target>` block.
  * **Evaluate Phase:** Validates that the forward destination is not configured to known unreachable / non-routable subnets (RFC 5737 documentation ranges `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, loopback `127.0.0.1`, or `0.0.0.0`). If invalid, flags `upstreamCorrupted = true`.

#### 5. `autoHealReplicas` (`bool`)
* **Usage:** Safety guardrail enabling or disabling autonomous scaling mutations on the CoreDNS Deployment.
* **Why it matters:** Supports a "dry-run" or "auditing-only" rollout mode. When evaluating the operator in production, administrators may want alerts and status reporting without granting the operator permission to modify deployment replica counts automatically.
* **Controller Behavior:** In `Evaluate()`, `NeedsRemediation` inherits the boolean value of `autoHealReplicas`. When `false`, the controller records the degraded replica state in the CRD status and emits a Kubernetes Warning Event, but halts before calling `Client.Update()` on the Deployment.

#### 6. `autoHealConfigMap` (`bool`)
* **Usage:** Safety guardrail enabling or disabling automated mutation and rolling restarts of the CoreDNS ConfigMap.
* **Why it matters:** Mutating a core cluster ConfigMap and rolling out restarted pods is a sensitive cluster operation. Setting this to `false` lets teams use the operator strictly to detect config corruption while maintaining manual change approval workflows.
* **Controller Behavior:** When `upstreamCorrupted == true`, `NeedsRemediation` takes the value of `autoHealConfigMap`. If `true`, the operator rewrites the forward directive to the fallback server, applies the ConfigMap update, and initiates a rolling restart (`kubectl.kubernetes.io/restartedAt`). If `false`, it logs the issue without modifying the ConfigMap.

#### 7. `fallbackUpstreamServers` (`[]string`)
* **Usage:** Specifies the prioritized fallback upstream DNS resolvers used during ConfigMap auto-repair.
* **Why it matters:** Clusters run in diverse network environments:
  * Clusters relying on node DHCP/resolv.conf require `/etc/resolv.conf`.
  * Air-gapped or enterprise clusters require internal resolvers (e.g., `10.0.0.2`).
  * Public cloud environments can use reliable public DNS resolvers (e.g., `8.8.8.8`, `1.1.1.1`).
* **Controller Behavior:** When `AutoHealConfigMap` triggers remediation, the operator replaces the invalid forward target with the servers configured in this list (defaulting to `/etc/resolv.conf` if empty or unspecified).

---

### 2.3 Example `NetworkRemediation` CRD Manifest

Below is an annotated YAML manifest demonstrating how all `CoreDNSSpec` fields are configured in a cluster:

```yaml
apiVersion: remediation.cn-operator.yuvraj-rathod-1202.github.io/v1alpha1
kind: NetworkRemediation
metadata:
  name: network-remediation-sample
  namespace: default
spec:
  coreDNS:
    # Master switch: enable CoreDNS monitoring & self-healing
    enabled: true

    # HA requirement: ensure at least 2 CoreDNS pods are available
    expectedReplicas: 2

    # Latency threshold: alert/remediate if query duration exceeds 150ms
    latencyThresholdMs: 150

    # Actively parse and validate the Corefile upstream forward directive
    upstreamDnsValidation: true

    # Allow operator to automatically scale deployment back to expectedReplicas
    autoHealReplicas: true

    # Allow operator to automatically repair corrupted forward directives
    autoHealConfigMap: true

    # Resolvers to inject when repairing a corrupted forward directive
    fallbackUpstreamServers:
      - "/etc/resolv.conf"
      - "8.8.8.8"
      - "1.1.1.1"
```

---

## 3. Remediation Policy Workflow Diagram

The flowchart below illustrates the complete **Operator Workflow & Remediation Policy** for CoreDNS health management, corresponding to the operator's reconciliation logic.

```mermaid
flowchart TD
    Trigger([Trigger / Reconcile Event]) --> CheckReplicas{"Core DNS replicas < Desired Replicas?"}
    
    CheckReplicas -- Yes --> CreatePods["Create New CoreDNS Pods\n(required number of replicas)"]
    CheckReplicas -- No --> ListPods["List all DNS pods"]
    CreatePods --> ListPods
    
    ListPods --> CheckUnready{"Any UnReady pods?"}
    
    CheckUnready -- Yes --> DeleteUnready["Delete UnReady Pods"]
    DeleteUnready --> ReQueue((ReQueue))
    
    CheckUnready -- No --> CheckProbeJobs{"Any Probe Jobs exist\n+ active?"}
    
    CheckProbeJobs -- Yes --> WaitJob["Wait for Job to Finish"]
    WaitJob --> ReQueue
    
    CheckProbeJobs -- No --> JobSucceeded{"Job Succeeded?"}
    
    JobSucceeded -- Yes --> FailCountInc["failCount++"]
    JobSucceeded -- No --> FailCountReset["failCount = 0"]
    
    FailCountInc --> DeleteJob["Delete Job"]
    FailCountReset --> DeleteJob
    
    DeleteJob --> ReQueue
```

*Figure 1: CoreDNS Operator Remediation Workflow*

---

## 4. Workflow Step-by-Step Explanation

### Step 1: Trigger & Replica Verification
* **Trigger**: The operator's reconciliation loop runs periodically or is triggered by Kubernetes watch events on CoreDNS resources.
* **Replica Check**: The operator checks if `CoreDNS availableReplicas < Desired Replicas`.
  * **If `Yes`**: The operator updates the `coredns` Deployment spec to scale back up to the desired/expected replica count (default: 2 replicas).
  * **If `No`**: The operator proceeds to list all active DNS pods in the `kube-system` namespace.

### Step 2: Pod Readiness Audit
* **Check UnReady Pods**: The operator iterates through `kube-dns` labelled pods to inspect phase and container statuses (`CrashLoopBackOff`, `Error`, `ImagePullBackOff`, or `Ready = false`).
  * **If `Yes`**: UnReady/CrashLooping pods are deleted to force Kubernetes to spawn fresh, healthy pods immediately, and the controller calls `ReQueue`.
  * **If `No`**: The operator proceeds to evaluate synthetic DNS health probes.

### Step 3: Synthetic Probe Job Management
* **Check Active Probe Jobs**: The operator checks for running synthetic DNS test jobs (e.g., DNS probing jobs executing external/internal name resolution tests).
  * **If Active Probe Jobs Exist**: The operator waits for the job to complete and returns `ReQueue`.
  * **If No Active Probe Jobs Exist**: The operator checks the completion status of the executed probe job.

### Step 4: Probe Result Evaluation & `failCount` Tracking
* **`Job Succeeded?` Check**:
  * **If Probe Detected Failure (`Yes`)**: The health check probe recorded a DNS resolution error or latency threshold violation. The operator increments `failCount++`, deletes the completed probe job, and enters `ReQueue` to execute the appropriate remediation action (e.g., repairing ConfigMap or restoring CPU limits).
  * **If Probe Passed (`No failure`)**: DNS resolution is functioning normally. The operator resets `failCount = 0`, cleans up the finished job (`Delete Job`), and enters `ReQueue` for the next routine monitoring cycle.

### Step 5: Cooldown Enforcement
* To prevent restart-thrashing, the module enforces a **30-second cooldown window** (`cooldownWindow = 30s`) between remediation actions. If a remediation was recently triggered, subsequent reconcile passes log the remaining cooldown time and wait before taking further corrective steps.

---

## 5. Summary of Controller Code Mapping

| Flowchart Stage | Implementation File | Key Function / Method |
| :--- | :--- | :--- |
| **Check Phase** | `operator/internal/controller/coredns/controller.go` | `Check(ctx, spec)` |
| **Replica & Pod Inspection** | `operator/internal/controller/coredns/controller.go` | `checkDeployment()`, `checkPods()` |
| **ConfigMap & Upstream Validation** | `operator/internal/controller/coredns/controller.go` | `checkConfigMap()` |
| **Prometheus Latency Query** | `operator/internal/controller/coredns/controller.go` | `queryPrometheusLatency()` |
| **Evaluation Phase** | `operator/internal/controller/coredns/controller.go` | `Evaluate(ctx, checkResult)` |
| **Remediation Execution** | `operator/internal/controller/coredns/controller.go` | `Remediate(ctx, evalResult)` |

