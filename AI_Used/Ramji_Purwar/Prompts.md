# Prompts

Below are the key prompts and discussion topics given to Google Antigravity during the development, testing, CI debugging, and documentation of the **CNI Self-Healing Module**, presented in chronological sequence.

---

## 1. Initial Flows & Conceptual Requirements
1. *"Here are the basic flows and failure scenarios for the CNI module in Kubernetes that we need to handle:"*
   - *Calico DaemonSet unreadiness / crashlooping pods on specific nodes.*
   - *IPAM pool exhaustion (100% block allocation) and disabled IPPools.*
   - *Workload pods stuck in `ContainerCreating` with `FailedCreatePodSandBox` CNI errors.*
2. *"The remediation must be targeted and safe — if only one node's CNI pod or one IP pool is failing, we must only remediate that specific target rather than performing blanket cluster-wide restarts or wiping all pools."*

---

## 2. Architectural Workflow Discussion
3. *"How do we structure this inside the operator's `Check -> Evaluate -> Remediate` loop so that `Remediate()` knows exactly which targets to fix without doing redundant blanket sweeps?"*
4. *"Let's design a state pattern (like `pendingTarget` on `CNIModule`) so `Evaluate()` identifies the exact action (`restart_calico_node`, `reenable_ippool`, `evict_stuck_pods`) and stores the affected pod/pool names for `Remediate()` to consume."*
5. *"How should we query Calico CRDs (`IPAMBlock`, `IPPool`) using the dynamic client to calculate IP pool utilization and detect disabled pools?"*

---

## 3. Implementation & CRD Customization
6. *"Now implement this complete workflow in `operator/internal/controller/cni/controller.go` following the `pendingTarget` pattern."*
7. *"Add configurable thresholds and options in `operator/api/v1alpha1/cni_types.go` (`IPAMUsageThresholdPercent`, `Namespace`, `DaemonSetName`, `DisabledIPPoolsAction`) and regenerate the CRD manifests."*

---

## 4. Unit Testing & Verification
8. *"Generate comprehensive unit tests in `controller_test.go` using controller-runtime fake client mocks covering all the flows we discussed: healthy cluster, unready Calico node restart, high IPAM usage warnings, disabled pool re-enabling, and stuck pod eviction."*
9. *"Run the unit tests with `go test ./internal/controller/cni/...` and verify test coverage and assertions."*

---

## 5. GitHub Actions CI & Linter Debugging
10. *"failed this test correct this"* *(provided screenshot of GitHub Actions CI failure showing `golangci-lint` errors on `gocyclo` complexity and `unused` function in `controller.go`)*
11. *"continue"*
12. *"failing a github test on pr check it"*
13. *"failing this github test"* *(uploaded CI logs; requested modularizing `Remediate()` switch branches, adding constants for repeated strings, and adopting Go `min()` builtin)*

---

## 6. Remediation Workflow Analysis & Documentation
14. *"Analyze the complete CNI module implementation and generate comprehensive documentation (`cni-remediation-policy.md` and `CNI_README.md`) detailing the self-healing workflow, failure detection matrix, evaluation state machine, and targeted remediation policies with Mermaid flowcharts."*
