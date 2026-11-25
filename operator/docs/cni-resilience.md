# CNI resilience (Project 2, Problem 2)

Every pod normally has one network interface from one CNI. If that CNI's data path
breaks on a node, the pods there are unreachable while the node still reports `Ready`,
so Kubernetes does nothing. This part of Project 2 gives pods a second, independent
network path and moves Service traffic to it when the first one is proven broken.

Code: `internal/controller/cniresilience/` (controller, `healthagent/`, `failover/`,
`recovery/`, `linkrouter/`), `cmd/healthagent/`, `cmd/linkrouter/`. Experiment guides and
measured results:
[`experiments/cni-resilience-experiment.md`](../../experiments/cni-resilience-experiment.md)
(node-level failover),
[`experiments/link-steering-experiment.md`](../../experiments/link-steering-experiment.md)
(per-link steering).

## Two networks per pod

| | Primary | Backup |
|---|---|---|
| Interface | `eth0` | `net1` |
| CNI | Calico (IPIP) | Flannel (VXLAN) |
| Address space | `172.16.0.0/16` | `10.244.0.0/16` (node podCIDRs) |
| Node agent | `calico-node` | `flanneld` |
| Added by | default network | Multus, `kube-system/flannel-backup` |

The two share no agent, address range or tunnel, so one can fail while the other
works. Two copies of one CNI would fail together.

- `deploy/calico-primary-pool.yaml` moves Calico to `172.16.0.0/16`, because Flannel
  can only use the nodes' podCIDRs (`10.244.x.0/24`).
- `deploy/flannel-backup.yaml` is the official Flannel manifest without the part that
  makes it a node's default CNI.
- `deploy/multus-nad.yaml` defines `flannel-backup` with `isDefaultGateway: false`, so
  the default route stays on `eth0`.

## Objects

- `CNIResilience` (cluster-scoped, one per cluster): names both CNIs and holds the
  probe settings and switches. Sample: `config/samples/cniresilience_sample.yaml`.

  | Field | Sample | Meaning |
  |---|---|---|
  | `probeIntervalSeconds` | 2 | One probe round every 2 s |
  | `probeTimeoutSeconds` | 1 | Timeout of one probe |
  | `failureThreshold` | 3 | Identical results in a row before a path changes state |
  | `autoFailover` | true | Move Service traffic to `net1` |
  | `autoRecover` | true | Taint the node and restart its `calico-node` once |
  | `linkFailover` | true | Steer a single broken node-to-node link onto the backup path |
  | `linkRelay` | true | Relay a link through a third node when no direct path works |

  Status: `phase` (`Healthy`, `Degraded`, `FailedOver`, `Unknown`), and per node
  `primaryHealthy`, `backupHealthy`, `activePath` (`primary`, `backup`, `none`) and a
  message. `failoverCount` and `recoveryCount` count episodes. Per-link status
  (`links`, condition `LinksReachable`) is described under "Per-link steering".
- **Path report**: annotation `cniresilience.cn-operator/path-report` on each Node,
  written by that node's agent.
- **Link routes report**: annotation `cniresilience.cn-operator/link-routes` on each
  Node, written by that node's link router. It lists the path chosen for every peer
  (`primary`, `backup`, `relay` with `via`, or `none`).
- **Shadow EndpointSlice**: one per original slice and broken node, listing backup IPs.
- **Readiness gate** `cniresilience.cn-operator/primary-path` on protected pods
  (listed under `spec.readinessGates`), controlled by the operator.
- **Taint** `NoSchedule` on a node in a fault episode.

## Health agent (`cmd/healthagent`, `deploy/healthagent-daemonset.yaml`)

A DaemonSet pod on every node, with both interfaces. It is a normal pod, so its
probes cross the same data path as application pods.

- **Serves** `/small` and `/large` on port 9100.
- **Probes** every peer each round, in parallel, over `eth0` and over `net1` separately. Each probe
  is a small request and a 1400-byte one: a path with a too-small MTU drops full-size
  segments while small requests still pass.
