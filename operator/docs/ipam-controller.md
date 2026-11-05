# IPAM controller (Project 2)

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
  counts as Pending. A node's first block is marked `spec.initial` (its
  starting block). A block that was split keeps its original name, so its name
  can show a larger prefix than its `spec.cidr`.

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
     **what the node's pods are short of**, nothing more (below), cut back to
     fit under `capacityCapPercent`% of allocatable pods;
   - otherwise try to shrink (below).
3. **Growth is sized to the demand, not to the node.** Demand is the node's
   leases plus the pods scheduled to it that are still waiting for an address
   (or the node's pod count, when the lease count lags). The capacity that
   leaves the grow trigger quiet for that demand is the first capacity at which
   the trigger above is false (searched, so it agrees with it to the last
   address). The node is given the difference, in whole minimum blocks. After
   each piece the node's real capacity is recomputed with merged ranges: a
   piece that merges with a neighbour loses none of the 3 reserved addresses,
   so it is often enough on its own. 49 pods on one /27 need 61: the
   neighbouring /27 (two /27s make a /26 with 61 usable) is the whole grow, with
   no /28 on top. 52 pods on 61 addresses need 64: one /28, not a /27. The old
   rule grew by a percentage of the node's current size whatever it needed;
   `growthPercent` is ignored now.
   The size is taken first from the free buddy of the largest range the node
   owns completely around one of its blocks (two /28s that make a /27 grow
   towards the /27's buddy). A buddy
   no larger than the piece is taken whole: the two halves aggregate into
   one route and the agent serves them as one `host-local` range, which pays
   the 3-address overhead once (a /26 holds 61 pods, two separate /27s 58). A
   larger buddy gives only the piece, from the part next to the
   node's block (`grow-beside`); the rest stays free beside the node for its
   next grow, after which everything merges again. The node holds what it
   needs, at the cost of a second route until it does. Whatever is still
   missing comes from the smallest free blocks, split so that as few blocks as
   possible are used.
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
   The node's pods are counted from the pod list when that is higher than the
   agent's lease count, which lags: a block full of running pods is not drained
   on an old number.
   The node's **starting block is never drained or split**; only blocks it got
   later are given back. On blocks created before `spec.initial` existed, the
   node's oldest block counts as its starting block.
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

The controller wakes up when the next quarantine or cooldown runs out (plus
0.5 s), not only at its 30 s idle requeue: nothing in the cluster changes when a
timer ends, so no watch event would wake it.

### Giving back half a block (split)

When no whole block can go, a grown block with one empty half can give that
half back. `host-local` hands out addresses from the start of a range, so after
a burst the upper half of the newest block is often empty.

1. The candidate is a grown (not starting) Active block larger than
   `minBlockSize`, past the cooldown, with one half holding no lease
   (`status.halfUsage`, reported by the agent). The limits of a whole drain
   apply to what the node keeps. The largest such half goes first; the upper
   one when both are empty. One release at a time per node.
2. The controller sets `spec.release` to that half. The owner agent serves only
   the other half from then on; the other nodes keep their route for the whole
   block.
3. When the half has been out of the CNI config for the drain settle time and
   holds no lease, the agent sets `status.releaseReady`.
4. The controller completes the split: one update shrinks `spec.cidr` to the
   kept half, so the half is never owned twice; then it creates the released
   half as its own NodeBlock (`spec.drain`, Quarantined), which is released
   after `quarantineSeconds` like any drained block. Other nodes install the
   route for the kept half before they remove the one for the whole block.
5. If the node needs the space first, the controller clears `spec.release`.

A crash between the two writes of step 4 leaves the half free in the pool
without its quarantine; it held no lease and was out of the config, so this is
safe. If the second write (Quarantined) is lost, the agent quarantines the half
itself. Events: `BlockHalfReleasing`, `BlockHalfRestored`, `BlockSplit`.

## Emergency grow

Usage counters lag. When a kubelet reports `FailedCreatePodSandBox` with "no IP
addresses available", the controller marks that node *starved* for
three minutes and grows it regardless of the counters. The event only counts if
it is newer than the node's latest block change, so one failure causes one grow.
(A live test showed what happens otherwise: the node kept growing until it hit
its capacity cap.) It must also be at least 5 s newer (`starveGrace`): during a
burst, sandboxes that read the CNI config before a new block went Active keep
failing for a moment after it, and succeed on retry. Without the grace, 21 such
failures one second after a grow made the controller add a /26 a node with 70
pods did not need. Changes that leave no NodeBlock behind (a release) are
remembered per node in the pool status (`nodeAllocations[].lastBlockChange`);
without that, a node with no pods grew, drained and released the same block
eight times, because the burst's failures looked new again after each release.

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

`--controllers=ipam` runs only this controller. The default is
`ipam,cniresilience` (both Project 2 controllers); `all` also adds Project 1's
`networkremediation`.

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
  bigger block. A split only returns a half that is completely empty; an
  address in use cannot be moved, so after much churn a half rarely empties. Out of scope: BGP, dual-stack, Multus.
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
