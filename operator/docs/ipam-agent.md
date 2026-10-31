# IPAM node agent (Problem 1)

`cmd/ipam-agent` runs on every node as a privileged `hostNetwork` DaemonSet
(`deploy/ipam-agent.yaml`). It is the data-plane half of the controller: the
controller decides which blocks a node owns, the agent makes that true on the
node. It writes only `NodeBlock` status.

## One pass (on every watch event, and at least every 30 s)

1. **Routes.** For every block owned by another node, install
   `block via <owner InternalIP>` (netlink, protocol 99, so the agent only ever
   touches its own routes). Blocks of one owner that are buddies aggregate into
   one prefix. Stale routes are removed. Blocks in Quarantined or Released
   phases get no route.
2. **Acknowledge.** Only after step 1 succeeded, add this node to
   `status.routesAckedBy` of every block it now routes. A failed route is never
   acknowledged.
3. **CNI config.** Write `/etc/cni/net.d/10-buddy.conflist` (ptp + host-local,
   `ipMasq: false`) with all of the node's Active blocks in one inner `ranges`
   array. The write is atomic (temp file + rename) and skipped when nothing
   changed. Pending and Draining blocks are not in the config.
4. **Promote.** A Pending block becomes Active when every other Ready node has
   acknowledged its route, and only after the config containing it is on disk.
   After `--promote-timeout` (30 s) it is promoted anyway, so a dead node cannot
   block a grow; that node resyncs its routes on return.
5. **Usage.** Count host-local lease files per block (ground truth, including
   leaked leases) and report `status.usedAddresses` when it changed.

Order is routes, then config, then Active. Otherwise a pod could be given an
address that nobody can reach.

## Watches, merged ranges, stale NAT

- **Watches.** The agent reads nodes and NodeBlocks from a watch-fed cache and
  runs a pass as soon as one changes (events within 300 ms are merged); the
  `--interval` pass (default 30 s) is the fallback if a watch breaks. Measured:
  0.6 s from a new NodeBlock to its route acknowledged on both other nodes.
  Status writes go straight to the API server, so a stale cache cannot cause
  a lost update.
- **Merged ranges.** Blocks that are buddies are written as one `host-local`
  range (two /27s become a /26), so the reserved 3 addresses are paid once.
  Leases are per-address files, so addresses already handed out stay valid when
  blocks merge or split. Verified live: after a node doubled its /24, the config
  showed one /23, a single route covered it, and a pod was given 10.244.2.255
  (the old /24's broadcast address) and was reachable from the other nodes.
- **Stale NAT.** An earlier CNI's `-s <pod IP> -j CNI-*` MASQUERADE rules would
  NAT any pod that later gets that address. At most once a minute the agent
  removes such rules for addresses in its own blocks, and their chains, using the
  host's `iptables` through `nsenter` (so it matches the host's backend). The pod
  needs `hostPID: true`; the image is busybox for `nsenter`. `--clean-stale-nat=false`
  turns it off. Verified live: an injected rule and chain were gone within a
  minute.

## Drain and leak reclaim

- A block with `spec.drain` leaves the CNI config and becomes Draining. Pods in
  it keep their addresses, and routes to it stay. Once it holds no lease (and
  `--drain-settle`, default 10 s, has passed, to cover a sandbox that read the
  old config a moment ago) the agent reports Quarantined; other nodes then drop
  the route. If `spec.drain` is cleared first, the block returns to the config
  and Active.
- Leak reclaim (`--reclaim-leaks`, default on): a lease file in an owned block
  whose address no pod on this node holds, and that is older than
  `--leak-min-age` (default 2 min), is deleted, which is what `host-local` DEL
  does. An empty pod list is not trusted, since every node runs at least
  kube-proxy. The count goes to `status.reclaimedLeases`. Needs `get` and
  `list` on pods.

## Recovery

Nothing is remembered between passes, so recovery is just running again:

| Event | Result |
|---|---|
| Agent restart | Next pass re-applies everything; config and leases persist on disk. |
| Node reboot | Kernel routes are lost; restored within one interval. |
| Stale route | Removed on the next pass. |
| Controller down | Routes, config and usage reporting continue; no new blocks. |

## Verified on minikube (3 nodes, `buddy-ipam` profile)

- Initial blocks went Pending → Active on their own, each acknowledged by the
  other two nodes.
- All six cross-node pod pairs answered ping.
- 211 lease files on one node made the controller add a block; the agents
  routed, acknowledged and promoted it, and a pod placed in it was reachable
  from the other nodes in both directions.
- `ip route flush proto 99` on a node caused 100% loss; one interval later all
  routes were back and traffic recovered.

Also verified live: a block drained, quarantined and released on its own;
leaked leases older than two minutes were removed while the lease of a running
pod was kept; with the agent paused on one node and its block full, a pod failing
with "no IP addresses available" made the controller add a block, and after
the agent returned the extra blocks drained again.

## Prerequisites and limits

- `kube-controller-manager --allocate-node-cidrs=false`, and kube-proxy with
  `detectLocalMode: InterfaceNamePrefix` (prefix `veth`); see
  `ipam-phase0-assumptions.md`, item 6.
- The runtime must pick the agent's conflist: `10-buddy` sorts before other
  files in `/etc/cni/net.d`. Other conflists are not removed (the node image's
  podman bridge conflist stays but sorts after ours), and stale NAT rules are
  removed only for addresses inside our blocks.
- A pod sandbox that read the CNI config just before a drain can still take an
  address from the drained block; the settle time makes that unlikely, not
  impossible. The block then simply stays Draining until that pod ends.
