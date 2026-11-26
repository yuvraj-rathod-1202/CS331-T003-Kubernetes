# Operator

One Go/Kubebuilder manager with one controller, `cniresilience`, plus two per-node
binaries. The design is in [`docs/cni-resilience.md`](docs/cni-resilience.md).

| Component | Binary | Runs as | What it does |
|---|---|---|---|
| Operator | `cmd/main.go` | Deployment, or on the host | Reads the agents' reports, fails a node over (readiness gate, shadow EndpointSlice, taint, one `calico-node` restart) and back, aggregates link status |
| Health agent | `cmd/healthagent` | DaemonSet | Probes every peer on both networks and publishes the verdicts on its Node |
| Link router | `cmd/linkrouter` | DaemonSet (host network, `NET_ADMIN`) | Chooses primary, backup, relay or none per peer and installs it as policy routes |

## Layout

```text
operator/
├── api/v1alpha1/                       # CNIResilience types
├── cmd/
│   ├── main.go                         # manager
│   ├── healthagent/
│   └── linkrouter/
├── internal/controller/cniresilience/
│   ├── cniresilience_controller.go     # reconcile: blame, fail over, fail back, link status
│   ├── links.go                        # link status from router reports, LinksReachable condition
│   ├── healthagent/                    # probing and verdicts
│   ├── linkrouter/                     # path choice and netlink routes/rules
│   ├── failover/                       # readiness gates, shadow EndpointSlice, taint
│   └── recovery/                       # calico-node restart with backoff
├── deploy/                             # calico-primary-pool, flannel-backup, multus-nad,
│                                       # healthagent and linkrouter DaemonSets
├── config/                             # CRD, RBAC, manager, sample CR (kustomize)
├── Dockerfile                          # manager image
├── Dockerfile.healthagent              # agent image (from bin/healthagent)
└── Dockerfile.linkrouter               # router image (from bin/linkrouter)
```

## Build and test

```bash
go test ./...                                   # unit tests
go build -o bin/manager ./cmd/main.go
go build -o bin/healthagent ./cmd/healthagent
go build -o bin/linkrouter ./cmd/linkrouter
make lint
```

After editing `api/v1alpha1/*_types.go` or an RBAC marker:

```bash
make manifests generate
```

## Run

The cluster needs Calico on `172.16.0.0/16`, Multus and Flannel on a second interface.
[`../testbed/bootstrap.sh`](../testbed/bootstrap.sh) sets all of this up, including
kube-proxy's `clusterCIDR`, which must be Calico's range. Then:

```bash
kubectl apply -f config/crd/bases/
kubectl apply -f config/samples/cniresilience_sample.yaml
kubectl apply -f deploy/healthagent-daemonset.yaml -f deploy/linkrouter-daemonset.yaml
go run ./cmd/main.go
kubectl get cniresilience default-resilience -o yaml
```

Pods are protected if they carry the readiness gate
`cniresilience.cn-operator/primary-path` and a `net1` interface (see
[`../testbed/manifests/targets.yaml`](../testbed/manifests/targets.yaml), whose gate list
[`../testbed/workload.sh`](../testbed/workload.sh) fills in).

`linkrouter --cleanup` removes every route and rule the router owns.