- **All peers, every round.** One round takes at most one probe timeout, because the
  probes run in parallel. Each node probes all N-1 peers, so a round costs N² probes
  per path across the cluster (6 on 3 nodes). That is fine up to about 50 nodes. Every
  node needs its own fresh verdict on every peer, because the link router steers from it.
- **Starts from its last report.** A restarted agent reads its own Node's
  `path-report`. If it is younger than 30 s, each peer starts in the state it had, with
  the streak at `failureThreshold`, so one opposite result does not flip it. Otherwise
  it starts healthy. Without this, every router would drop its routes for 6-10 s after
  an agent restart while the link is still broken.
- **Streaks.** A path flips state only after `failureThreshold` identical results in
  a row. One lost packet or one lucky reply changes nothing.
- **Report.** The agent writes the latest result per peer to its own Node annotation,
  and rewrites it at least every 10 s. A report older than 30 s means the node is
  *silent*.
- Agents probe only each other. `deploy/healthagent-networkpolicy.yaml` allows port
  9100 on the primary network, so an application NetworkPolicy never looks like a
  broken path. Standard NetworkPolicy does not apply to Multus interfaces, so the
  backup probes are never blocked.

## Blame rule (`healthagent.Classify`)

A failed probe A → B can mean A is broken, B is broken, or only the link is. The
operator turns the reports into probe edges and blames node X only if:

1. every probe **into** X failed, and
2. every probe **out of** X failed, or X is silent, and
3. some probe between two **other** nodes worked.

A remaining failure between two nodes that are each seen working elsewhere is a
broken link. Anything else is **ambiguous**, and the operator does nothing that round.
Primary and backup edges are classified separately.

On N nodes at most N − 2 broken nodes can be located. With 3 nodes and both workers
broken, every probe touches a broken node, and the phase is `Unknown`.

## Reconcile (`cniresilience_controller.go`)

Triggered by `CNIResilience` spec changes and Node report changes (status writes do
not trigger it). Every pass first turns the gate on for gated pods that have no
condition yet, except on nodes that are failed over. Then, for each node:

| Node health | Action |
|---|---|
| No report involves it | Nothing is proven, nothing changes |
| Primary proven broken, backup works | **Fail over** |
| Primary proven broken, backup broken or missing | **Isolate** |
| Primary proven broken, `autoFailover: false` | Report only |
| Not blamed, but a probe through it still fails | Keep the current state |
| Every probe touching it works | **Fail back** |

On a proven primary failure with `autoRecover`, the node is also tainted. The round
that adds the taint restarts `calico-node` on that node, once per episode.

### Fail over (`failover.FailoverNode`)

Services know only a pod's primary IP. Two standard features change that:

1. **Readiness gate off.** The operator sets the pod's custom condition to `False`.
   Kubernetes itself then removes the `eth0` address from the Service. Editing the
   EndpointSlice directly does not last: the EndpointSlice controller rewrites it
   within about a second.
