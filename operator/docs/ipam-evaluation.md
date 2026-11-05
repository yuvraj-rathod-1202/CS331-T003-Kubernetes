# IPAM evaluation (Project 2)

How the buddy controller compares with the usual alternatives. Regenerate with

```bash
go run ./cmd/ipam-sim --out docs/ipam-eval --seeds 3
```

Outputs are in `docs/ipam-eval/`: `results.md` (the table below), `summary.csv`
(every run), `series-burst.csv`, `sweep.csv` and four SVG charts.

## What is simulated

`internal/controller/ipam/sim` runs the **controller's own planning code**
(`ipam.PlanPool`) on `NodeBlock` objects, with the node agent modelled as fixed
delays (10 s to route and promote a block, 10 s drain settle). Pods are counted,
not scheduled. Each pod lives about 10 minutes on average, and pods in different
blocks are replaced at random, which is what keeps blocks from emptying. Time
step is 5 s. Every scenario starts from a two-minute ramp, and the first two
minutes are left out of the averages.

Baselines:

| name | what it is |
|---|---|
| `static-/24` | one fixed /24 per node (kube-controller-manager node IPAM default), 253 usable after the same 3-address overhead |
| `static-/25` | a fixed /25 per node, sized for 110 pods |
| `calico-64` | Calico-style 64-address blocks taken on demand when a node has no free address, released when empty, no per-block overhead |
| `buddy` | this project, default parameters |
| `buddy-no-shrink`, `buddy-no-emergency` | ablations |

Scenarios use 40 to 50 nodes: steady (80 pods each), skewed (10% of nodes at 220
pods, the rest at 30, max-pods 250), burst (20 → 200 → 20 pods), wave
(per-node sine, random phase) and tight-pool (a /20 shared by 40 nodes, four
of them hot). The sweep uses a /16 and 60 pods per node.

## Results (mean of 3 seeds)

| scenario | policy | pods served % | unmet pod-min | peak unmet | utilisation % | addrs/node | peak routes | block ops |
|---|---|---|---|---|---|---|---|---|
| steady | buddy | 100.0 | 8 | 50 | 71.4 | 112 | 200 | 300 |
| steady | static-/24 | 100.0 | 0 | 0 | 31.2 | 256 | 50 | 0 |
| steady | static-/25 | 100.0 | 0 | 0 | 62.5 | 128 | 50 | 0 |
| steady | calico-64 | 100.0 | 0 | 0 | 62.5 | 128 | 100 | 100 |
| steady | buddy-no-emergency | 100.0 | 8 | 50 | 71.4 | 112 | 200 | 300 |
| steady | buddy-no-shrink | 100.0 | 8 | 50 | 71.4 | 112 | 200 | 300 |
| skewed | buddy | 100.0 | 24 | 85 | 55.7 | 88 | 84 | 107 |
| skewed | static-/24 | 100.0 | 0 | 0 | 19.1 | 256 | 50 | 0 |
| skewed | static-/25 | 80.6 | 13523 | 475 | 30.9 | 128 | 50 | 0 |
| skewed | calico-64 | 100.0 | 0 | 0 | 58.9 | 83 | 65 | 66 |
| skewed | buddy-no-emergency | 100.0 | 107 | 135 | 55.7 | 88 | 85 | 106 |
| skewed | buddy-no-shrink | 100.0 | 24 | 85 | 55.7 | 88 | 84 | 107 |
| burst | buddy | 100.0 | 0 | 0 | 37.7 | 188 | 550 | 2078 |
| burst | static-/24 | 100.0 | 0 | 0 | 32.0 | 256 | 50 | 0 |
| burst | static-/25 | 73.1 | 64062 | 3750 | 46.8 | 128 | 50 | 0 |
| burst | calico-64 | 100.0 | 0 | 0 | 38.2 | 217 | 200 | 212 |
| burst | buddy-no-emergency | 100.0 | 0 | 0 | 37.6 | 188 | 550 | 2067 |
| burst | buddy-no-shrink | 100.0 | 0 | 0 | 34.4 | 246 | 550 | 750 |
| wave | buddy | 100.0 | 254 | 1017 | 46.5 | 154 | 298 | 2358 |
| wave | static-/24 | 100.0 | 0 | 0 | 27.8 | 256 | 50 | 0 |
| wave | static-/25 | 96.6 | 14478 | 222 | 53.7 | 128 | 50 | 0 |
| wave | calico-64 | 100.0 | 0 | 0 | 52.4 | 137 | 115 | 337 |
| wave | buddy-no-emergency | 100.0 | 367 | 1017 | 46.8 | 153 | 312 | 2412 |
| wave | buddy-no-shrink | 100.0 | 254 | 1017 | 41.0 | 175 | 328 | 344 |
| tight-pool | buddy | 100.0 | 12 | 52 | 64.8 | 86 | 80 | 80 |
| tight-pool | static-/24 | 57.1 | 27864 | 960 | 31.2 | 102 | 16 | 0 |
| tight-pool | static-/25 | 72.3 | 17812 | 620 | 39.6 | 102 | 32 | 0 |
| tight-pool | calico-64 | 100.0 | 0 | 0 | 67.3 | 83 | 52 | 53 |
| tight-pool | buddy-no-emergency | 100.0 | 55 | 96 | 64.8 | 86 | 84 | 84 |
| tight-pool | buddy-no-shrink | 100.0 | 12 | 52 | 64.8 | 86 | 80 | 80 |

