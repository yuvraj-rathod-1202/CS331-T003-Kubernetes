# Project 2: Kubernetes IPAM Efficiency & CNI Resilience

Project 2 is a **separate project** from Project 1, not a new version of it. Project 1
built an operator that detects and repairs common network failures (CNI crashes,
CoreDNS, NetworkPolicy, pod-to-pod connectivity). Project 2 looks at two design
weaknesses in how pod networking itself works, framed by our professor as
*"one is efficiency, the other is resiliency."*

The code lives in the same Go operator binary as Project 1, but it uses its own
custom resources and controllers and does not depend on Project 1's
`NetworkRemediation` resource or its Check → Evaluate → Remediate modules.

| | Problem 1: IPAM efficiency | Problem 2: CNI resilience |
|---|---|---|
| Question | How do we hand out pod IPs so none are stranded and no node starves? | How does a pod stay reachable when its CNI data path breaks? |
| Our solution | Buddy-style IPAM: power-of-two, aligned blocks per node that grow and shrink with demand | Two interfaces per pod (Calico + Flannel via Multus), health agents, failover of Service traffic |
| Custom resources | `IPPool`, `NodeBlock` | `CNIResilience` |
| Controller | `ipam` | `cniresilience` |
| Node component | `ipam-agent` DaemonSet | `cni-health-agent` DaemonSet |
| Design doc | [`ipam-controller.md`](operator/docs/ipam-controller.md), [`ipam-agent.md`](operator/docs/ipam-agent.md) | [`cni-resilience.md`](operator/docs/cni-resilience.md) |
| Evaluation | [`ipam-evaluation.md`](operator/docs/ipam-evaluation.md) (simulator + live checks) | [`cni-resilience-experiment.md`](experiments/cni-resilience-experiment.md) (10 fault experiments) |
| Dashboard page | `/project1` (simulation, no cluster needed) | `/project2` (live cluster) |

> The dashboard routes are named after the **problem** number inside Project 2:
> `/project1` is the IPAM demo (Problem 1), `/project2` is the CNI resilience demo
> (Problem 2). Project 1's own dashboard is the root page `/`.

## Problem 1: buddy-style dynamic IPAM

**Problem.** Kubernetes gives every node a fixed block of pod addresses (default /24)
once, at creation. Quiet nodes waste most of it; busy nodes run out and their pods stay
in `ContainerCreating`, though other nodes have free addresses. Ranges cannot be resized,
growth into a non-adjacent range is not supported, and blocks in use cannot be shrunk.

**Solution.**
- The **controller** treats each pool CIDR as a buddy tree (/22 → two /23 → ...
  → /28) and gives each node aligned power-of-two blocks, so each block is still one
  route. It grows a node at 80% use (or at once on a "no IP addresses available"
  event), prefers the node's own free buddy, drains and releases blocks when a node is
  quiet, and spills into further, discontiguous CIDRs.
- The **node agent** installs `block via <node IP>` routes, writes the `ptp` +
  `host-local` CNI config with the node's Active blocks, counts lease files for real
  usage, and reclaims leaked addresses.
- Block life: Pending → Active → Draining → Quarantined → Released.

**Result.** In simulation it served 100% of pods in all five workloads and up to 1000
nodes (a fixed /24 per node: as low as 26%), with 2–3× better address utilisation than
a /24 under light or uneven load and 1–3 routes per node. Calico-style on-demand blocks
pack about as well; buddy's gain is allocation outside the pod startup path.

## Problem 2: dual-CNI resilience

**Problem.** A pod has one interface from one CNI. When that CNI's data path breaks, the
node stays `Ready`, pods are not moved, and the usual fix is a manual restart or reboot.

**Solution.**
- Every protected pod has `eth0` on **Calico** (`172.16.0.0/16`) and `net1` on
  **Flannel** (`10.244.0.0/16`), added by **Multus**.
- A **health agent** on each node probes one peer per round over both networks and
  writes the results on its Node.
- The **operator** blames a node only when the probe pattern proves it, then turns off
  the pods' readiness gate (Kubernetes drops `eth0` from the Service) and adds a
  **shadow EndpointSlice** with their `net1` addresses. It taints the node and restarts
  `calico-node` once, and fails back when every probe works again.

**Result.** On a 3-node cluster a broken primary path was detected in 10–31 s, then all
requests succeeded over Flannel; failback took about 10 s with no failed request.
Without failover, about half the requests kept failing.

## Where things are

```text
operator/
├── api/v1alpha1/ipam_types.go, cniresilience_types.go
├── cmd/ipam-agent/        # IPAM node agent
├── cmd/ipam-sim/          # IPAM simulator (runs the real planner)
├── cmd/healthagent/       # CNI health agent
├── internal/controller/ipam/           # controller, buddy/, agent/, sim/
├── internal/controller/cniresilience/  # controller, healthagent/, failover/, recovery/
├── deploy/                # ipam-agent, healthagent, calico-primary-pool, flannel-backup, multus-nad
├── config/samples/        # ipam_sample.yaml, cniresilience_sample.yaml
└── docs/                  # ipam-*.md, ipam-eval/, cni-resilience.md
experiments/cni-resilience-experiment.md, experiments/manifests/resilience-workload.yaml
k8s-visualizer/src/components/Project1Demo.tsx (IPAM), Project2Demo.tsx (CNI resilience)
report/project2/           # report
```

## Running

The operator runs **only the Project 2 controllers by default**:

```bash
cd operator
make install                       # all CRDs
go run ./cmd/main.go               # same as --controllers=ipam,cniresilience
go run ./cmd/main.go --controllers=ipam            # IPAM only
go run ./cmd/main.go --controllers=cniresilience   # CNI resilience only
go run ./cmd/ipam-sim --out docs/ipam-eval --seeds 3   # IPAM evaluation
```

The two problems need different clusters (IPAM replaces the CNI, CNI resilience runs
Calico + Flannel). Full setup:

- IPAM: [`operator/docs/ipam-agent.md`](operator/docs/ipam-agent.md),
  [`operator/docs/ipam-phase0-assumptions.md`](operator/docs/ipam-phase0-assumptions.md)
  (cluster flags), [`operator/docs/ipam-evaluation.md`](operator/docs/ipam-evaluation.md).
- CNI resilience: [`experiments/cni-resilience-experiment.md`](experiments/cni-resilience-experiment.md).

## Background

- Problem statements, background and early design options:
  [`Kubernetes IPAM Efficiency & CNI Resilience — Problem Statements.md`](<Kubernetes IPAM Efficiency & CNI Resilience — Problem Statements.md>)
- IPAM decisions and what turned out wrong:
  [`operator/docs/ipam-decision-log.md`](operator/docs/ipam-decision-log.md)