2. **Shadow EndpointSlice.** For each slice that holds pods on the node, the operator
   writes a slice for the same Service that lists their `net1` addresses (read from
   Multus's `network-status` annotation). kube-proxy sends traffic there.

Only pods with both a gate and a backup IP are touched. If no Service sends traffic to
the node's pods, nothing moves and the node is not counted as failed over.

### Isolate (`failover.IsolateNode`)

Neither path reaches the node, so a shadow would point at a dead address. The gate goes
off with no shadow: the pods leave the Service, and traffic goes to replicas elsewhere.
`activePath` is `none`.

### Recover (`recovery`)

- **Taint** the node `NoSchedule`, so no new pods land there. The taint stays until
  the node is proven working, so it marks one fault episode and survives an operator
  restart.
- **Restart `calico-node`** on the node only in the round that adds the taint. In the
  lab a restart never fixed a data-path fault (an iptables DROP survives it), and each
  restart briefly takes the node's network down. A recovery is counted only when the
  probes show the path working again.

### Fail back (`failover.FailbackNode`)

Only when every probe touching the node works:

1. gate back on, so `eth0` returns to the Service;
2. then delete the node's shadow slices;
3. remove the taint.

The gate goes on before the shadows go away, so the Service is never left without the
pod. In the lab, failback caused no failed request.

## Per-link steering (`cmd/linkrouter`, `deploy/linkrouter-daemonset.yaml`)

The blame rule finds a broken **node**. When only the link between two nodes is broken
(A to B fails, but A and B each work with everyone else), `Classify` reports a broken
link, and node-level failover does nothing. Per-link steering fixes that case. Each
node steers its own outgoing traffic, one peer at a time.

### Components

| Component | Role |
|---|---|
| `cni-health-agent` | Probes every peer, writes `path-report` (see above) |
| `cni-link-router` (DaemonSet, `hostNetwork`, `NET_ADMIN`) | Reads the reports, decides a path per peer, writes host routes and rules, reports its choices on its Node |
| Operator | Copies the routers' choices into `status.links`, sets the condition, emits Events |

The router decides only for traffic leaving its own node. No central component is in
the data path. If the operator is down, the routers keep working. If a router stops, its
last routes stay in place.

### Decision (per peer B, on node A)

1. A's own report missing or older than 30 s: change nothing.
2. `linkFailover: false`: `primary` for every peer, all routes removed.
3. A to B over Calico works: `primary` (no route).
4. A to B over Flannel works: `backup`. Route B's Calico blocks via B's Flannel address
   (the network address of B's `podCIDR`) on `flannel.1`.
5. `linkRelay` on and a relay C exists: `relay`. Route B's blocks via C's node IP on
   `tunl0`.
6. Otherwise `none`: no route, and an alert.

A relay candidate C is another node (not A or B) with a fresh report, where A to C over
**Calico** works and C reaches B directly on at least one path. C's own router picks
whichever direct path works. A candidate whose own path to B is itself a relay is
skipped, so there is at most one intermediate hop. A relay that is still a candidate is
kept (no churn). Otherwise the lowest node name wins. A peer that is not in A's report
yet gets `primary`. A missing backup result counts as down.

Relays are reached over Calico only. Service traffic is masqueraded to the sender's
outgoing tunnel address, so over Flannel B's replies would take the broken Flannel B to
A path.

Calico's block addresses never change, so open connections survive the switch. The
steering comes after kube-proxy, so direct pod-IP traffic and Service traffic are both
covered.

### What the router installs

All of it is owned by the router. It never reads or deletes anything else.

| Item | Content |
|---|---|
| Table `200` | One route per steered peer block: `<block> via <next hop> dev <flannel.1 or tunl0> onlink`, route protocol `100` (the IPAM agent uses 99) |
| Rule, pref 90 | `ipproto tcp dport 9100 lookup main`: probes to peers use the real path |
| Rule, pref 91 | `ipproto tcp sport 9100 lookup main`: probe replies use the real path |
| Rule, pref 100 | `lookup 200`: steering. An empty table falls through to `main` |

The two probe rules stop flapping. The probes keep measuring the real Calico path.
Without them a steered link would look healthy, the route would go, and the link would
fail again. The rules match the probe **port**, not the probe pod's address, because
excluding the pod's address would also exclude all other traffic to that pod.

Flags (defaults): `--table 200`, `--protocol 100`, `--probe-port 9100`,
`--interval 10s`, `--node-name` (from `NODE_NAME`). The router syncs on every watch event
(debounced 300 ms) and at least every 10 s, as a diff. On start it adopts what is in
table 200. On SIGTERM it leaves the routes. `--cleanup` deletes the routes and rules and
exits (command in the header of `deploy/linkrouter-daemonset.yaml`).

### Report, status, condition, Events

- The router writes `cniresilience.cn-operator/link-routes` on its Node when its choices
  change, and at least every 10 s:
  `{"time": ..., "links": {"<peer>": {"path": "relay", "via": "<node>"}}}`.
- The operator ignores reports older than 30 s (unknown, not a change).
- `status.links`: one entry per non-primary link, with `from`, `to`, `path`, `via` and
  `since`.
- Condition `LinksReachable`: `True`, or `False` with reason `NoPath` and the pairs
  (`A→B, A→C`) in the message.
- Events on the `CNIResilience` object: `NoPath` (Warning) when a pair becomes `none`,
  `PathRestored` (Normal) when it leaves. Only on transitions.
- Phase: a `none` link makes the phase at least `Degraded`. Backup or relay links alone
  do not change it.

### Switches

| Field | Default | Effect when `false` |
|---|---|---|
| `linkFailover` | true | No steering at all: every peer `primary` |
| `linkRelay` | true | A link with both direct paths down is `none` instead of a relay |

### With node-level failover

The two layers are independent. When node B is proven broken, Service traffic moves to
B's `net1` addresses (never steered), and at the same time every peer's router steers
B's Calico blocks over Flannel for direct pod traffic. If B is cut off on both paths,
the operator isolates it as before, and every router reports `none` for B. NetworkPolicy
still applies, because a rerouted packet enters the pod through its Calico interface.

### Measured results

See [`experiments/link-steering-experiment.md`](../../experiments/link-steering-experiment.md),
section 7. The results table is not filled in yet.

### Link steering limits

- If A reaches every relay candidate only over Flannel, a link with both direct paths
  down gets no relay: `none` plus an alert.
- Service traffic is still masqueraded to the node's tunnel address (existing
  behaviour), so B's pods see the node address, not the client pod's.
- On 3 nodes, any fault that leaves a pair with no relay also isolates a node at the
  node level, so the two layers act together.
- Detection takes the agent's streak (`failureThreshold` x `probeIntervalSeconds`, about
  6 s) plus report and reconcile time.
