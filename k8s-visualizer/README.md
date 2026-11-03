# k8s-visualizer

React + TypeScript (Vite) dashboard used to demo both projects. It has three pages:

| Page | Project | What it shows | Needs |
|---|---|---|---|
| `/` | Project 1 | Cluster map, operator modules, fault buttons for the Project 1 experiments, pod logs | Cluster + `kubectl proxy` + operator with `--controllers=networkremediation` |
| `/project1` | Project 2, Problem 1 (IPAM) | In-browser simulation of the buddy IPAM planner, plus evaluation results | Nothing: no cluster |
| `/project2` | Project 2, Problem 2 (CNI resilience) | Live node paths, operator status, traffic bars, shadow EndpointSlices, fault buttons | Dual-CNI cluster + operator (`make run`) |

The routes `/project1` and `/project2` are named after the two **problems of Project 2**,
not after Project 1 and Project 2. Project 1's dashboard is the root page.

## Run

```bash
npm install
npm run dev        # http://localhost:5173
```

For `/`, also run `kubectl proxy` (port 8001); Vite forwards `/api` and `/apis` to it.

## How it talks to the cluster

- **Kubernetes API**: `/api` and `/apis` are proxied to `kubectl proxy` on
  `127.0.0.1:8001` (`vite.config.ts`). Used by the Project 1 page.
- **Fault API**: `vite-plugin-fault-api.ts` adds `/api/fault/*` endpoints to the dev
  server. They run `kubectl` and `docker exec` into the minikube node containers on the
  machine running Vite, so `docker` and `kubectl` must work there. The Project 2 page
  uses `/api/fault/cniresilience/*` (status, traffic, break, restore, config); it does
  not need `kubectl proxy`.
- **Path verdicts**: `path-verdict.ts` turns the `path-probe` pods' results into a
  working / broken / unknown verdict per node for the `/project2` page.

Every value on `/project2` is read from the cluster. If something cannot be read, the
page says so instead of guessing.

## Code

```text
src/App.tsx                      # Routes: /, /project1, /project2
src/components/ClusterMap.tsx, ControlPanel.tsx, LogTerminal.tsx, PodLogsModal.tsx   # Project 1 page
src/components/Project1Demo.tsx  # IPAM demo page (Project 2, Problem 1)
src/ipam/engine.ts               # Browser port of the IPAM planner rules
src/ipam/evalData.ts             # Results from operator/cmd/ipam-sim
src/components/Project2Demo.tsx  # CNI resilience page (Project 2, Problem 2)
vite-plugin-fault-api.ts         # /api/fault/* endpoints
path-verdict.ts                  # Per-node path verdicts
```

`src/ipam/engine.ts` follows the operator's planner rules and default parameters, with
time sped up. The real numbers come from `operator/cmd/ipam-sim`
(`operator/docs/ipam-evaluation.md`).

## Other scripts

```bash
npm run build      # type-check and build
npm run lint       # oxlint
```
