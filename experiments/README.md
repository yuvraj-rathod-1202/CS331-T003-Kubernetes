# Experiments

This directory holds the guides and manifests used to reproduce failures in a real
minikube cluster. It serves **two separate projects**.

## Project 1: network self-healing

Before building the Project 1 operator, each failure mode (CNI crashes, CoreDNS
degradation, NetworkPolicy misconfiguration, pod connectivity issues) was reproduced
by hand. The experiments serve to:

- **Reproduce failures** in a controlled minikube environment.
- **Validate detection signals**: which metrics, pod statuses or events reliably show
  the failure.
- **Test remediation steps** by hand before automating them in the operator.
- **Document findings**, edge cases and gotchas.

| File | Description |
|---|---|
| `setup.md` | Cluster setup (minikube + Calico, test pods, Prometheus/Grafana) |
| `cni-failure.md` | Calico agent crash, IPAM pool exhaustion, disabled IPPool |
| `coredns-failure.md` | CoreDNS degradation and DNS resolution failures |
| `networkpolicy-failure.md` | NetworkPolicy enforcement issues |
| `pod-connectivity-failure.md` | Pod-to-pod and inter-node connectivity failures |
| `demo_experiments.md` | Step-by-step demo of the Project 1 experiments |

Run the Project 1 operator with `go run ./cmd/main.go --controllers=networkremediation`
(from `operator/`).

## Project 2: IPAM efficiency and CNI resilience

| File | Description |
|---|---|
| `cni-resilience-experiment.md` | Problem 2: dual-CNI cluster setup (Calico + Multus + Flannel) and 10 fault experiments with measured results |

Problem 1 (buddy IPAM) is evaluated with the simulator and on its own cluster; see
[`operator/docs/ipam-evaluation.md`](../operator/docs/ipam-evaluation.md) and
[`operator/docs/ipam-agent.md`](../operator/docs/ipam-agent.md).

## Manifests (`manifests/`)

| File | Used by |
|---|---|
| `resilience-workload.yaml` | Project 2, CNI resilience (gated app pods with `net1`, Service, traffic client, path probes) |
| `tiny-ippool.yaml` | Project 1, Calico IPAM exhaustion experiment |
| `backend-api-deployment.yaml`, `frontend-probe-deployment.yaml`, `dns-checker-deployment.yaml`, `dns-load-generator.yaml`, `dns-test-service.yaml` | Project 1 test workloads |
| `netpolicy-*.yaml`, `networkpolicy-*.yaml` | Project 1, NetworkPolicy experiments |
| `prometheus-stack.yaml` | Project 1 monitoring |
