# IPAM controller (Problem 1)

The controller hands out pod address space to nodes in buddy blocks and grows a
node when it runs low. Pod startup never waits on it: `host-local` on the node
allocates single addresses from the blocks the node already owns.

## Objects

- `IPPool` (cluster-scoped): ordered CIDR list and the sizing parameters. The
  controller writes only its status.
- `NodeBlock` (cluster-scoped): one allocated block. Its name is derived from
  the CIDR, so the API server's name uniqueness prevents the same block being
  given out twice. The controller writes `spec`; the node agent writes `status`.
  Phases: Pending → Active → Draining → Quarantined → Released. An empty phase
  counts as Pending.

## Reconcile

Each pass is level-triggered and stateless:

1. Rebuild the buddy forest from the pool CIDRs minus every `NodeBlock` that is
   not Released (`buildPool`). A restart therefore changes nothing.
2. For each node (sorted by name):
   - no block: allocate an initial block, the smallest power of two >=
     max(16, `initialBlockPercent`% of allocatable pods);
   - any block Pending, or a Draining block being restored: wait, one change in
     flight per node;
   - if free addresses < max(`triggerFloor`, (100 − `expandThresholdPercent`)%
     of capacity), or the node is *starved* (below), grow. A block that is still
     draining is restored first (`spec.drain` back to false); otherwise grow by
     `growthPercent`% of capacity rounded up to a power of two, halved until it
     fits under `capacityCapPercent`% of allocatable pods;
   - otherwise try to shrink (below).
3. Growth first tries the free buddy of a block the node owns, even if that is
   up to twice the growth target: the two halves aggregate into one route and
   the agent serves them as one `host-local` range, which pays the 3-address
   overhead once (a /26 holds 61 pods, two separate /27s 58). Otherwise it takes
   the smallest free block, splitting demand into several blocks if needed.
   A node's first block is *spread*: the controller splits the smallest larger
   free block, so the new block's buddy stays free for that node to grow into.
   Taking exact-size free blocks first would hand every node another node's
   buddy. CIDRs still fill in order.
4. Create the `NodeBlock`s as Pending. A name collision is not an error: the
   next pass rebuilds the tree and picks another block.
5. Update the `IPPool` status (`Ready`, `Exhausted`, `Degraded`, `Inactive`).

Usable addresses per host-local range are the range size minus 3 (network,
gateway and broadcast; see `ipam-phase0-assumptions.md`). Buddy blocks of one
node count as one range.

## Shrink, quarantine and release

`host-local` hands out addresses round-robin, so a block with pod churn never
empties by itself (Phase 0, item 2). The controller therefore drains:

1. A node may give up an Active block when it has at least two, the block has
   been Active for `shrinkCooldownSeconds`, what is left still covers the node's
   initial block, and the node's pods (including those still in draining
   blocks) fill at most `shrinkThresholdPercent` of that remainder with the grow
   trigger still clear. The emptiest, smallest block goes first, and several
   blocks can drain in one pass. `disableShrink` turns this off.
2. The controller sets `spec.drain`. The owner agent drops the block from the
   CNI config and sets Draining; existing pods keep their addresses and routes.
3. When the block holds no lease, the agent sets Quarantined. Other nodes drop
   the route.
4. After `quarantineSeconds` the controller deletes the `NodeBlock`, and the
   space is free again for any node (buddies merge on the next rebuild).
5. If the node needs the space back before step 3, the controller clears
   `spec.drain` and the agent restores the block.

Blocks of a node that no longer exists are set to Quarantined by the controller
itself (no agent is left to do it) and then released the same way.

## Emergency grow

Usage counters lag. When a kubelet reports `FailedCreatePodSandBox` with "no IP
addresses available", the controller marks that node *starved* for
three minutes and grows it regardless of the counters. The event only counts if
it is newer than the node's latest block change, so one failure causes one grow.
(A live test showed what happens otherwise: the node kept growing until it hit
its capacity cap.)

## kube-proxy

With `--ipam-configure-kube-proxy` the controller sets `clusterCIDR: ""`,
`detectLocalMode: InterfaceNamePrefix` and `detectLocal.interfaceNamePrefix:
veth` in the `kube-proxy` ConfigMap and restarts the DaemonSet, only when the
values differ. It is off by default because it edits kube-system. It needs get,
update and patch on that one ConfigMap and get and patch on that DaemonSet.

## Pool status

`status.cidrs` shows each pool CIDR as Active, Standby (nothing handed out yet)
or Full. CIDRs fill in order, so a later one is used only when the earlier ones
cannot satisfy a request. The pool reports `Degraded` when its free share falls
below `lowWaterPercent`.

## Running selected controllers

`--controllers=ipam` runs only this controller; the default `all` runs
`networkremediation`, `ipam` and `cniresilience`.

## Rules worth knowing

- Pool CIDRs that overlap `reservedCIDRs` or contain a node's InternalIP are
  rejected and the pool reports `Degraded`.
- With several IPPools the earliest created serves the nodes; the rest report
  `Inactive`.
- A pool CIDR smaller than `maxBlockSize` is clamped to its own size.
- Deleting an IPPool does not delete its NodeBlocks, because pods may still use
  those addresses.
- There is no owner reference from NodeBlock to IPPool, for the same reason.

## Known limits

- **Shrink is as slow as pod turnover.** A drained block empties only as its
  pods end; after a burst a node can hold its extra blocks for tens of minutes
  (see `ipam-evaluation.md`).
- **Many routes.** Buddy aggregation does not reach one route per node:
  expect 1–3 prefixes per node.
- **No compaction.** Free space can fragment; a node cannot be moved into one
  bigger block. Out of scope: BGP, dual-stack, Multus.
- **First come, first served.** Nodes that grow first keep their headroom, which
  matters only when the pool is nearly full. The sweep shows 100% served at 91.5%
  full, but with no fairness guarantee beyond that.
- **Dead nodes.** Blocks of a deleted node are released; those of a node that is
  only NotReady stay allocated.
- Simulator numbers are a model, not a cluster (see `ipam-evaluation.md`).
- The older `NodeIPAllocation` types and `monitor/` package from the first
  scaffold are not used by the controller.

## Try it

```bash
kubectl apply -f config/crd/bases/
kubectl apply -f config/samples/ipam_sample.yaml
go run ./cmd/main.go
kubectl get nodeblocks
kubectl apply -f deploy/ipam-agent.yaml   # the agent promotes blocks to Active
```
