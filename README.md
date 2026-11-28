# Link-Level Self-Healing for Container Networks at the IoT Edge

Code, testbed and paper for a self-healing pod network for Kubernetes edge clusters.

Kubernetes notices a failure only when a node stops reporting to the control plane, so a
broken path between two healthy nodes silently drops the traffic between them. This
system repairs such faults per node pair:

- Every node runs two pod networks over separate links: **Calico** (IPIP, primary,
  `172.16.0.0/16`) and **Flannel** (VXLAN, backup, `10.244.0.0/16`), attached through
  **Multus**.
- A **health agent** on every node probes every peer on both networks.
- A **link router** on every node steers traffic for a broken pair onto the backup
  network, or through a relay node, with Linux policy routing. No pod or Service address
  changes.
- An **operator** (custom resource `CNIResilience`) aggregates the reports and fails a
  node over to its pods' backup addresses when its whole CNI data plane is broken.

## Layout

```text
operator/                   Go operator, health agent and link router (Kubebuilder)
  cmd/main.go               manager (CNIResilience controller)
  cmd/healthagent/          health agent DaemonSet binary
  cmd/linkrouter/           link router DaemonSet binary
  internal/controller/cniresilience/   controller, healthagent/, linkrouter/, failover/, recovery/
  api/v1alpha1/             CNIResilience API
  deploy/                   Calico pool, Flannel backup, Multus attachment, agent and router DaemonSets
  docs/cni-resilience.md    design
testbed/                    edge testbed: cluster setup, fault injection, workload, campaign, analysis
paper/                      paper source (LaTeX) and compiled PDF
```

## Reproduce the evaluation

The paper's results come from a 3-node K3s cluster (K3s v1.35 in Docker containers via
k3d) on one Linux host; see [`testbed/README.md`](testbed/README.md). The harness also
runs on minikube (`DISTRO=minikube`, `testbed/bootstrap.sh`).

```bash
export DISTRO=k3s
testbed/bootstrap-k3s.sh             # K3s cluster with two networks, Calico, Multus, Flannel, images
testbed/campaign-all.sh results      # whole evaluation, fresh clients every run (about 13 h)
testbed/ablations.sh results         # pinning and source-NAT experiments (about 1 h)
python3 testbed/analysis/analyze.py runs results/main -o summary.csv
python3 testbed/analysis/plot.py summary.csv paper/figs
python3 testbed/analysis/paper_numbers.py results   # every number the paper quotes
```

## Build and test the operator

```bash
cd operator
go test ./...          # unit tests
make build             # bin/manager
```

The paper builds with `latexmk -pdf main.tex` in `paper/` once the figures are in
`paper/figs/`.
