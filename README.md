### Team ID: T003
### Project ID: 7
### Project Title: Extending Kubernetes Self-Healing for Networking Failure
### Team Members: 
    1. Arpan Gupta (24110051)
    2. Buha Deep Maheshbhai (24110082)
    3. Param Tanna (24110236)
    4. Ramji Purwar (24110287)
    5. Rathod Yuvraj Rajubhai (24110293)
    6. Solanki Viraj Rajeshbhai (24110348)

---

A custom Kubernetes operator developed in Go using [Kubebuilder](https://book.kubebuilder.io/) to detect and automatically heal networking-related failures in a Kubernetes cluster.

Kubernetes provides built-in recovery for standard container crashes, but it does not auto-recover from low-level networking degradation-such as CNI agent crashes, CoreDNS throttling, NetworkPolicy drift, or broken veth/tunnel interfaces. This operator bridges that gap.

---

## Repository Overview

```text
CS331-CN-Project-1/
├── PROJECT.md             # Theoretical background & failure mode analysis
├── README.md              # Project overview & team guide (this file)
├── experiments/           # Manual failure reproduction & validation guides
│   ├── README.md          # Guide to running experiments
│   ├── setup.md           # Multi-node Minikube + Calico setup
│   ├── cni-failure.md
│   ├── coredns-failure.md
│   ├── networkpolicy-failure.md
│   ├── pod-connectivity-failure.md
│   └── manifests/         # Test workloads used to simulate failures
└── operator/              # The Go/Kubebuilder operator codebase
    ├── README.md          # Operator development & execution instructions
    ├── docs/
    │   └── architecture.md # Detailed Check → Evaluate → Remediate architecture
    ├── api/v1alpha1/      # CRD types (separated per module)
    ├── pkg/module/        # Shared Module interface
    ├── internal/controller/ # Controller packages (separated per module)
    └── config/            # Generated CRD & RBAC manifests
```

---

## Module Overview

The operator is divided into 4 modular subsystems.

- **CNI**
- **CoreDNS**
- **NetworkPolicy**
- **PodConnectivity**

---

## How It Works: The 3-Phase Pipeline

Every module implements the common `Module` interface defined in [`operator/pkg/module/module.go`](operator/pkg/module/module.go):

```text
Reconcile Loop
    │
    ▼
Module Enabled? ──(No)──► Skip
    │ (Yes)
    ▼
1. Check        ──► Gathers raw telemetry (pod status, query latency, probe pings)
    │
    ▼
2. Evaluate     ──► Evaluates signals against thresholds (avoids false alarms)
    │
    ├──(Healthy)─────────────────────────► Status: Healthy ────────┐
    │                                                              │
    ▼ (Needs Remediation)                                          ▼
3. Remediate    ──► Executes the fix (restarts pod, reapplies policy, taints node)
    │                                                              │
    ▼                                                              │
Updates CR Status & Requeues for next cycle ◄──────────────────────┘
```

Read [`operator/docs/architecture.md`](operator/docs/architecture.md) for full architectural details.

---

## Quickstart

### 1. Prerequisites
Ensure you have the following installed:
- **Go**: 1.24+ (1.26 recommended)
- **Minikube**: Latest
- **kubectl**: Latest
- **Kubebuilder**: v4.15+ (if scaffolding new APIs)

### 2. Start the Cluster
Start a 2-node Minikube cluster running Calico CNI:
```bash
minikube start --cni=calico --nodes=2 -p k8s-experiments
```

Verify nodes and Calico pods are ready:
```bash
kubectl get nodes
kubectl get pods -n kube-system -l k8s-app=calico-node
```

### 3. Install the CRD
From the `operator/` directory:

```bash
cd operator

# On Linux / macOS / WSL:
make install

# On Windows PowerShell:
kubectl apply -k config/crd
```

### 4. Create the Custom Resource
Apply the sample configuration that enables monitoring:
```bash
kubectl apply -f config/samples/remediation_v1alpha1_networkremediation.yaml
```

### 5. Run the Operator Locally
Run the controller manager against your local Minikube cluster:

```bash
# On Linux / macOS / WSL:
make run

# On Windows PowerShell:
go run ./cmd/main.go
```

The operator will immediately log the reconciliation loops and execute the Check → Evaluate → Remediate phases for all enabled modules!

---

## Workflow: Implementing Your Module

1. **Review Failure Scenarios**: Read your module's experiment doc in `experiments/` to understand how the failure occurs and how to recover from it.
2. **Define CRD Parameters**: Add your configuration fields (thresholds, timeouts, intervals) to your `operator/api/v1alpha1/<module>_types.go` file.
3. **Regenerate Code**: Whenever you change your types file, run:
   ```bash
   cd operator
   make manifests
   make generate
   ```
4. **Implement Logic**: Open your `operator/internal/controller/<module>/controller.go` file and implement the three methods:
   - `Check()`
   - `Evaluate()`
   - `Remediate()`
5. **Run Tests**:
   ```bash
   make test
   ```
6. **Submit PR**: GitHub Actions CI will automatically lint, test, build, and verify that manifests are up to date.

---

## Key References
- [Project Description & Problem Statement (`PROJECT.md`)](PROJECT.md)
- [Operator Developer Guide (`operator/README.md`)](operator/README.md)
- [Architecture & Implementation Guide (`operator/docs/architecture.md`)](operator/docs/architecture.md)
- [Experiments Directory (`experiments/README.md`)](experiments/README.md)
