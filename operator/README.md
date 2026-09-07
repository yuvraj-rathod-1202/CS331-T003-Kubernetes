# Network Remediation Operator

A custom Kubernetes operator that detects and auto-heals networking failures in a Kubernetes cluster. The operator monitors CNI plugin health, CoreDNS performance, NetworkPolicy enforcement, and pod-to-pod connectivity.

## Prerequisites

Before developing this operator, ensure you have the following installed:

### Required

| Tool | Version | Installation |
|------|---------|-------------|
| **Go** | 1.27.0+ | [go.dev/dl](https://go.dev/dl/) |
| **Kubebuilder** | v4.15.0+ | [kubebuilder.io/quick-start](https://book.kubebuilder.io/quick-start) |
| **kubectl** | Latest | [kubernetes.io/docs/tasks/tools](https://kubernetes.io/docs/tasks/tools/) |
| **Minikube** | Latest | [minikube.sigs.k8s.io/docs/start](https://minikube.sigs.k8s.io/docs/start/) |
| **Docker** | Latest | [docs.docker.com/get-docker](https://docs.docker.com/get-docker/) |

### Verify Installation

```bash
go version          # Should show go1.27.0 or later
kubebuilder version # Should show v4.15.0 or later
kubectl version     # Should show client version
minikube version    # Should show minikube version
docker version      # Should show Docker version
```

## Getting Started

```bash
cd CN-Project-1/operator

# Download Go dependencies
go mod download

# Read the architecture guide before starting
# docs/architecture.md
```

## Project Structure

```
operator/
├── cmd/main.go                          # Entrypoint - sets up manager, registers modules
├── api/v1alpha1/
│   ├── networkremediation_types.go      # Top-level CRD types (Spec, Status)
│   ├── cni_types.go                     # CNI module spec types
│   ├── coredns_types.go                 # CoreDNS module spec types
│   ├── networkpolicy_types.go           # NetworkPolicy module spec types
│   └── podconnectivity_types.go         # PodConnectivity module spec types
├── pkg/module/
│   └── module.go                        # Module interface - Check/Evaluate/Remediate
├── internal/controller/
│   ├── networkremediation_controller.go # Top-level dispatcher
│   ├── cni/                             # CNI module
│   ├── coredns/                         # CoreDNS module
│   ├── networkpolicy/                   # NetworkPolicy module
│   └── podconnectivity/                 # PodConnectivity module
├── docs/
│   └── architecture.md                  # Architecture guide (READ THIS FIRST)
└── config/
    ├── crd/bases/                       # Generated CRD YAML
    ├── rbac/                            # Generated RBAC
    └── samples/                         # Sample CR manifests
```

See [docs/architecture.md](docs/architecture.md) for the full architecture explanation and how to implement your module.

## Development Workflow

### 1. Modify CRD Types

When adding new fields to your module's spec:

```bash
# 1. Edit your module's types file in api/v1alpha1/
#    (e.g., api/v1alpha1/cni_types.go for CNI)

# 2. Regenerate CRD manifests and DeepCopy methods
make manifests
make generate
```

### 2. Implement Module Logic

Your module lives in `internal/controller/<module>/controller.go`. Implement the three methods:
- `Check()` - gather health signals
- `Evaluate()` - analyze signals, decide if remediation is needed
- `Remediate()` - execute the fix

### 3. Add RBAC Permissions

If your module needs to access Kubernetes resources, add RBAC markers in your controller:

```go
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
```

Then run `make manifests` to regenerate RBAC rules.

## Building

```bash
# Build the operator binary
make build
```

## Running Tests

```bash
# Run all tests (includes lint, vet, unit tests, envtest)
make test

# Run tests for a specific module
go test ./internal/controller/cni/... -v
go test ./internal/controller/coredns/... -v
go test ./internal/controller/networkpolicy/... -v
go test ./internal/controller/podconnectivity/... -v

# Run with coverage report
go test ./... -coverprofile=cover.out
go tool cover -html=cover.out

# Run linter only
make lint
```

## Running Locally on Minikube

### 1. Start Minikube with Calico CNI

```bash
# Start a multi-node cluster with Calico
minikube start --cni=calico --nodes=2 -p k8s-experiments

# Verify nodes are ready
kubectl get nodes

# Verify Calico is running
kubectl get pods -n kube-system -l k8s-app=calico-node
```

### 2. Install CRDs

```bash
# Install the NetworkRemediation CRD into the cluster
make install

# Verify
kubectl get crd networkremediations.remediation.cn-operator.yuvraj-rathod-1202.github.io
```

### 3. Apply Sample CR

```bash
# Apply the sample NetworkRemediation resource
kubectl apply -f config/samples/remediation_v1alpha1_networkremediation.yaml

# Verify
kubectl get networkremediation
```

### 4. Run the Operator Locally

```bash
# Run the operator against your Minikube cluster
make run
```

The operator will connect to the cluster specified in your `~/.kube/config` and start the reconciliation loop. You should see logs for each module's Check → Evaluate → Remediate cycle.

### 5. Check Status

```bash
# View the CR status
kubectl get networkremediation cluster-network-remediation -o yaml

# Watch logs
make run 2>&1 | grep -E "(Check|Evaluate|Remediate)"
```

## Uninstalling

```bash
# Remove the sample CR
kubectl delete -f config/samples/remediation_v1alpha1_networkremediation.yaml

# Remove the CRDs
make uninstall
```

## CI/CD

This project uses GitHub Actions for continuous integration. The CI pipeline (`.github/workflows/ci.yaml`) runs on every push and pull request to `main`:

1. **Lint** - golangci-lint for code quality
2. **Test** - `make test` (unit tests + envtest)
3. **Build** - compile the operator binary
4. **Manifests** - verify generated CRD/RBAC manifests are up to date

Make sure `make test` passes locally before pushing.
