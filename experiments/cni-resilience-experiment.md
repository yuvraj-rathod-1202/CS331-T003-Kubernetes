# Problem 2: Dual-CNI Resilience, Failover and Recovery

How to set up and run the experiment, and what you see when you do. Every result below
was measured on the lab cluster on 2026-11-02 (3 nodes, K8s v1.37, Calico v3.32.2,
Flannel v0.26.7, Multus). Numbers are from those runs, not promises.

## 1. The idea in one paragraph

Every app pod gets two network cards. `eth0` is on Calico (the main network, `172.16.0.0/16`).
`net1` is on Flannel (the backup network, `10.244.0.0/16`), added by Multus. A small
health agent on every node sends real requests to the agents on other nodes over both
networks, and writes what it saw on its Node. The operator reads those reports. When a
node's Calico path is proven broken but its Flannel path works, the operator moves that
node's Service traffic to the pods' `net1` addresses. It does this with a readiness gate
(so Kubernetes stops using the `eth0` address) plus a "shadow" EndpointSlice listing the
`net1` address. When Calico works again, it moves traffic back.

## 2. Requirements

Docker, minikube, kubectl, Go (version in `operator/go.mod`), Node.js 18+.

## 3. Cluster setup (once)

All commands start from the repository root.

```bash
cd ~/path/to/CS331-T003-Kubernetes
minikube start --nodes 3 --driver=docker --cni=calico -p k8s-experiments
kubectl get nodes        # all 3 Ready
```

### 3.1 Move Calico to 172.16.0.0/16

minikube's Calico uses `10.244.0.0/16`, but Flannel can only use the node podCIDRs
(`10.244.x.0/24`). So Calico moves. Follow the steps in the header of
`operator/deploy/calico-primary-pool.yaml`:

```bash
kubectl apply -f operator/deploy/calico-primary-pool.yaml
kubectl patch ippools.crd.projectcalico.org default-ipv4-ippool --type=merge -p '{"spec":{"disabled":true}}'
# delete every pod that has a 10.244 IP, so it restarts with a 172.16 IP:
kubectl get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name} {.status.podIP}{"\n"}{end}' | awk '$3 ~ /^10\.244\./ {print "-n", $1, $2}' | xargs -L1 -r kubectl delete pod
kubectl delete ippools.crd.projectcalico.org default-ipv4-ippool
kubectl get ipamblocks.crd.projectcalico.org,blockaffinities.crd.projectcalico.org -o name | grep 10-244 | xargs -r kubectl delete
```

Check: `kubectl get pods -A -o wide` shows no pod IP starting with `10.244` (host-network
pods show node IPs `192.168.58.x`, which is fine).

### 3.2 Multus and Flannel

```bash
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/deployments/multus-daemonset.yml
kubectl -n kube-system rollout status ds/kube-multus-ds
kubectl apply -f operator/deploy/flannel-backup.yaml
kubectl -n kube-flannel rollout status ds/kube-flannel-ds
kubectl apply -f operator/deploy/multus-nad.yaml
```

`flannel-backup.yaml` is the official Flannel file with one part removed (its header says
which and why): Flannel must not become a node's main network. Check on a node:

```bash
docker exec k8s-experiments-m02 sh -c 'cat /run/flannel/subnet.env; ls /etc/cni/net.d'
```

You want `FLANNEL_SUBNET=10.244.1.1/24` (one /24 per node) and **no** `10-flannel.conflist`.

### 3.2.1 Adding a worker node later

If you add a node to a running cluster (`minikube node add -p k8s-experiments`), two
things do not happen by themselves.

**Multus can latch onto the wrong default network.** Multus writes `00-multus.conf` by
copying whichever CNI config it finds first in `/etc/cni/net.d`. On a brand-new node it
usually wins the race against Calico and captures `200-loopback.conf`, which has no
`name` field. Every pod on that node then hangs in `ContainerCreating` with:

```
plugin type="multus" name="multus-cni-network" failed (add):
  error adding container to network "": missing network name
```

Check the delegate, and if it says `loopback`, restart Multus on that node so it
regenerates against Calico:

```bash
docker exec k8s-experiments-m03 sh -c 'cat /etc/cni/net.d/00-multus.conf'   # want name "k8s-pod-network"
kubectl -n kube-system delete pod -l app=multus --field-selector spec.nodeName=k8s-experiments-m03
```

**The health-agent image is not on the new node.** `cni-health-agent:dev` is a local tag
with `imagePullPolicy: IfNotPresent`, and `minikube image load` only reaches the nodes
that existed when it ran, so the new node's pod goes `ImagePullBackOff`. Load it again:

```bash
minikube -p k8s-experiments image load cni-health-agent:dev
```

Then recreate the replicas so the spread constraint takes effect — `topologySpreadConstraints`
are applied at scheduling time, so pods already running stay where they are:

```bash
kubectl rollout restart deployment/resilience-target
```

### 3.3 Operator CRDs, settings, health agent, workload

```bash
cd operator
make install                                   # all CRDs, incl. NodeBlock (the manager stops after 2 min without it)
kubectl apply -f config/samples/cniresilience_sample.yaml
CGO_ENABLED=0 go build -o bin/healthagent ./cmd/healthagent
docker build -f Dockerfile.healthagent -t cni-health-agent:dev bin
minikube -p k8s-experiments image load cni-health-agent:dev
kubectl apply -f deploy/healthagent-daemonset.yaml
cd ..
kubectl apply -f experiments/manifests/resilience-workload.yaml
```

