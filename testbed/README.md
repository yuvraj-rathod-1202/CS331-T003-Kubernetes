# Edge testbed

Reproducible measurements of the CNI resilience operator and per-link steering on a
3-node cluster whose two pod networks run over **separate** underlay links. The paper's
results come from **K3s** (`DISTRO=k3s`); the same harness also runs on minikube.

```
              eth0: cluster network (K3s: 192.168.59.0/24)  -> Calico IPIP, primary
  C (server) ───────────────┬──────────────────────┐
     │                      │                      │
     A (agent-0) ───────────┴──────────── B (agent-1)
     │  eth1: second Docker network (K3s: 192.168.201.0/24) -> Flannel VXLAN, backup
```

- **A** runs the traffic sources: an HTTP prober (10 probes/s to the pod IP and to the
  Service) and ten MQTT sensors (five at QoS 1, five at QoS 0, 10 messages/s each).
- **B** runs the destinations: nginx and a Mosquitto broker, plus the MQTT subscriber in
  B's host network. **C** is healthy and is the relay.
- Every fault hits the A–B link or node B, so all measured traffic crosses it.
- Everything runs on one host, so all timestamps come from one clock.

## Distributions

| | `DISTRO=k3s` (paper) | `DISTRO=minikube` |
|---|---|---|
| Cluster | K3s v1.35.1 in Docker containers via k3d, cluster `edge-k3s` | minikube, Docker driver, profile `edge-lab` |
| Setup | `testbed/bootstrap-k3s.sh` | `testbed/bootstrap.sh` |
| State | `~/edge-k3s` (kubeconfig, kubectl, k3d) | `~/edge-lab` |
| Nodes C, A, B | `k3d-edge-k3s-server-0`, `-agent-0`, `-agent-1` | `edge-lab`, `-m02`, `-m03` |

On K3s, its bundled Flannel, network policy controller, Traefik, ServiceLB and
metrics-server are disabled; Calico v3.31.3, Multus v4.3.2 and Flannel v0.26.7 are
installed as on minikube. The K3s node image has no `tc` or `pgrep`, so every command
that touches a node's network or processes runs in a helper container
(`<node>-net`) that shares the node's network and PID namespaces.

## Requirements

Linux host with Docker (your user in the `docker` group), `tmux`, `git`, Python 3 with
matplotlib, about 8 GB free RAM, and the `sch_netem`, `ipip` and `vxlan` kernel modules
(Fedora: `kernel-modules-extra` for `sch_netem`). minikube only for `DISTRO=minikube`;
the K3s bootstrap downloads k3d and kubectl itself. No sudo, no Go: Go builds run in a
container.

## Use

```bash
export DISTRO=k3s                    # every script reads it
testbed/bootstrap-k3s.sh             # cluster, second network, Calico, Multus, Flannel, CRDs, images
testbed/operator.sh start            # operator on the host, in tmux
testbed/workload.sh up               # targets on B, clients on A
testbed/fault.sh link-cut primary    # inject; fault.sh clear removes everything
testbed/campaign-all.sh results      # the paper's whole evaluation (about 13 h)
testbed/ablations.sh results         # pinning and source-NAT experiments (about 1 h)
python3 testbed/analysis/analyze.py runs results/main -o summary.csv
python3 testbed/analysis/analyze.py aggregate summary.csv -o aggregate.csv
python3 testbed/analysis/plot.py summary.csv figs
```

`campaign-all.sh` runs, with fresh client pods before every run: the main campaign
(`main/`), the realistic-link runs (`profile-edge/`), the probe-interval sweep
(`sweep-d1/`, `sweep-d4/`), the overhead measurement (`overhead-d{1,2,4}.txt`) and the
strict reverse-path-filtering ablation (`rp-strict/`). `run.sh` runs single campaigns
(see its header for options).

From a development machine, `testbed/sync.sh user@host` copies the committed branch with
a git bundle and overlays the working copy of `testbed/`. Do not sync while a campaign
runs: the harness reads `fault.sh` and the manifests on every run.

## Configurations and faults

| Configuration | What runs |
|---|---|
| `vanilla` | Stock Kubernetes: no operator, health agent, router or readiness gates |
| `node` | Node-level failover only (`autoFailover`), no steering |
| `full` | Node-level failover, per-link steering and relay |
| `full-norelay` | As `full` with `linkRelay: false` (run with `link-cut-both` only) |

| Fault | How |
|---|---|
| `link-cut` | netem 100 % loss on eth0 between A and B (primary link down) |
| `link-cut-both` | the same on eth0 and eth1 (only the relay through C is left) |
| `link-loss30` | netem 30 % loss each way on eth0 (gray failure) |
| `link-flap` | eth0 down 5 s, up 5 s, repeating |
| `overlay-cut` | iptables drops Calico IPIP between A and B (CNI fault, link fine) |
| `node-cni` | B drops all forwarded Calico pod traffic (CNI fault on a node) |

Netem acts only on packets to the peer's address on that interface, inside the node
containers. Nothing on the host is changed.

`--link-profile edge` (or `LINK_PROFILE=edge testbed/fault.sh profile`) adds background
conditions on every node pair for the whole campaign: 5 ms +- 2 ms one-way delay on the
primary link, and 20 ms +- 10 ms, 1 % loss and 10 Mbit/s on the backup link. Faults are
applied on top; a fault's loss replaces the profile's loss.

## Metrics (`analyze.py`)

Per run, with F = the fault window and R = the window after clearing:

- `http_<pod|svc>_recovery_s`: time to the first run of ten successes in F (censored at
  F if none). `_avail`: share of successful probes in F. `_failback_failed`: failures in R.
- `mqtt_<q1|q0>_delivery`, latency percentiles, `_reconnects`, and `_max_gap_s` (the
  longest silence of any sensor).
- `steer_s` / `steer_final`: first and last path change of A's router during F.

## Setup findings that matter for the results

- **kube-proxy must know the real pod network.** With its default pod range, the
  cluster's 10.244.0.0/16 (the backup network's), Service connections from Calico pods
  (172.16.0.0/16) are masqueraded. Linux drops a masqueraded connection's NAT state when
  its output interface changes, so every switch (tunl0 <-> flannel.1) resets long-lived
  connections. Both bootstraps give kube-proxy Calico's pool (K3s:
  `--kube-proxy-arg=cluster-cidr`).
- **Loose reverse-path filtering on the tunnels.** Nodes switch independently, so A->B
  and B->A briefly use different tunnels, and strict `rp_filter` drops everything in
  that window. The link router's init container sets `rp_filter=2` on `tunl0` and
  `flannel.1`.
- **Multus v4.3.2's manifest** uses the `:snapshot` image with a 50Mi limit that gets
  OOM-killed; both bootstraps pin the release image and raise the limit.
- **Mosquitto** needs `set_tcp_nodelay true`, or Nagle adds about 40 ms to QoS 0.
- **minikube cannot manage a node with two networks.** `minikube start` and `status` fail
  while eth1 is attached. `bootstrap.sh` checks the containers instead, and detaches eth1
  before restarting a stopped cluster.
- **K3s with a custom CNI** uses the standard `/opt/cni/bin` and `/etc/cni/net.d`, but
  keeps its own plugins elsewhere; `bootstrap-k3s.sh` links them into `/opt/cni/bin`.