*pods served* is the share of wanted pods that got an address; *unmet pod-min*
is pod-minutes of demand without an address; *peak routes* is the number of
route prefixes a node holds to the others (one per node is the minimum).

Sweep, 60 pods per node in a /16 (`sweep.svg`, `sweep.csv`):

| nodes | buddy | static-/24 | static-/25 | calico-64 |
|---|---|---|---|---|
| 200 | 100% | 100% | 100% | 100% |
| 400 | 100% | 64% | 100% | 100% |
| 800 | 100% | 32% | 64% | 100% |
| 1000 | 100% | 26% | 51% | 100% |

At 1000 nodes the pool is 91.5% full of pods; buddy holds all of it (100% of the
/16 handed out), Calico 97.7%.

Two later rule changes, and what they cost or gained here:

- **The starting block is never given back** (see the controller doc): 0.3–0.4
  points of utilisation in burst and wave; served and unmet demand unchanged.
- **Growth is sized to what the node's pods are short of**, not to 50% of its
  current capacity (see the controller doc). Pods served is unchanged (100% in
  every scenario and the whole sweep), and packing improves: steady 62.5% →
  71.4% utilisation, burst 35.1% → 37.7%, wave 41.0% → 46.5%, with 10–25% fewer
  addresses per node (burst 219 → 188, wave 175 → 154, steady 128 → 112). The
  cost is more, smaller blocks: peak routes in the burst 202 → 550 (11 per
  node), steady 150 → 200, wave 145 → 298; block operations about two to three
  times higher; the sweep needs 2 prefixes per node as before. Waiting did not
  get worse: wave unmet pod-minutes 366 → 254, skewed 12 → 24, tight pool 2 →
  12, burst still 0. Growth also looks at the free neighbour of a node's
  *merged* range, not only of each small block; without that the burst's peak
  was 750 routes.

The simulator counts addresses per block, not which ones are used, so it never
sees an empty half: splitting a block is not simulated.

## What the numbers say

1. **Fixed blocks fail in the way expected.** A /24 per node wastes 68–81% of
   the space at light load and stops admitting nodes at 256. A tuned /25 is
   efficient when load is even, but drops 19–27% of pods when load is skewed or
   bursts, because a node cannot borrow.
2. **Buddy serves everything the pool can hold** in all five scenarios and the
   whole sweep, with unmet demand of at most a few pod-minutes after warm-up (a
   pod waits for a new block to be routed and promoted, about 15–20 s). It holds
   27–66% fewer addresses per node than a /24, depending on how light or uneven
   the load is (66% in the skewed scenario, 27% in the burst, which peaks near
   a full node).
3. **Calico-style blocks still pack better when load changes**: wave 52%
   utilisation against buddy's 46%; in the burst they are level (38% and 38%),
   and in the steady run buddy packs better (71% against 62.5%). In skewed
   load Calico is slightly ahead (59% against 56%). Calico needs no headroom or
   lead time because the block is taken inside the CNI call. Buddy's reason to
   exist is not better packing; it is that allocation happens in the background,
   away from pod startup, with no cluster-wide block store in the CNI path. This
   is a trade-off, not a win on every metric.
4. **Route table:** buddy holds up to 4 prefixes per node in the steady run
   (200 for 50 nodes), 6 in the wave (298), 11 in the burst (550) and 2 across
   the sweep, against 1 for the static policies and 1–4 for Calico (200 in the
   burst). This is what sizing growth to the need costs: more, smaller blocks.
5. **Shrink matters, and is slow.** Without it buddy holds 175 addresses per
   node in the wave scenario against 154 with it (utilisation 41% → 46%), and
   246 against 188 in the burst, at about seven times the block operations in
   the wave. Giving space back is limited by pod lifetime, not by the
   controller: a drained block empties only as its pods end.
6. **Why the 1000-node case used to fail, and what fixed it.** The first
   version served 86% there. Two causes, both in how blocks are placed:
   - first blocks of adjacent nodes were buddies of each other, so no node
     could later double in place, and growth took pieces that could not merge;
   - every block paid its own 3-address overhead, so two /27s held 58 pods,
     not the 61 of one /26.
   Fixes: first blocks are now spread (each leaves its buddy free), growth
   takes from the node's own buddy (first up to 2× the need; now only the need,
   see above), and the agent renders buddy blocks as one `host-local` range, which the
   controller counts the same way. A tighter-headroom mode under pool pressure
   was also tried and removed: it changed nothing once placement was fixed.
7. **The emergency grow shows no gain in the model**, because the model's
   usage signal is exact. Its value is in the real case where usage lags (shown
   on the cluster: with the agent paused and a block full, the controller grew
   the node from the kubelet event alone), so this result says nothing against
   it.

## Limits of this evaluation

- It is a model. It does not measure pod start latency, CNI call cost, packet
  forwarding or the real cost of a route-table entry, and the baselines are
  models of their policies, not their code.
- The agent's delay is fixed; a slow or partitioned node would lengthen it.
- Pod lifetime and placement are simple (exponential lifetime, random block).
- Numbers depend on parameters (growth 50%, threshold 80%, cooldown 300 s).
  They were not tuned per scenario, and three seeds show the trend, not
  confidence intervals.
- The 500-pods-per-node case cannot run on this hardware (see
  `ipam-phase0-assumptions.md`, item 8), so large-node results come from here.

## Charts

`burst-addresses.svg`, `pods-served.svg`, `utilization.svg`, `sweep.svg`.
They follow the light/dark colour scheme of the viewer.
