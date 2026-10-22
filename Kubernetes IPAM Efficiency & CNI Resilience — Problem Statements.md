# Kubernetes IPAM Efficiency & CNI Resilience — Problem Statements

## Overview

Our project tackles two gaps in how Kubernetes handles pod networking: **wasteful, static IP address allocation** (an efficiency problem) and **the CNI plugin as a single point of failure** (a resilience problem). Both come from our discussion with the professor, who framed them as a pair: "one is efficiency, the other is resiliency."

|  | Problem 1 | Problem 2 |
| --- | --- | --- |
| Theme | Efficiency | Resilience |
| Networking layer | Layer 3: addressing, subnetting, routing | Layers 2-4: interfaces, redundancy, failover |
| Core question | How do we hand out pod IP addresses so none are stranded and no node starves? | How does a pod stay reachable, and how does the cluster recover, when its CNI fails? |
| Starting idea | A buddy-style allocator over multiple CIDR blocks | Two independent CNIs per pod via Multus, each monitoring the other |

For each problem, the professor asked us to cover: the current state of the art, its problems, and the approach we will take. He said a negative result is acceptable as long as it is analysed. Problem 2 in particular is exploratory.

**Out of scope:** the pod-to-pod connectivity controller and network-policy verification from our earlier work.

**Test environment:** a multi-node minikube cluster for both problems.

This is a working draft. We will keep revising it until the final problem statements and solutions are agreed.

## Background: the networking concepts behind both problems

Both problems rest on a handful of standard computer-networks ideas. Read this section first if any term in the problem statements is unfamiliar.

### IP addresses, CIDR and subnetting

An IPv4 address is 32 bits. CIDR notation `a.b.c.d/n` names a **block** of addresses: the first n bits are the fixed network prefix, and the remaining 32 − n bits number the hosts inside it. A block therefore always holds a power of two:

```latex
\text{addresses in a /n block} = 2^{32-n}
```

| Prefix | Addresses | Typical use in Kubernetes |
| --- | --- | --- |
| /16 | 65,536 | A whole cluster's pod range (e.g. `10.244.0.0/16`) |
| /24 | 256 | Default per-node pod range with the built-in node IPAM |
| /26 | 64 | Calico's default per-node block |
| /32 | 1 | A single pod's address |

A block must also be **aligned**: a /26 can start only at a multiple of 64 (`.0`, `.64`, `.128`, `.192`). Alignment is what lets routers treat the block as one route.

### Routing, longest-prefix match and aggregation

Routers forward a packet by finding the most specific (longest) prefix in their routing table that contains the destination address. If every node owns one aligned block, the whole cluster needs only one route per node: "`10.244.3.0/24` → node 3". This is **route aggregation** (supernetting). When addresses are scattered, routers need more specific routes, down to one /32 route per pod, and routing tables grow.

### Layer 2 vs Layer 3

- **Layer 2 (data link):** frames are delivered by MAC address inside one broadcast domain. Hosts find each other's MAC with ARP. Example CNIs: bridge, macvlan, Flannel's host-gw mode.
- **Layer 3 (network):** packets are routed by IP prefix between networks. Example: Calico, which advertises each node's blocks with BGP.
- **Overlay vs underlay:** an overlay (e.g. VXLAN) wraps pod packets inside node-to-node UDP packets, so the physical network (underlay) never sees pod addresses. Flannel and Calico can both run in VXLAN mode.

### The Kubernetes pod networking model

Kubernetes requires that every pod gets its own IP address, and that any pod can reach any other pod directly, without NAT. Kubernetes itself does not implement this. It delegates the job to a plugin.

### CNI and IPAM

