### Team ID: T003
### Project ID: 7
### Project Title: Extending Kubernetes Self-Healing for Networking Failure
### Project 2: Kubernetes IPAM Efficiency & CNI Resilience
### Team Members:
    1. Arpan Gupta (24110051)
    2. Buha Deep Maheshbhai (24110082)
    3. Param Tanna (24110236)
    4. Ramji Purwar (24110287)
    5. Rathod Yuvraj Rajubhai (24110293)
    6. Solanki Viraj Rajeshbhai (24110348)

---

Kubernetes pod networking has two weak spots. This project tackles both, as a pair:
*"one is efficiency, the other is resiliency."*

| | Problem 1: IPAM efficiency | Problem 2: CNI resilience |
|---|---|---|
| Problem | Every node gets a fixed block of pod addresses (default /24). Quiet nodes waste it; busy nodes run out and their pods cannot start. | Every pod has one interface from one CNI. If its data path breaks, the pod is unreachable while the node still reports `Ready`. |
| Solution | Buddy-style IPAM: aligned power-of-two blocks per node that grow and shrink with demand | Second interface on an independent CNI (Calico + Flannel via Multus), health agents, automatic failover of Service traffic |
| Custom resources | `IPPool`, `NodeBlock` | `CNIResilience` |
| Controller | `ipam` | `cniresilience` |
| Node component | `ipam-agent` DaemonSet | `cni-health-agent` DaemonSet |
| Dashboard page | `/project1` (simulation, no cluster) | `/project2` (live cluster) |

