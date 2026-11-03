# How to Run the Projects

The repository holds two separate projects. The operator binary runs **only the
Project 2 controllers by default**; Project 1 needs `--controllers=networkremediation`.

## Project 1: network self-healing operator

Open **3 terminals** (cluster set up as in [`experiments/demo_experiments.md`](experiments/demo_experiments.md)).

### Terminal 1: Kubernetes API proxy
Exposes the Kubernetes API so the dashboard can read it.
```bash
kubectl proxy
```

### Terminal 2: the operator
```bash
cd operator
go run ./cmd/main.go --controllers=networkremediation
```

### Terminal 3: the dashboard
```bash
cd k8s-visualizer
npm run dev          # open http://localhost:5173
```

## Project 2: IPAM efficiency and CNI resilience

### Problem 1, IPAM demo (no cluster needed)
```bash
cd k8s-visualizer
npm run dev          # open http://localhost:5173/project1
```

IPAM evaluation (writes `operator/docs/ipam-eval/`):
```bash
cd operator
go run ./cmd/ipam-sim --out docs/ipam-eval --seeds 3
```

IPAM on a live cluster: see [`operator/docs/ipam-agent.md`](operator/docs/ipam-agent.md) and the
"Running Project 2" section of [`operator/README.md`](operator/README.md).

### Problem 2, CNI resilience (cluster set up as in [`experiments/cni-resilience-experiment.md`](experiments/cni-resilience-experiment.md))

`kubectl proxy` is not needed for this page.

```bash
# Terminal 1: the operator (gated pods are only Ready while it runs)
cd operator
make run             # = go run ./cmd/main.go (ipam + cniresilience)

# Terminal 2: the dashboard
cd k8s-visualizer
npm run dev          # open http://localhost:5173/project2
```