The workload is `resilience-target` (2 replicas, one on each worker, both with `net1` and the
readiness gate), `resilience-service`, the `traffic-client` pod on the control-plane node
(one request per second, its log is the real traffic measurement), and one `path-probe`
pod per node (the page uses them to show each node's real data path).

Check `net1`:

```bash
kubectl get pods -l app=resilience-target -o custom-columns='NAME:.metadata.name,NODE:.spec.nodeName,ETH0:.status.podIP,STATUS:.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status'
```

Each pod shows an `eth0` address in `172.16.x.x` and a `net1` address in `10.244.<node>.x`.

## 4. Run it

Two terminals.

```bash
# Terminal 1: the operator. Gated pods are only Ready while it runs.
cd ~/path/to/CS331-T003-Kubernetes/operator
make run
```

```bash
# Terminal 2: the page
cd ~/path/to/CS331-T003-Kubernetes/k8s-visualizer
npm run dev
# open http://localhost:5173/project2
```

`kubectl proxy` is only needed for the other pages of the visualizer, not `/project2`.

Healthy start: Operator `Running`, Phase `Healthy`, every node card shows Data path working,
Operator says Primary `Healthy`, Backup `Healthy`, Active path `primary`, and the traffic bars
are all green.

## 5. Experiments

Pick the target node on the page, then press the button. Measured results:

| # | What you do | What happens (measured) |
|---|---|---|
| 1 | Target **m02 or m03**, **Block pod traffic** | About half the requests fail until the operator blames the node (10–31 s in our runs). Then Phase `FailedOver`, the node's Active path `backup`, a shadow slice appears with the pod's `10.244` address, the node gets the `recovering` taint, and its calico-node pod is restarted **once**. Traffic is back to all `200`. The pod's nginx log shows requests arriving from `10.244.0.0` (the Flannel tunnel). |
| 2 | **Reset the lab** after 1 | Rule removed. The operator itself moves traffic back within ~10 s: Phase `Healthy`, shadow gone, taint gone, no failed request during the switch. |
| 3 | Same as 1, but turn **auto failover off** first | Node blamed, tainted, one calico restart, but no shadow: about half the requests keep failing. Message says `autoFailover disabled`. Turning it on again fails over within 5 s. |
| 4 | Block **both workers** | Phase `Unknown`, nothing done, traffic 0 %. With 3 nodes every probe then touches a broken node, so "both workers broken" cannot be told apart from "control-plane broken and the worker link broken". A node is blamed only if some probe between two other nodes works, so on N nodes at most N−2 broken nodes can be located. |
| 5 | Block **k8s-experiments** (the control-plane node) | Traffic 0 %: the `traffic-client` itself is on that node and has no backup. Phase `Degraded`; message says there is nothing to move (no app pod there). |
| 6 | **Delete calico-node pod** | The pod is replaced in about 30 s, no request fails, the operator does nothing (killing Calico does not break existing routes). |

Faults you inject by hand (all tagged `cniresilience-drop`, so **Reset the lab** removes them):

```bash
# Break m03's backup (Flannel VXLAN) as well as its primary:
docker exec k8s-experiments-m03 iptables -I INPUT -p udp --dport 8472 -j DROP -m comment --comment cniresilience-drop
# Break Calico's IPIP tunnel on m03 (a different primary fault from the page button):
docker exec k8s-experiments-m03 iptables -t raw -I PREROUTING -p 4 -j DROP -m comment --comment cniresilience-drop
```

| # | What you do | What happens (measured) |
|---|---|---|
| 7 | Block m03's primary **and** backup | Active path `none`: the operator takes m03's pod out of the Service (gate off, no shadow) because neither address works. Traffic all `200` through m02. Unblock only the backup and it fails over again. |
| 8 | IPIP drop on m03 | Same as experiment 1 (failover in 11 s in our run). |
| 9 | Stop the operator (Ctrl+C) during a failover | Traffic keeps working: the shadow and gates are in the cluster, not in the operator. The page marks operator data as stale. Start it again and it carries on. A Reset while it is stopped also removes taint and shadows. |
| 10 | Freeze a whole worker: `docker pause k8s-experiments-m03` (undo: `docker unpause`) | The operator blames m03 and takes its pod out after ~26 s, but requests keep failing until Kubernetes marks the node unreachable at ~55 s. A pod's Ready is computed by the kubelet on its own node, and that kubelet is frozen, so the gate cannot take effect sooner. After unpause everything returns to Healthy by itself. |

## 6. Limits we know about

- Detection takes 10–31 s (probe failures must repeat; each node probes one peer per round).
- Only pods with the readiness gate and `net1` can fail over. Pods without a gate are never touched.
- The gated pods are Ready only while the operator runs (it sets the gate). Start it before the workload, or the Service has no endpoints.
- A frozen node is no faster than plain Kubernetes (experiment 10).
- App pods see Service requests coming from the sending node's tunnel address (`172.16.148.128` on Calico, `10.244.0.0` on Flannel), not from the client pod's address. Traffic works. Not yet proven why; a likely part is that `kube-proxy` still has `clusterCIDR 10.244.0.0/16`, the old Calico range.

## 7. Clean up

```bash
kubectl delete -f experiments/manifests/resilience-workload.yaml
minikube stop -p k8s-experiments
```