Both controllers are written in Go with [Kubebuilder](https://book.kubebuilder.io/) and run
in one operator binary.

---

## Problem 1: buddy-style dynamic IPAM

**Why.** The built-in node IPAM gives each node the same fixed range once, at creation. A
range cannot be resized, the cluster cannot grow into a non-adjacent range, and a block
in use cannot be shrunk. The scheduler does not track IPs, so pods on a full node stay
stuck in `ContainerCreating` while other nodes have free addresses.

**How.**
- The **controller** treats each pool CIDR as a buddy tree (/22 → two /23 → … → /28)
  and gives each node aligned power-of-two blocks, so each block is one route.
  - It grows a node at 80% use, or at once when a pod fails with "no IP addresses
    available", preferring the node's own free buddy so the two halves merge.
  - It drains and releases blocks when a node is quiet; running pods are never moved.
  - It spills into further, discontiguous CIDRs when the first is full.
- The **node agent** installs `block via <node IP>` routes, writes the `ptp` +
  `host-local` CNI config with the node's Active blocks, counts lease files for real
  usage, and reclaims leaked addresses.
- Block life: Pending → Active → Draining → Quarantined → Released.

**Result.** In simulation it served 100% of pods in all five workloads and up to 1000
nodes (a fixed /24 per node served as little as 26%), with 2–3× better address
utilisation than a /24 under light or uneven load and 1–3 routes per node. Calico-style
on-demand blocks pack about as well; buddy's gain is that allocation happens outside
the pod startup path.

## Problem 2: dual-CNI resilience

**Why.** When a CNI's data path breaks, the kubelet still reaches the API server over the
host network, so the node stays `Ready` and pods are not moved. Restarting containers
does not fix the network; the usual fix is a manual restart or a reboot.

**How.**
- Every protected pod has `eth0` on **Calico** (`172.16.0.0/16`, IPIP) and `net1` on
  **Flannel** (`10.244.0.0/16`, VXLAN), added by **Multus**. The two share no agent,
  address range or tunnel.
- A **health agent** on each node probes one peer per round, over both networks, with a
  small and a large request. It writes the results on its Node.
- The **operator** blames a node only when the probe pattern proves it. It then turns off
  the pods' readiness gate (Kubernetes drops `eth0` from the Service) and adds a
  **shadow EndpointSlice** with their `net1` addresses. It taints the node, restarts
  `calico-node` once, and fails back when every probe works again.

**Result.** On a 3-node cluster a broken primary path was detected in 10–31 s, then all
requests succeeded over Flannel. Failback took about 10 s with no failed request.
Without failover, about half the requests kept failing.

---

## Repository layout

```text
operator/
├── api/v1alpha1/ipam_types.go, cniresilience_types.go
├── cmd/main.go                # manager (runs ipam + cniresilience by default)
├── cmd/ipam-agent/            # IPAM node agent
├── cmd/ipam-sim/              # IPAM simulator (runs the real planner)
├── cmd/healthagent/           # CNI health agent
├── internal/controller/ipam/            # controller, buddy/, agent/, sim/
├── internal/controller/cniresilience/   # controller, healthagent/, failover/, recovery/
├── deploy/                    # ipam-agent, healthagent, calico-primary-pool, flannel-backup, multus-nad
├── config/samples/            # ipam_sample.yaml, cniresilience_sample.yaml
└── docs/                      # design docs and IPAM evaluation results
experiments/cni-resilience-experiment.md      # CNI resilience setup and experiments
experiments/manifests/resilience-workload.yaml
k8s-visualizer/                # dashboard: /project1 (IPAM), /project2 (CNI resilience)
report/project2/               # report and figures
```

## Documentation

| Document | What it covers |
|---|---|
| [`PROJECT2.md`](PROJECT2.md) | Project overview |
| [`Problem Statements`](<Kubernetes IPAM Efficiency & CNI Resilience — Problem Statements.md>) | Background, problem statements, early design options |
| [`operator/docs/ipam-controller.md`](operator/docs/ipam-controller.md) | Buddy IPAM controller |
| [`operator/docs/ipam-agent.md`](operator/docs/ipam-agent.md) | IPAM node agent |
| [`operator/docs/ipam-evaluation.md`](operator/docs/ipam-evaluation.md) | Simulator results against fixed and Calico-style allocation |
| [`operator/docs/ipam-phase0-assumptions.md`](operator/docs/ipam-phase0-assumptions.md) | Assumptions tested on a real cluster, cluster flags |
| [`operator/docs/ipam-decision-log.md`](operator/docs/ipam-decision-log.md) | Design decisions and what turned out wrong |
| [`operator/docs/cni-resilience.md`](operator/docs/cni-resilience.md) | Dual-CNI design: health agent, blame rule, failover, recovery |
| [`experiments/cni-resilience-experiment.md`](experiments/cni-resilience-experiment.md) | Cluster setup and the 10 measured fault experiments |
| [`operator/README.md`](operator/README.md) | Building, testing and running the operator |
| [`k8s-visualizer/README.md`](k8s-visualizer/README.md) | Dashboard pages |

---

## Prerequisites

- **Go** 1.26 (see `operator/go.mod`)
- **Docker**, **minikube** (v1.37 tested), **kubectl**
- **Node.js** 18+ (dashboard)

## Quickstart

**Problem 1, IPAM (no cluster needed):**
```bash
cd operator
go run ./cmd/ipam-sim --out docs/ipam-eval --seeds 3      # evaluation tables and charts
cd ../k8s-visualizer && npm install && npm run dev        # open http://localhost:5173/project1
```
On a live cluster: see the "Running" section of [`operator/README.md`](operator/README.md)
and [`operator/docs/ipam-agent.md`](operator/docs/ipam-agent.md).

**Problem 2, CNI resilience:** set up the Calico + Multus + Flannel cluster as in
[`experiments/cni-resilience-experiment.md`](experiments/cni-resilience-experiment.md), then:
```bash
cd operator && make run                          # terminal 1: ipam + cniresilience controllers
cd k8s-visualizer && npm run dev                 # terminal 2: open http://localhost:5173/project2
```

The two problems need different clusters: IPAM replaces the node's CNI, while CNI
resilience runs Calico and Flannel side by side.

## Tests and CI

```bash
cd operator
make test        # unit tests + envtest
make lint
go test ./internal/controller/ipam/... ./internal/controller/cniresilience/... -v
```

GitHub Actions (`.github/workflows/ci.yaml`) runs lint, tests, build and a manifest check
on every push and pull request to `main`.
