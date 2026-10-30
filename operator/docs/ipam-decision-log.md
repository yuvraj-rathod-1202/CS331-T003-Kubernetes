# Problem 1 decision log

Decisions made while building the buddy IPAM, with the reason and what would
change them.

| # | Decision | Why | Revisit if |
|---|---|---|---|
| 1 | Controller is stateless: the buddy forest is rebuilt from `NodeBlock`s every pass | A restart cannot lose or double-allocate; name uniqueness of `NodeBlock` is the lock | Rebuild cost shows up at thousands of nodes (1000 nodes plan in tens of ms) |
| 2 | Controller writes spec, agent writes status | One writer per field; no lost updates | |
| 3 | `ptp` + `host-local`, ranges for all blocks in one inner array | Verified in Phase 0: different blocks on one node reach each other; several outer entries mean dual-stack | A CNI with its own IPAM is chosen |
| 4 | Routes first, then config, then Active | A pod must never get an address nobody can reach | |
| 5 | Pending blocks are promoted after 30 s even if a node has not acknowledged | A dead node must not block a grow; it resyncs routes on return | Acks should become mandatory for stricter safety |
| 6 | Drain is required for shrink | `host-local` is round-robin, so blocks do not empty (Phase 0, item 2) | |
| 7 | Quarantine before reuse (`quarantineSeconds`, default 60) | Stale routes or late packets must not reach a new owner | |
| 8 | Draining blocks keep routes until empty | Their pods are still alive | |
| 9 | Shrink thresholds 50% (shrink) vs 80% (grow), cooldown 300 s, never below the initial block | Hysteresis so blocks do not flap | Tuning on a real workload |
| 10 | A starvation event counts once per block change | Live test: a 3-minute flag grew a node to its cap | |
| 11 | No NodeBlock owner reference | Deleting an IPPool must not free blocks in use | |
| 12 | kube-proxy setting is an opt-in controller flag | It edits kube-system and restarts kube-proxy | |
| 13 | Simulator runs the real planner, in Go | One implementation to trust; the brief said Python, but a Python port would test a copy | A reviewer insists on Python |
| 14 | Tried a tighter-headroom mode under pool pressure; removed it twice | No effect on the 1000-node result, before or after the placement fixes (rows 16–18) | Evidence that it helps |
| 15 | Leak reclaim compares leases with the node's pod IPs, minimum age 2 min | Needs no CRI access; the age guard covers pods whose IP is not yet reported | A CRI-level check is available |
| 16 | First blocks are spread: split the smallest larger free block so the buddy stays free | Packed first blocks were each other's buddies, so no node could double in place | |
| 17 | Growth may take the node's own free buddy even if it overshoots the target by up to 2× | The free space after spreading is exactly those buddies; stealing a piece of another node's buddy spoils two nodes | Overshoot waste matters more than merge gains |
| 18 | The agent renders buddy blocks as one host-local range; capacity counts it that way | Overhead is paid once per range: /26 = 61 usable, two /27 = 58. Verified live: a pod got 10.244.2.255, which was a broadcast address before the merge | A CNI/IPAM that cannot take a bigger range |
| 19 | Agent reacts to watch events (nodes, NodeBlocks) and polls every 30 s as a fallback | Measured 0.6 s from a new NodeBlock to routes acknowledged on both other nodes, against up to 30 s | |
| 20 | Stale CNI-* masquerade rules are removed through `nsenter` into the host's iptables (agent pod needs hostPID) | The host's iptables backend (legacy or nft) is unknown; the host's own binary is always right. Verified live. | An agent image that ships a matching iptables |
| 21 | Not adopted: a gateway outside the subnet to recover one address per range | Gain is 1 of 61; needs ptp validation that was not done | Address pressure justifies the test |

## Assumptions that were wrong

- "`--cni=false` works with containerd" (Phase 0, item 7), and "500 pods fit on
  a minikube node" (item 8).
- The first version of the starvation signal (a boolean for 3 minutes), above.
- "A tighter-headroom mode will fix the near-full pool." It did nothing; the
  cause was block placement and per-block overhead (rows 16–18).
- "A 3-minute starvation flag is fine." It is not (row 10).
- The first simulator run charged buddy for pods arriving before any block
  existed, which made it look far worse than it is; fixed with a pre-roll and a
  two-minute ramp.