- A one-way fault makes both ends steer, because probes are round trips.
- Host-network traffic is steered as a side effect, but this is not tested or promised.
- Only Calico IPIP plus Flannel VXLAN, IPv4, about 50 nodes at most, one relay at most.

## State lives in the cluster

Shadow slices, gate conditions and taints are all Kubernetes objects. The operator keeps
only a short recovery backoff in memory. If it stops during a failover, traffic keeps
flowing, and after a restart it reads the state back and carries on.

## Measured results (3 nodes, see the experiment guide)

| Case | Result |
|---|---|
| Primary path of one worker blocked | Blamed in 10–31 s, then all requests succeed over Flannel |
| Fault removed | Fail back in about 10 s, no failed request |
| Same fault, `autoFailover: false` | About half the requests keep failing |
| Calico IPIP dropped on one worker | Failover in 11 s |
| Primary and backup of one worker blocked | Pod isolated; all requests succeed through the other worker |
| Operator stopped during failover | Traffic keeps working |
| `calico-node` pod deleted | Replaced in ~30 s, no failed request, no action |
| Both workers blocked | `Unknown`, no action (ambiguous) |
| Worker frozen (`docker pause`) | No faster than Kubernetes (~55 s) |

## Known limits

- Detection takes 10–31 s: 3 identical results plus up to the 10 s report heartbeat.
- Only Service traffic to pods with a gate and `net1` is protected. A client on the
  broken node itself has no backup.
- Gated pods are Ready only while the operator runs (it sets the gate). Start it before
  the workload, or the Service has no endpoints.
- A frozen node is no faster than plain Kubernetes: a pod's Ready is computed by the
  kubelet on its own node.
- Each pod uses two addresses, and each node runs two CNI agents.
- Not built: same IP on both interfaces (Layer 2 takeover), Multipath TCP.

## Try it

```bash
kubectl apply -f config/samples/cniresilience_sample.yaml
kubectl apply -f deploy/healthagent-daemonset.yaml
kubectl apply -f deploy/linkrouter-daemonset.yaml
go run ./cmd/main.go --controllers=cniresilience
kubectl get cniresilience default-resilience -o yaml
```

Full cluster setup (Calico move, Multus, Flannel, workload) is in
[`experiments/cni-resilience-experiment.md`](../../experiments/cni-resilience-experiment.md).
