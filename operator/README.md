# Operator (Project 1 and Project 2)

One Go/Kubebuilder manager binary holds the controllers of **two separate projects**.
They share the binary and the API group, nothing else: different custom resources,
different controllers, different cluster setups.

| Controller | Project | Custom resources | What it does | Docs |
|---|---|---|---|---|
| `networkremediation` | 1 | `NetworkRemediation` | Detects and repairs CNI, CoreDNS, NetworkPolicy and pod-to-pod failures (Check → Evaluate → Remediate modules) | [`docs/architecture.md`](docs/architecture.md) |
| `ipam` | 2, Problem 1 | `IPPool`, `NodeBlock` | Buddy-style dynamic pod IP allocation | [`docs/ipam-controller.md`](docs/ipam-controller.md) |
| `cniresilience` | 2, Problem 2 | `CNIResilience` | Dual-CNI (Calico + Flannel) failover and recovery | [`docs/cni-resilience.md`](docs/cni-resilience.md) |

Pick controllers with `--controllers` (comma-separated, or `all`):

```bash
go run ./cmd/main.go                                       # default: ipam,cniresilience (Project 2)
go run ./cmd/main.go --controllers=networkremediation      # Project 1
go run ./cmd/main.go --controllers=ipam --ipam-configure-kube-proxy
go run ./cmd/main.go --controllers=all
```

`make run` runs `go run ./cmd/main.go` with no flags, so it starts the Project 2
controllers. Project 1 is off by default because its PodConnectivity module cordons and
taints a node on a fault and never reverses it.

## Prerequisites

| Tool | Version |
|---|---|
| Go | 1.26 (see `go.mod`) |
| Docker | Latest |
| minikube | v1.37 tested |
| kubectl | Latest |
| Kubebuilder | v4.15+ (only to scaffold new APIs) |

## Project structure

```text
operator/
├── cmd/
│   ├── main.go                     # Manager; --controllers selects what runs
│   ├── ipam-agent/                 # P2: IPAM node agent (DaemonSet)
│   ├── ipam-sim/                   # P2: IPAM simulator and charts
│   └── healthagent/                # P2: CNI health agent (DaemonSet)
├── api/v1alpha1/
│   ├── networkremediation_types.go # P1: top-level CR
│   ├── cni_types.go, coredns_types.go, networkpolicy_types.go, podconnectivity_types.go  # P1 module specs
│   ├── ipam_types.go               # P2: IPPool, NodeBlock
│   └── cniresilience_types.go      # P2: CNIResilience
├── pkg/module/module.go            # P1: Check/Evaluate/Remediate interface
├── internal/controller/
│   ├── networkremediation_controller.go   # P1 dispatcher
│   ├── cni/, coredns/, networkpolicy/, podconnectivity/   # P1 modules
│   ├── ipam/                       # P2: controller, buddy/, agent/, sim/
│   └── cniresilience/              # P2: controller, healthagent/, failover/, recovery/
├── deploy/                         # P2 manifests
│   ├── ipam-agent.yaml
│   ├── healthagent-daemonset.yaml, healthagent-networkpolicy.yaml
│   ├── calico-primary-pool.yaml, flannel-backup.yaml, multus-nad.yaml
├── hack/ipam-phase0/               # P2: scripts used to test IPAM assumptions
├── Dockerfile.agent                # P2: ipam-agent image
├── Dockerfile.healthagent          # P2: health agent image
├── config/
│   ├── crd/bases/                  # Generated CRDs (both projects)
│   ├── rbac/                       # Generated RBAC
│   └── samples/                    # networkremediation (P1), ipam_sample, cniresilience_sample (P2)
└── docs/                           # See the table below
```

`NodeIPAllocation` and `internal/controller/ipam/monitor/` are left over from an early
Project 2 scaffold and are not used by the IPAM controller.

## Docs

| Project | File |
|---|---|
| 1 | [`architecture.md`](docs/architecture.md), [`cni-remediation-policy.md`](docs/cni-remediation-policy.md), [`coredns-remediation.md`](docs/coredns-remediation.md), [`networkpolicy-remediation.md`](docs/networkpolicy-remediation.md), [`pod_connectivity_operator.md`](docs/pod_connectivity_operator.md) |
| 2, IPAM | [`ipam-controller.md`](docs/ipam-controller.md), [`ipam-agent.md`](docs/ipam-agent.md), [`ipam-evaluation.md`](docs/ipam-evaluation.md) (results in [`ipam-eval/`](docs/ipam-eval/)), [`ipam-phase0-assumptions.md`](docs/ipam-phase0-assumptions.md), [`ipam-decision-log.md`](docs/ipam-decision-log.md) |
| 2, CNI resilience | [`cni-resilience.md`](docs/cni-resilience.md), [`../experiments/cni-resilience-experiment.md`](../experiments/cni-resilience-experiment.md) |

## Build and test

```bash
make build                 # bin/manager
make test                  # unit tests + envtest, all controllers
make lint

# One package at a time
go test ./internal/controller/cni/... -v              # P1
go test ./internal/controller/ipam/... -v             # P2 IPAM
go test ./internal/controller/cniresilience/... -v    # P2 CNI resilience
```

After editing any `*_types.go` file or RBAC marker:

```bash
make manifests
make generate
```

## Running Project 1

```bash
minikube start --cni=calico --nodes=2 -p k8s-experiments
make install
kubectl apply -f config/samples/remediation_v1alpha1_networkremediation.yaml
go run ./cmd/main.go --controllers=networkremediation
kubectl get networkremediation cluster-network-remediation -o yaml
```

Adding a Project 1 module: implement `Check()`, `Evaluate()` and `Remediate()` in
`internal/controller/<module>/controller.go` (see [`docs/architecture.md`](docs/architecture.md)).

## Running Project 2

The two problems need different clusters: IPAM replaces the node's CNI, while CNI
resilience runs Calico and Flannel side by side.

**IPAM simulation** (no cluster):
```bash
go run ./cmd/ipam-sim --out docs/ipam-eval --seeds 3
```

**IPAM on a cluster** (flags explained in [`docs/ipam-phase0-assumptions.md`](docs/ipam-phase0-assumptions.md)):
```bash
minikube start -p buddy-ipam --driver=docker --container-runtime=docker --nodes=3 \
  --cni=false --memory=1900 \
  --extra-config=controller-manager.allocate-node-cidrs=false \
  --extra-config=kubelet.max-pods=600
# remove /etc/cni/net.d/87-podman-bridge.conflist on every node
make install
kubectl apply -f config/samples/ipam_sample.yaml
docker build -f Dockerfile.agent -t ipam-agent:dev .
minikube -p buddy-ipam image load ipam-agent:dev
kubectl apply -f deploy/ipam-agent.yaml
go run ./cmd/main.go --controllers=ipam --ipam-configure-kube-proxy
kubectl get nodeblocks -w
```

**CNI resilience:** full setup (Calico on `172.16.0.0/16`, Multus, Flannel, health agent,
workload) in [`../experiments/cni-resilience-experiment.md`](../experiments/cni-resilience-experiment.md), then:
```bash
make run
kubectl get cniresilience default-resilience -o yaml
```

## Uninstalling

```bash
kubectl delete -f config/samples/remediation_v1alpha1_networkremediation.yaml   # P1 CR
kubectl delete -f config/samples/ipam_sample.yaml -f config/samples/cniresilience_sample.yaml   # P2 CRs
make uninstall                      # CRDs
```

## CI

`.github/workflows/ci.yaml` runs on every push and pull request to `main`: lint,
`make test`, build, and a check that generated CRD/RBAC manifests are up to date.
