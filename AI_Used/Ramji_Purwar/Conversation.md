# Conversation Transcript (Curated)

This is a curated record of the key interactions and technical discussions with Google Antigravity during the development of the **CNI Self-Healing Module**.

**Legend:**
- **Me:** Ramji Purwar
- **AI:** Google Antigravity (Claude Sonnet 4.6 / Gemini 3.7 Flash)

---

### Phase 1: Conceptual Flow, Workflow Discussion & Controller Architecture

**Me:** I defined the basic flows and failure scenarios for the CNI module:
1. **Calico DaemonSet failures:** A `calico-node` pod crashes or gets evicted on a node, preventing new pods on that node from obtaining networking.
2. **IPAM exhaustion & disabled pools:** Calico IP pools run out of IPs (100% allocation in `IPAMBlock`) or an administrator/script disables an `IPPool`, blocking pods from acquiring IPs.
3. **Stuck pods:** Pods remain trapped in `ContainerCreating` with `FailedCreatePodSandBox` CNI errors.

I insisted on a strict rule: **remediation must be targeted, not blanket**. If only one node or IP pool is broken, we should only remediate that specific target rather than restarting all CNI pods or modifying all pools.

**Discussion on Workflow:**
- We discussed how to fit this into the operator's `Check -> Evaluate -> Remediate` pipeline.
- We identified a key architectural challenge: `Remediate()` only receives `*EvalResult` without the raw check signals, which previously led to naive blanket scans.
- To solve this, we agreed on the `pendingTarget` pattern on `CNIModule`: during `Evaluate()`, the module analyzes the signals, determines the precise `ActionType` (`restart_calico_node`, `reenable_ippool`, `evict_stuck_pods`), records the exact target pod names / pool names in `pendingTarget`, and then `Remediate()` consumes this to execute selective, safe remediation.

**AI:** Implemented the agreed workflow into `operator/internal/controller/cni/controller.go`:
- **`Check()`**: Collects signals on:
  1. `calico-node` DaemonSet status (desired scheduled vs. ready pods).
  2. Pods stuck in `ContainerCreating` with `FailedCreatePodSandBox` events.
  3. Calico `IPAMBlock` CRDs to calculate IP pool utilization and detect disabled `IPPool` custom resources.
- **`Evaluate()`**: Sets severity (`critical`, `warning`, `none`), populates `pendingTarget`, and returns evaluation reasons.
- **`Remediate()`**: Consumes `pendingTarget` and executes targeted remediation:
  - `ActionRestartCalicoNode`: Deletes only unready `calico-node` pods so the DaemonSet reconciles them.
  - `ActionReenableIPPool`: Re-enables disabled Calico IP pools and evicts waiting pods.
  - `ActionEvictStuckPods`: Evicts pods blocked by IP exhaustion so they reschedule once IPAM recovers.

---

### Phase 2: CRD Types and Unit Testing

**Me:** Add the required CRD configuration options in the spec for thresholds and namespaces, and write unit tests for every flow we discussed.

**AI:**
1. Extended `CNISpec` in `operator/api/v1alpha1/cni_types.go` with configurable thresholds (`IPAMUsageThresholdPercent`, `Namespace`, `DaemonSetName`, `DisabledIPPoolsAction`) and generated CRD manifests.
2. Created `operator/internal/controller/cni/controller_test.go` with fake client mocks covering:
   - Healthy cluster evaluations.
   - Unready Calico DaemonSet detection and pod restart remediation.
   - IPAM exhaustion detection above configurable thresholds (e.g. 80%, 90%).
   - Targeted re-enabling of disabled IP pools without affecting healthy pools.
   - Selective eviction of CNI-stuck pods.

---

### Phase 3: GitHub Actions CI & Linter Debugging

**Me:** Failed this test, correct this. [Uploaded CI failure screenshot showing `gocyclo` (>30 complexity in `Remediate`) and `unused` function].

**AI:** Analyzed the CI log and local codebase:
1. **`gocyclo`**: `Remediate()` had high cyclomatic complexity (32 > 30). Refactored the switch cases into dedicated helper functions (`remediateCalicoNodeRestart`, `remediateReenableIPPool`, `remediateEvictStuckPods`).
2. **`unused`**: Verified and retained `hasIPAMStuck` helper used during evaluation.
3. **`goconst` & `modernize`**: Replaced repeated string literals (`"apps"`, `"DaemonSet"`, `"calico-node"`) with defined constants and replaced manual if-checks with Go's `min()` builtin.
4. Verified that `golangci-lint run ./...` reports 0 issues and all unit tests pass with `go test`.

---

### Phase 4: Analyzing the Implementation & Documenting the CNI Remediation Workflow

**Me:** Analyze the complete CNI module implementation and generate comprehensive documentation detailing the self-healing workflow, failure detection logic, evaluation state machine, and targeted remediation policies.

**AI:** Analyzed the controller implementation and created technical documentation (`operator/docs/cni-remediation-policy.md` and `CNI_README.md`):
- Created Mermaid workflow diagrams showing the full reconciliation lifecycle (`Check` -> `Evaluate` -> `Remediate`).
- Documented the exact signals monitored (`calicoNodeUnready`, `stuckPods`, `ipamUsageByPool`, `ipamExhaustedPools`, `disabledIPPools`).
- Detailed the decision table mapping failure conditions to targeted remediation actions (`restart_calico_node`, `reenable_ippool`, `evict_stuck_pods`).
- Documented safety guarantees against blanket cluster restarts and control plane disruption.
