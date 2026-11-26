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

Everything runs on one Linux host with Docker; see [`testbed/README.md`](testbed/README.md).

```bash
testbed/bootstrap.sh                 # 3-node cluster with two networks, Calico, Multus, Flannel
testbed/build.sh                     # operator, agent, router and workload images
testbed/operator.sh start            # operator on the host
testbed/workload.sh up               # MQTT sensors and HTTP prober on A, broker and nginx on B
testbed/run.sh results/main --reps 10
python3 testbed/analysis/analyze.py runs results/main -o summary.csv
python3 testbed/analysis/plot.py summary.csv paper/figs
```

## Build and test the operator

```bash
cd operator
go test ./...          # unit tests
make build             # bin/manager
```

The paper builds with `latexmk -pdf main.tex` in `paper/` once the figures are in
`paper/figs/`.
