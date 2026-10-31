# Problem 1 — Phase 0: assumption table

Each assumption in the buddy-IPAM design that we were not sure of, tested on a
3-node minikube cluster (profile `buddy-ipam`, docker driver, docker runtime,
Kubernetes v1.37.0, kube-proxy in iptables mode). Scripts are in
`hack/ipam-phase0/`.

| # | Assumption | Result | Evidence |
|---|---|---|---|
| 1 | With `ptp` + `host-local`, pods in different blocks on the same node can reach each other | **Confirmed** | Node 0 (10.244.0.2 in /26 ↔ 10.244.0.66 in /27) and node 1 (10.244.1.2 ↔ 10.200.0.2 in a discontiguous pool) all answered ping in both directions. `ptp` installs a /32 host route per pod, so there is no shared L2 domain. |
| 2 | `host-local` allocates round-robin across ranges | **Confirmed** | 25 ADDs filled range 1 (.2–.14) then range 2 (.66–.78). After freeing .2–.4, the next ADD returned .78 (after the last reserved IP), and only later wrapped to .2/.3. It is "next after last reserved", not lowest-free. A block on a node with churn therefore never empties on its own, so draining is required. |
| 3 | DEL succeeds and releases the lease after the range was removed from the config | **Confirmed** | With only range 1 in the config, DEL of two containers whose leases were in range 2 returned rc=0 and removed their lease files. |
| 4 | The runtime picks up a changed CNI config without a restart | **Confirmed** (cri-dockerd) | The conflist was replaced while the node was running. The next pods (including recreated CoreDNS) got `ptp` addresses with no restart of the runtime or kubelet. Not tested on containerd/CRI-O. |
| 5 | Per-block overhead is network + broadcast + gateway (3 addresses) | **Confirmed**; partly recoverable | /28 gives 13 usable (.2–.14), so 18.75% overhead. `rangeStart=.1` plus explicit gateway .1 changes nothing. `rangeStart=.0` hands out the network address, which is unsafe. A gateway outside the subnet (169.254.1.1) is accepted by `host-local` and starts allocation at .1, recovering 1 of 3. **Not yet tested with `ptp`**, which needs a usable default route. |
| 6 | kube-proxy works across two discontiguous pools with `InterfaceNamePrefix` or an empty cluster CIDR | **Refuted as-is; fix confirmed** | With `clusterCIDR=10.244.0.0/16`, kube-proxy installs `! -s 10.244.0.0/16 -d <ClusterIP> -j KUBE-MARK-MASQ`. A pod in 10.200.0.0/27 reaching a Service endpoint on another node arrived with its source rewritten to the node IP (192.168.58.3). Pods in 10.244/16 kept their IP. Setting `clusterCIDR: ""`, `detectLocalMode: InterfaceNamePrefix`, `detectLocal.interfaceNamePrefix: veth` made new flows from the 10.200 pod keep `src=10.200.0.2`. Service access and DNS from 10.200 pods worked in both cases. |
| 7 | The minikube flags work: `--cni=false`, `allocate-node-cidrs=false`, kubelet `max-pods` | **Partly refuted** | `--cni=false` is rejected with the default containerd runtime (`containerd requires CNI`); it works with `--container-runtime=docker`. `allocate-node-cidrs=false` took effect (`spec.podCIDR` empty). `max-pods=600` took effect (allocatable pods 600). The node image still ships `/etc/cni/net.d/87-podman-bridge.conflist` (bridge + host-local on 10.244.0.0/16) and the runtime uses it even with `--cni=false`. |
| 8 | The machine can run 500+ pause pods on one minikube node | **Refuted for this setup** | 60 pause pods raised host memory by about 653 MB (about 11 MB per pod: sandbox + container + shim). That gives about 5.5 GB for 500 pods. Minikube caps each node at 1.86 GB, so the cap is reached at about 150 pods. Host has about 5 GB free in total. The 500-pod scenario must run in the simulator, or on a larger machine with more node memory and a lighter workload. |
| 9 | Static routes between minikube nodes work for pod addresses in blocks outside the default range | **Confirmed** | `ip route add <block> via <node IP>` on every node. Pings between all node pairs, including to and from 10.200.0.2 (outside 10.244.0.0/16), succeeded (12 of 12). Direct pod-to-pod connections kept their real source IP (no NAT) once the stale rule below was removed. |

## Additional findings

- **Stale NAT after switching CNI config.** Replacing the podman bridge config
  left `-s <podIP>/32 -j CNI-… → MASQUERADE` in the node's nat table. A new pod
  that reused that IP was masqueraded to the node IP. The agent must clean
  chains from any previous CNI config, and the experiment setup must flush
  them (`hack/ipam-phase0/clean.sh`).
- **No ipMasq in `ptp`** is required to keep pod-to-pod traffic un-NATed, as the
  design says. This was confirmed with conntrack on the destination node.
- **Cluster setup that works** for later phases:
  `minikube start -p buddy-ipam --driver=docker --container-runtime=docker --nodes=3 --cni=false --memory=1900 --extra-config=controller-manager.allocate-node-cidrs=false --extra-config=kubelet.max-pods=600`,
  then remove the podman conflist and its bridge on every node. The host needs
  `fs.inotify.max_user_instances` of about 1024; at 128 the node containers
  fail to start when other clusters are running.

## Consequences for the design

1. Keep draining as a first-class step (assumption 2).
2. The node agent must also own the kube-proxy local-traffic setting, or the
   operator must document it as a prerequisite (assumption 6).
3. Moving the 500-pod and large-pool scenarios to the simulator is not optional
   on this hardware (assumption 8).
4. Keep /28 as the minimum block, but prefer /27 or larger for initial and
   growth blocks. 3 of 16 addresses lost is 18.75%.