- **CNI (Container Network Interface)** is the plugin contract. When a pod starts, the container runtime calls the CNI plugin's ADD command. The plugin creates a virtual Ethernet (veth) pair, puts one end in the pod as `eth0`, assigns an IP and sets up routes. When the pod is deleted, it calls DEL.
- **IPAM (IP Address Management)** is the part of the CNI that decides which address the pod gets. Examples: `host-local` (allocates from the node's `podCIDR`), Calico IPAM (allocates from IP pools in blocks), Whereabouts (cluster-wide ranges).
- Most CNIs also run a long-lived **agent** on every node (e.g. `calico-node`, `flanneld`, `cilium-agent`) that programs routes, tunnels and policy rules. The CNI binary runs only at pod create and delete; the agent runs all the time.

### Services and NAT

Pods are short-lived and their IPs change when they are recreated. A **Service** gives a group of pods one stable virtual IP (ClusterIP). `kube-proxy` uses destination NAT (DNAT) to rewrite traffic for that virtual IP to one of the current pod IPs, listed in the Service's EndpointSlices. As the professor pointed out, this indirection is what makes it possible to change a pod's IP at all.

## Problem 1: Static pod IP allocation wastes addresses and starves busy nodes

Kubernetes hands each node a fixed-size, power-of-two block of pod addresses up front. Real demand per node varies, so addresses get stranded on quiet nodes while busy nodes run out, and nothing moves the spare space to where it is needed.

### 1.1 Detailed problem statement

**Allocation is decided before demand is known.** With the built-in node IPAM, `kube-controller-manager` carves the cluster range (`--cluster-cidr`) into equal per-node slices (`--node-cidr-mask-size`, default /24 = 256 addresses). The size is set once, at cluster creation, and is identical for every node. Calico refines this with smaller /26 blocks of 64 addresses and gives a node more blocks as it fills up, but it still allocates in fixed-size blocks.

This produces four concrete problems.

1. **Internal fragmentation ("bubbles").** The professor's example: node A runs 100 pods and node B needs 500, but each was given 256 addresses. Node A wastes 156 addresses. Node B can start only 256 pods, and the other 244 cannot start. The cluster as a whole has enough addresses; they are just on the wrong node. Because blocks come in powers of two, a node that needs 65 addresses under Calico takes a second /26 and leaves 63 of them idle.
2. **No resizing after the fact.** A node's range cannot grow or shrink while it runs. GKE, for example, treats a node pool's pod range as immutable; using a new range requires creating a new node pool.
3. **Expansion cannot stay contiguous.** When the cluster range fills up, the obvious fix is to extend it. But the address space right after it is usually already used elsewhere in the organisation's network (another VPC, a data centre, a VPN). Growth therefore has to come from new, **discontiguous** CIDRs, and the IPAM and routing must handle several unrelated prefixes at once.
4. **Shrinking is blocked by addresses in use.** A block cannot be taken back while even one running pod holds an address inside it. To reclaim it, those few pods would have to move to new addresses. That is only safe if nothing depends on the old address, which comes down to how the pod is exposed (see 1.4).

In short: the allocation unit (a fixed, aligned block) is chosen for **routing convenience** (one aggregated route per node), while demand arrives **one pod at a time** and varies by node. Our problem is to keep the routing benefits of aligned blocks while matching allocation to real demand.

### 1.2 What Kubernetes does when this happens

Kubernetes has no feedback loop between IP availability and scheduling. Here is what each component does when a node or the cluster runs out of addresses.

| Situation | What Kubernetes does | Result |
| --- | --- | --- |
| A node's range is full and a new pod is scheduled there | The **scheduler** does not track IPs. It places pods by CPU, memory and the max-pods limit, so it keeps sending pods to the full node. The CNI's ADD fails (e.g. host-local reports no IP addresses available in its range). The kubelet records `FailedCreatePodSandBox` and retries indefinitely. | Pod stuck in `ContainerCreating`, even though other nodes have free addresses |
| The cluster range has no slice left for a new node | The node IPAM controller cannot assign a `podCIDR` and records a `CIDRNotAvailable` event. | The new node cannot run pods. Cluster autoscaling adds capacity that cannot be used. |
| A node holds many idle addresses | Nothing. Kubernetes never reclaims or rebalances a node's range. | Permanent waste until the node is deleted |
| A pod is deleted but its CNI DEL never completes (node crash, runtime restart) | Nothing in core Kubernetes. The IPAM record stays allocated ("leaked"). Some CNIs, such as Calico's kube-controllers, run their own garbage collection. | Addresses slowly disappear from the pool |
| Calico: a node's blocks are full | Calico claims another free block. If none is left and strict affinity is off, it **borrows** single addresses from another node's block. | Works, but each borrowed address needs its own specific route, which breaks aggregation and grows routing tables |

Upstream Kubernetes tried to address part of this. KEP-2593 proposed multiple, discontiguous cluster CIDRs with different per-node sizes. It was **withdrawn** in v1.29, and the feature moved out of the core project. GKE offers "discontiguous multi-Pod CIDR", but only for **new node pools**; existing nodes keep their range.

### 1.3 How this happens in real-world clusters

- **Mixed node sizes.** A cluster with small 4-core nodes and large 64-core nodes gives both the same /24. The small nodes can never use it; the large nodes run out first.
- **Bursty workloads.** CI pipelines, batch jobs and serverless platforms create hundreds of short-lived pods on a few nodes, then go quiet. Peaks hit the per-node limit while the average is low.
- **Autoscaling consumes address space by node count, not pod count.** A /16 cluster range split into /24 slices allows at most 256 nodes, however few pods each one runs. GKE's own example: with max 64 pods per node, each node gets a /25 (128 addresses), so a /24 pod range supports only 2 nodes.
- **Enterprise address planning.** Pod ranges must not overlap corporate networks, so teams start with a small range. Later, the adjacent range is already taken, and expansion must be discontiguous.
- **Cloud-native IPAM that uses real subnet addresses.** Some cloud CNIs, such as AWS's VPC CNI, give pods addresses from the VPC subnet and keep a "warm pool" of pre-allocated addresses on each node. That pool is the same hoarding problem, and subnet exhaustion is a common operational issue.
- **Leaks after failures.** Node crashes and container-runtime restarts leave allocations behind that nobody frees.

### 1.4 Proposed solution: a dynamic, buddy-style IPAM controller

We propose a cluster-wide controller that allocates pod address space the way an operating system's **buddy memory allocator** allocates memory. The professor suggested exactly this analogy: never cut below a power-of-two boundary, but within those boundaries always find the best fit.

**How a buddy allocator maps onto IP blocks**

- Treat each pod CIDR in the pool as a tree of blocks. A /22 splits into two /23 "buddies", each /23 into two /24s, and so on down to a minimum block of /28 (16 addresses).
- To serve a request, take the smallest free block that fits, splitting a larger block only when needed. Example: a node needing 100 addresses gets a /25 (128), not a /24 (256).
- When a block becomes free and its buddy is also free, merge them back into the larger block. This reduces fragmentation over time.
- Every block stays aligned to its size, so each one is still a single routable prefix. That keeps route aggregation intact and avoids Calico-style /32 borrowing.

&#91;embedded content: fixed /24 slices vs buddy allocation · professor's 100 vs 500 pod example\]

With fixed /24 slices, 156 addresses sit idle on node A while 244 of node B's pods cannot start. Buddy allocation gives A a /25 and B a /23, and every pod gets an address.

**Controller components**

1. **Monitor.** Track per-node address usage, pending pods that failed with IP errors, and free space in each pool.
2. **Multi-CIDR pool.** Manage a list of discontiguous prefixes (e.g. `10.244.0.0/16` plus `172.20.0.0/18`), each with its own buddy tree, so the cluster can grow without needing adjacent space.
3. **Expand before exhaustion.** When a node passes a usage threshold (e.g. 80%, to be tuned), give it an additional block sized to the remaining demand. When the whole pool passes a threshold, add a new CIDR from an operator-approved list.
4. **Reclaim leaks.** Regularly compare IPAM records with the pods that actually exist, and free addresses that belong to no pod.
5. **Shrink and defragment safely.** In quiet periods, return empty blocks and merge buddies. For partly used blocks, move pods off only if it is safe:
   - **Safe to move:** pods reached only through a Service. The Service's virtual IP and NAT hide the pod address, so recreating the pod elsewhere (with a rolling restart) changes nothing for clients.
   - **Pinned:** pods whose address is used directly, e.g. allow-listed in an external firewall or reached without a Service. As the professor put it: if the IP cannot change, the block cannot shrink.
6. **Routing updates.** Advertise new blocks and withdraw returned ones (e.g. via Calico's BGP, or by updating node routes) so the network stays consistent.

**Implementation.** We build our own controller rather than extending Calico. It stores which blocks belong to which node in its own custom resource. On each node, the standard host-local IPAM plugin hands out pod addresses from the blocks currently assigned to that node (host-local accepts several address ranges). We do not rewrite the node's `spec.podCIDRs`, because Kubernetes lets that field be set only once. Since host-local does not program routes between nodes, the controller also installs one route per block ("block → node").

**How we will evaluate it** (against Kubernetes' built-in node IPAM with the host-local plugin, same workloads)

| Metric | What it shows |
| --- | --- |
| Address utilisation (used ÷ allocated) | How much waste we remove |
| Pods failed or delayed due to IP errors | Whether busy nodes still starve |
| Number of routes per node | Whether aggregation is preserved |
| Time to allocate and to expand | Controller overhead |
| Pods disrupted by shrinking | Safety of defragmentation |

Scenarios: mixed node sizes, a burst of short-lived pods, a cluster that must grow into a discontiguous range, and a quiet period after a burst.

## Problem 2: A pod's single CNI is a single point of failure

Every pod normally has exactly one network interface, built and maintained by one CNI plugin. When that CNI fails, the pod loses all reachability, Kubernetes has no second path to detect or fix the problem, and in practice recovery often means restarting components or rebooting nodes.

### 2.1 Detailed problem statement

**One interface, one path.** In networking terms, a pod is a single-homed host: one interface (`eth0`), one IP, one path to the rest of the cluster. Classic network design avoids single points of failure with redundancy: two uplinks, two routers, two power supplies. Kubernetes pod networking has no equivalent.

**The CNI has several parts, and each can fail.**

| CNI component | Its job | If it fails |
| --- | --- | --- |
| CNI binary | Runs at pod create/delete: veth pair, IP, routes | New pods cannot start networking |
| Node agent (e.g. `calico-node`, `flanneld`) | Keeps routes, tunnels, BGP sessions and policy rules up to date | Existing pods gradually or suddenly lose reachability |
| IPAM datastore | Records which addresses are allocated | New pods cannot get an IP |
| Data path (routes, VXLAN tunnel, iptables/eBPF rules) | Actually forwards packets | Packets are dropped, often silently |

**Reachability is how we know anything is alive.** The professor's key point: we detect failure by trying to reach something. If the only network path is down, then:

- other pods and Services cannot reach the pod, so it looks dead even if the application is healthy;
- the pod cannot reach the Kubernetes API server (normally reached through a Service over the pod network), so an operator or self-healing logic inside it cannot act;
- the failure cannot be repaired over the network, because the network is the thing that failed.

Without redundant reachability there is no way to tell "application failed" from "network failed", and no path to recover.

**Multus already gives pods multiple interfaces, but not for resilience.** Multus is a "meta" CNI that calls several CNIs for one pod, so the pod gets `eth0` from the primary CNI plus extra interfaces (`net1`, `net2`, ...) from others. The professor previously used it for performance: one interface on DPDK (kernel bypass, for fast packet processing) and one on the normal kernel stack. As he put it, Multus was not meant for resiliency. Our problem is to use the same multi-interface idea to build **redundancy, failure detection and recovery**.

### 2.2 What Kubernetes does when the CNI fails

Kubernetes' built-in resilience works at the level of **replicas and nodes**, not network paths. It assumes that if a pod is unreachable, it is better to run a copy elsewhere.

| Failure | What Kubernetes does | Why it is not enough |
| --- | --- | --- |
| CNI agent process crashes | The DaemonSet restarts the agent pod | Helps only for clean crashes. If the agent hangs or the data path is corrupted while the process looks healthy, nothing happens unless the agent's own liveness probe catches it. |
| CNI not configured or not ready on a node | The container runtime reports `NetworkReady=false`, and the node becomes `NotReady` | Only covers a missing/uninitialised CNI, not a running CNI with a broken data path |
| Data path broken but node still `Ready` | Nothing at the node level. The kubelet talks to the API server over the host network, so the node still looks healthy. Pods are **not** rescheduled. | Pods on that node are unreachable while Kubernetes believes everything is fine |
| Pod readiness probes fail | The pod is removed from its Services' endpoints, so traffic goes to replicas on other nodes | Works only if healthy replicas exist elsewhere. Single-replica and stateful workloads are simply down. |
| Pod liveness probes fail | The kubelet kills and restarts the containers | The network is still broken, so restarts do not help, and any brand-new pods need a CNI ADD, which may also fail (stuck in `ContainerCreating`) |
| Node marked `NotReady` | After a grace period (default toleration 300 s), pods are evicted and recreated elsewhere | Minutes of downtime; does not fix the node |

There is no mechanism to give a pod a second path, switch traffic to it, or repair a CNI through an independent channel. Operators usually restart the CNI pods by hand or reboot the node.

### 2.3 How this happens in real-world clusters

- **CNI agent crash or out-of-memory kill**, e.g. the agent's memory grows with the number of routes or policies on a large cluster.
- **CNI upgrades and configuration pushes.** Upgrading the CNI DaemonSet restarts agents on every node; a bad config is applied cluster-wide at once.
- **Routing protocol failures.** Calico's BGP sessions to peers or top-of-rack switches can drop or flap, withdrawing pod routes.
- **Overlay problems.** A VXLAN MTU mismatch silently drops large packets while small ones (like pings) still pass, so simple health checks report "healthy".
- **Rule or map corruption.** Stale iptables rules or eBPF maps blackhole traffic for some pods.
- **Datastore outage.** If the IPAM backend (etcd or the Kubernetes API) is unreachable, no new pod can get an address.
- **Physical failure.** A NIC, cable or switch port fails on a node with one uplink.
- **Telecom (5G) workloads** already run Multus with several interfaces per pod to separate control-plane and data-plane traffic, so multi-interface pods are a realistic deployment model, not just a lab idea.

### 2.4 Proposed solution: redundant CNIs with mutual monitoring and recovery

Give every pod two independent network paths with Multus, continuously check both, and use the healthy path to fail over and to repair the failed one.

**Design rule: the two paths must fail independently.** They should use different CNI implementations, different agents, different IPAM and, ideally, different physical NICs. Two copies of the same CNI would fail together.

**Example setup**

|  | Primary interface (`eth0`) | Backup interface (`net1`) |
| --- | --- | --- |
| CNI | Calico (Layer 3, BGP routing) | Flannel (VXLAN overlay: Layer 2 frames carried in UDP) |
| Address space | `10.244.0.0/16` | `192.168.0.0/16` |
| Agent needed | Yes (`calico-node`) | Yes (flanneld), separate from Calico's |

Flannel is a simple overlay CNI. Its `flanneld` agent gives each node a subnet and carries pod traffic between nodes in VXLAN tunnels (Layer 2 frames inside UDP packets). It shares nothing with Calico: different agent, IPAM and forwarding method. That is what lets the two paths fail separately. Two things to configure: Flannel needs its own network (`192.168.0.0/16`) so its subnets never overlap Calico's pool, and `flanneld` is a second daemon that can fail too, so the health agent watches both.

The professor noted that the two interfaces could even share **the same IP**, because one is reached by Layer 2 switching and the other by Layer 3 routing, so they do not interfere. Separate address spaces (10.x and 192.168.x) are simpler and fully isolated, so we start there.

&#91;embedded content: redundant CNI design · two interfaces, one health agent\]

Each pod gets two interfaces from independent CNIs. When the primary path fails, the health agent still reaches the API server over the backup path and restarts CNI A.

**Step 1: Detect failures (health agent).** A DaemonSet agent on each node checks both paths continuously:

- probes the health agents on other nodes (never application pods) over each network separately (ICMP and TCP, including large packets to catch MTU problems);
- checks the CNI agent's health endpoint and its BGP sessions;
- verifies that the expected routes and interfaces exist.

This is the same idea as **BFD** (Bidirectional Forwarding Detection, RFC 5880), which routers use to detect a dead link in milliseconds instead of waiting for routing-protocol timeouts.

**A probe blocked by NetworkPolicy is not a failure.** At the packet level the two look identical: a policy drop and a broken path both silently discard the packet. We tell them apart by design:

1. **Probe only infrastructure, never applications.** Health agents probe each other on a dedicated port, and we install a NetworkPolicy that explicitly allows that traffic. Application policies therefore never touch our probes.
2. **Local checks are always allowed.** Kubernetes always permits traffic between a pod and the node it runs on, so on-node checks cannot be blocked by policy.
3. **Fail a path only when the agent-to-agent probes fail.** If those pass but one application flow is dropped, the path is healthy and the drop is policy, so no failover happens.
4. **The backup path is outside standard NetworkPolicy.** Policy is enforced by the primary CNI (Calico); standard Kubernetes NetworkPolicy does not apply to Multus secondary interfaces such as the Flannel one. If we later add MultiNetworkPolicy, rule 1 applies there too.

**Step 2: Recover (main contribution).** When the primary path fails, the agent uses the backup path to reach the API server and repairs the primary CNI: restart its agent pod on that node, re-apply routes, and taint the node so no new pods land there until it is fixed. This directly addresses the professor's point that today the only fix is a reboot.

**Step 3: Fail over traffic.** Several designs are possible, from simplest to hardest:

| Option | How it works | Networking idea | Trade-off |
| --- | --- | --- | --- |
| A. Active/standby with route switching | On failure, change the pod's default route from `eth0` to `net1`. A controller reads Multus's `network-status` annotation (it lists every IP of a pod) and keeps "shadow" EndpointSlices with backup IPs, switching Services to them on failure. | Like **VRRP** (RFC 5798): a standby takes over when the active path dies | Clients see a different IP; needs our own controller because Kubernetes only knows the primary IP |
| B. Same IP on both interfaces | Primary on Layer 3 (Calico route), backup on Flannel (Layer 2 overlay). On failure, withdraw the L3 route and announce the IP on L2 with a gratuitous ARP. | VRRP-style virtual IP takeover | No IP change for clients; harder to make correct |
| C. Active/active multipath | Use both interfaces at once with **Multipath TCP** (MPTCP, RFC 8684, built into Linux since kernel 5.6). Each connection has a subflow on each path, so losing one path does not drop the connection. | Multipath transport | No failover step at all; only TCP apps that enable MPTCP benefit |

**Things the design must handle**

- Kubernetes only knows the primary IP. Services, EndpointSlices, kube-proxy and probes all use it. Anything on the backup path needs our controller.
- Liveness probes run over the primary path. If it dies, the kubelet may restart healthy pods. We must suppress or reroute probes during failover.
- Two interfaces per pod use twice the addresses, which connects back to Problem 1.

**How we will evaluate it.** Inject faults (kill `calico-node`, delete routes, drop traffic with iptables, bring down the VXLAN interface, cause an MTU mismatch) and measure:

| Metric | Baseline (single CNI) | Our design |
| --- | --- | --- |
| Time to detect the failure | To be measured | To be measured |
| Time to recover (MTTR) | Manual / reboot | To be measured |
| Packets lost and connections dropped during failover | To be measured | To be measured |
| Overhead (CPU, memory, addresses) | To be measured | To be measured |

## Decisions

| Question | Decision |
| --- | --- |
| Problem 1 baseline | Kubernetes' built-in node IPAM (host-local plugin) |
| Problem 1 implementation | Our own controller, not an extension of Calico |
| Problem 1 minimum block size | /28 (16 addresses) |
| Problem 2 CNI pair | Calico primary, Flannel backup |
| Problem 2 failover | Option A as the main goal; Options B and C as exploration |
| Test environment | Multi-node minikube cluster |

## Sources

- [KEP-2593: Enhanced NodeIPAM to support discontiguous cluster CIDR](https://www.kubernetes.dev/resources/keps/2593/) (withdrawn, v1.29)
- [GKE: Discontiguous multi-Pod CIDR](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/multi-pod-cidr)
- [Calico block affinity behaviour (block size, borrowing, strictAffinity)](https://oneuptime.com/blog/post/2026-03-13-optimize-blockaffinity-behavior-calico/markdown)
- Discussion with the professor (recording and transcript, October 2026)
