# Problem 2: Per-Link Path Steering

How to set up and run the link steering experiments (L1 to L13). The design is in
[`operator/docs/cni-resilience.md`](../operator/docs/cni-resilience.md), section "Per-link
steering". The results tables in section 7 are empty until the runs are done.

## 1. The idea in one paragraph

The node-level failover of [`cni-resilience-experiment.md`](cni-resilience-experiment.md)
acts only when a whole **node** is proven broken. This guide covers a broken **link**:
node A cannot reach node B over Calico, but A and B each work with everyone else. A new
host-network DaemonSet, `cni-link-router`, runs on every node. It reads the health agents'
probe reports and, for each peer, picks one of four paths for traffic leaving its node:
`primary` (Calico), `backup` (Flannel), `relay` (through a third node over Calico) or
`none` (no path, alert). It does this with policy routing: table `200`, rules at prefs
`90`, `91` and `100`. Pod addresses never change, so Service traffic, direct pod-IP traffic
and open connections all keep working.

## 2. Requirements

The same as the first guide: Docker, minikube, kubectl, Go (version in `operator/go.mod`).
You also need a running lab cluster set up as in sections 3.1 to 3.3 of
[`cni-resilience-experiment.md`](cni-resilience-experiment.md): Calico on `172.16.0.0/16`,
Flannel and Multus, the `CNIResilience` sample, and the health agent. This guide adds the
router and its test workload.

## 3. Setup (once)

All commands start from the repository root. Nodes in the lab cluster (profile
`k8s-experiments`):

| Role | Node | Why |
|---|---|---|
| A | `k8s-experiments-m02` | Client side. The faulty link is A to B |
| B | `k8s-experiments-m03` | Server side |
| C | `k8s-experiments` (control plane) | Relay |

A and B are workers, so a node blamed in L4b is not the node running the old
`traffic-client`. Confirm the node IPs, which the fault commands need (we saw
`192.168.67.2`, `.3` and `.4`):

```bash
kubectl get nodes -o wide
A_IP=192.168.67.3    # k8s-experiments-m02
B_IP=192.168.67.4    # k8s-experiments-m03
C_IP=192.168.67.2    # k8s-experiments
```

### 3.1 Build and load the images

The agent is rebuilt because it now probes every peer. The router is new. The operator runs
from your machine (`make run`), so it needs no image.

```bash
cd operator
CGO_ENABLED=0 go build -o bin/healthagent ./cmd/healthagent
docker build -f Dockerfile.healthagent -t cni-health-agent:dev bin
minikube -p k8s-experiments image load cni-health-agent:dev
GOOS=linux CGO_ENABLED=0 go build -o bin/linkrouter ./cmd/linkrouter
docker build -f Dockerfile.linkrouter -t cni-link-router:dev bin
minikube -p k8s-experiments image load cni-link-router:dev
cd ..
```

### 3.2 Deploy

```bash
cd operator
make install                                   # CRDs, incl. the new status fields
kubectl apply -f config/samples/cniresilience_sample.yaml
kubectl apply -f deploy/healthagent-daemonset.yaml
kubectl -n kube-system rollout restart ds/cni-health-agent   # pick up the new image
kubectl apply -f deploy/linkrouter-daemonset.yaml
kubectl -n kube-system rollout status ds/cni-link-router
cd ..
```

The sample sets `linkFailover: true` and `linkRelay: true`. Start the operator in its own
terminal and leave it running:

```bash
cd operator && make run
```

Check that every node has both reports, and that nothing is steered yet:

```bash
kubectl get nodes -o custom-columns='NODE:.metadata.name,ROUTES:.metadata.annotations.cniresilience\.cn-operator/link-routes'
for n in k8s-experiments k8s-experiments-m02 k8s-experiments-m03; do
  echo "== $n"; docker exec $n ip route show table 200; docker exec $n ip rule show | grep -E '^(90|91|100):'
done
kubectl get cniresilience default-resilience -o jsonpath='{.status.links}{"\n"}'
```

Expected: every peer `primary`, table 200 empty, the three rules present, `status.links`
empty.

### 3.3 Test workload

A target pod on B, a pod on A that sends one request per second to the pod IP, and a pod
on A that does the same to a Service. The Service has no selector. Its only endpoint is the
B pod, set by hand in an EndpointSlice, so every Service request must go to B.

```bash
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: link-target
  labels: {app: link-target}
spec:
  nodeSelector: {kubernetes.io/hostname: k8s-experiments-m03}
  containers:
  - name: web
    image: nginx:alpine
    ports: [{containerPort: 80}]
EOF
kubectl wait --for=condition=Ready pod/link-target
TARGET_IP=$(kubectl get pod link-target -o jsonpath='{.status.podIP}')
echo $TARGET_IP        # 172.16.x.x

kubectl apply -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: link-service
spec:
  ports: [{port: 80, targetPort: 80}]
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: link-service-b
  labels: {kubernetes.io/service-name: link-service}
addressType: IPv4
ports: [{port: 80, protocol: TCP}]
endpoints:
- addresses: ["$TARGET_IP"]
  nodeName: k8s-experiments-m03
---
apiVersion: v1
kind: Pod
metadata:
  name: link-client-pod
spec:
  nodeSelector: {kubernetes.io/hostname: k8s-experiments-m02}
  containers:
  - name: client
    image: curlimages/curl
    command: [sh, -c]
    args:
    - |
      while true; do
        echo "\$(date +%s) \$(curl -s -o /dev/null -m 1 -w '%{http_code}' http://$TARGET_IP)"
        sleep 1
      done
---
apiVersion: v1
kind: Pod
metadata:
  name: link-client-svc
spec:
  nodeSelector: {kubernetes.io/hostname: k8s-experiments-m02}
  containers:
  - name: client
    image: curlimages/curl
    command: [sh, -c]
    args:
    - |
      while true; do
        echo "\$(date +%s) \$(curl -s -o /dev/null -m 1 -w '%{http_code}' http://link-service)"
        sleep 1
      done
EOF
kubectl wait --for=condition=Ready pod/link-client-pod pod/link-client-svc
kubectl logs --tail=3 link-client-pod
kubectl logs --tail=3 link-client-svc
```

Each log line is `<unix time> <http code>`. A timeout shows `000`. Both logs must show `200`
before you start. To count failures after a run:

```bash
kubectl logs link-client-pod | awk '$2!=200' | wc -l
```

The target is a plain pod with no readiness gate and no `net1`, so node-level failover never
touches it. All steering comes from the router.

## 4. Fault commands

All faults carry the comment `cniresilience-drop`. They match on the **peer node's IP**,
so only one link breaks. They go on **both** ends of the link, and run inside the node
containers.

```bash
# Calico (IPIP, protocol 4) dropped between A and B:
docker exec k8s-experiments-m02 iptables -t raw -I PREROUTING -p 4 -s $B_IP -j DROP -m comment --comment cniresilience-drop
docker exec k8s-experiments-m03 iptables -t raw -I PREROUTING -p 4 -s $A_IP -j DROP -m comment --comment cniresilience-drop

# Flannel (VXLAN, UDP 8472) dropped between A and B:
docker exec k8s-experiments-m02 iptables -t raw -I PREROUTING -p udp --dport 8472 -s $B_IP -j DROP -m comment --comment cniresilience-drop
docker exec k8s-experiments-m03 iptables -t raw -I PREROUTING -p udp --dport 8472 -s $A_IP -j DROP -m comment --comment cniresilience-drop
```

The same two commands with another pair of nodes (for example A and C) break that pair.
Remove every fault on a node:

```bash
for n in k8s-experiments k8s-experiments-m02 k8s-experiments-m03; do
  docker exec $n sh -c 'iptables -t raw -S PREROUTING | grep cniresilience-drop | sed "s/^-A/-D/" | while read r; do iptables -t raw $r; done'
done
```

The Reset button on the `/project2` page removes the same tagged rules.

Watching what the router did:

```bash
docker exec k8s-experiments-m02 ip route show table 200
docker exec k8s-experiments-m02 ip rule show | grep -E '^(90|91|100):'
kubectl get node k8s-experiments-m02 -o jsonpath='{.metadata.annotations.cniresilience\.cn-operator/link-routes}{"\n"}'
kubectl get cniresilience default-resilience -o jsonpath='{.status.links}{"\n"}{.status.conditions}{"\n"}'
kubectl get events --field-selector involvedObject.kind=CNIResilience
kubectl -n kube-system logs -l app=cni-link-router --tail=20 --prefix
```

Time to steer: note the time the fault is injected (`date +%s`), and read the first `200`
after the last `000` in the client log.

## 5. Experiments

Run them in order. Start each from the healthy state of section 3.2 (all `primary`, no
faults). Record each result in section 7.

| # | What you do | Pass criteria |
|---|---|---|
| L1 | Drop Calico between A and B (both ends) | A to B steered to `backup`: A has a route for B's Calico block via B's Flannel address on `flannel.1`. Traffic recovers within about 10 s. 0 failed requests after recovery. A to C and B to C untouched |
| L2 | Remove the L1 fault | Route removed, link back to `primary`, 0 failed requests |
| L3 | Drop Calico **and** Flannel between A and B | `relay` via C on `tunl0`. Traffic recovers. 0 failures after |
| L4 | Set `linkRelay: false`, then L3 | A to B and B to A are `none`. `NoPath` Events. `LinksReachable=False` naming them. No node blamed. Remove the faults: `PathRestored` Events. Set `linkRelay` back to `true` |
| L4b | L3, plus drop Calico and Flannel between A and C (everything into and out of A is cut) | `Classify` blames A, so node-level isolation, taint and `calico-node` restart of A happen as before. Every pair involving A is `none` and listed in the condition. This documents how the two layers interact |
| L5 | Drop Calico in one direction only: on B only, drop IPIP from A | Both A and B steer (probes are round trips). Traffic OK both ways |
| L6 | 2 MB file on the target: (1) plain fetch during L1, (2) rate-limited download started before the L1 fault and still running when it lands | Checksums correct (MTU, open connections) |
| L7 | Hold L1 for 10 minutes | No flapping: route changes on A = 1, and the agent still reports A to B primary down |
| L8 | Restart the router pod on A during L1 | Routes stay. 0 failed requests. The new pod reconciles cleanly |
| L9 | During L1: apply a NetworkPolicy that denies A's pod to B's pod | Still denied over the reroute |
| L10 | Regression: experiments 1 to 3 of the first guide | Same outcomes as recorded there |
| L11 | `--cleanup` | Table 200 empty, rules 90, 91 and 100 gone |
| L12 | Restart the health agent on A during L1 | A's steering route stays. 0 failed requests |
| L13 | During L1 and again during L3: Service traffic and direct pod-IP traffic, separately | Both succeed (checks that Service masquerade does not break the reroute) |

### Procedures

**L1 and L2.** Inject the Calico fault. Watch `ip route show table 200` on A and on B: a
route per Calico block of the peer. Check `status.links` lists A to B and B to A as
`backup`. Remove the fault. The route goes and `status.links` empties.

**L3.** Add the Flannel fault to the L1 fault. A's route for B's blocks is now
`via $C_IP dev tunl0 onlink`. C's own table 200 stays empty: C still reaches both A and B
directly. Check both clients keep getting `200`.

**L4.** Patch the switch, then run L3:

```bash
kubectl patch cniresilience default-resilience --type=merge -p '{"spec":{"linkRelay":false}}'
```

Watch `kubectl get events -w` for `NoPath`, and the condition for `LinksReachable=False`.
Check `status.phase` is at least `Degraded`, and no node is blamed (no taint, no shadow
slice). After removing the faults, check `PathRestored`.

**L4b.** Starting from L3, add the A to C faults (both CNIs, both ends). Watch the phase,
the taint on A and the `calico-node` restart on A. Record which pairs are `none`.

**L5.** On B only:
`docker exec k8s-experiments-m03 iptables -t raw -I PREROUTING -p 4 -s $A_IP -j DROP -m comment --comment cniresilience-drop`.
Both nodes see the link as down, because a probe needs a request and a response.

**L6.** Start from the healthy state, with no fault. Create a 2 MB file on the target (B)
and note its checksum. Use `link-client-pod` (on A) as the client. Its image has `curl`
and `md5sum`.

```bash
kubectl exec link-target -- sh -c 'dd if=/dev/urandom of=/usr/share/nginx/html/big bs=1k count=2048 && md5sum /usr/share/nginx/html/big'
```

Part 1, plain fetch during L1. Inject the L1 fault and wait until the route is in
table 200 on A (L1 passed). Then:

```bash
kubectl exec link-client-pod -- sh -c 'curl -s -m 30 http://'$TARGET_IP'/big -o /tmp/big1 && md5sum /tmp/big1'
```

The checksum must equal the one on the target, and the size must be 2097152 bytes
(`ls -l /tmp/big1`). This covers MTU over the reroute.

Part 2, a download open across the fault. Remove the L1 fault first and wait for `primary`.
Start a slow download in the background, so it takes about 20 s, then inject the L1 fault
a few seconds later:

```bash
kubectl exec link-client-pod -- sh -c 'curl -s --limit-rate 100k -m 120 http://'$TARGET_IP'/big -o /tmp/big2; echo exit=$?; md5sum /tmp/big2' > /tmp/l6-part2.txt &
sleep 5
# now inject the L1 fault (section 4), both ends
wait
cat /tmp/l6-part2.txt
```

Pass: `exit=0` and the checksum equals the target's. The fault must land while the
download is still running (it takes about 20 s). If it finished before the fault, repeat
with a lower `--limit-rate`. Record both parts.

**L7.** Hold L1 for 10 minutes. On A run `docker exec k8s-experiments-m02 timeout 600 ip monitor route`
before injecting the fault, and count the events in table 200. The router does not log
route changes, so this is the count. Check A's agent still reports A to B primary down:
`kubectl get node k8s-experiments-m02 -o jsonpath='{.metadata.annotations.cniresilience\.cn-operator/path-report}'`.

**L8.** `kubectl -n kube-system delete pod -l app=cni-link-router --field-selector spec.nodeName=k8s-experiments-m02`.
Check `ip route show table 200` on A during the restart, and the new pod's logs.

**L9.** During L1, apply a policy that selects `link-target` and has no ingress rules, so
it denies all ingress:

```bash
kubectl apply -f - <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: link-deny
spec:
  podSelector: {matchLabels: {app: link-target}}
  policyTypes: [Ingress]
EOF
```

`link-client-pod` must go back to `000` over the reroute. Delete the policy afterwards
(`kubectl delete networkpolicy link-deny`) and the client returns to `200`.

**L10.** Run experiments 1 to 3 of
[`cni-resilience-experiment.md`](cni-resilience-experiment.md) with the router running.
With a node-level fault the router also steers the broken node's blocks, which is expected.

**L11.** See section 6.

**L12.** `kubectl -n kube-system delete pod -l app=cni-health-agent --field-selector spec.nodeName=k8s-experiments-m02`
during L1. The new agent starts from its previous report, so A's route must not be removed
while the link is still down.

**L13.** During L1 and again during L3, read both `link-client-pod` (direct pod IP) and
`link-client-svc` (Service). Both must stay `200`.

## 6. Clean up

Remove the faults (command in section 4), then the workload:

```bash
kubectl delete pod link-target link-client-pod link-client-svc
kubectl delete service link-service
kubectl delete endpointslice link-service-b
```

Uninstall the router. Deleting the DaemonSet leaves its routes in place on purpose, so
remove them with `--cleanup` on each node. The debug pod shares the node's network
namespace:

```bash
kubectl -n kube-system delete ds cni-link-router
for n in k8s-experiments k8s-experiments-m02 k8s-experiments-m03; do
  kubectl debug node/$n --image=cni-link-router:dev --profile=netadmin -- /linkrouter --cleanup
done
for n in k8s-experiments k8s-experiments-m02 k8s-experiments-m03; do
  docker exec $n ip route show table 200
  docker exec $n ip rule show | grep -E '^(90|91|100):'
done
kubectl -n kube-system get pods -l app=cni-link-router    # none left
kubectl get pods -o name | grep node-debugger | xargs -r kubectl delete
```

The `ip` checks print nothing when the node is clean. This is also the L11 check. To
remove the whole cluster: `minikube stop -p k8s-experiments`.

## 7. Results

Not run yet. Fill in time to steer (seconds from fault injection to the first `200`),
failed requests (lines with a code other than `200` in the client log, after the steer),
and notes.

| Case | Time to steer | Failed requests | Notes |
|---|---|---|---|
| L1 | | | |
| L2 | | | |
| L3 | | | |
| L4 | | | |
| L4b | | | |
| L5 | | | |
| L6 | | | |
| L7 | | | |
| L8 | | | |
| L9 | | | |
| L10 | | | |
| L11 | | | |
| L12 | | | |
| L13 | | | |

## 8. Limits we know about

- If A reaches every relay candidate only over Flannel, a link with both direct paths
  down gets no relay: `none` plus an alert.
- App pods still see Service requests coming from the sending node's tunnel address, not
  the client pod's address (see section 6 of the first guide).
- On 3 nodes, any fault that leaves a pair with no relay also isolates a node at the node
  level (L4b).
- Detection takes the agent's streak (`failureThreshold` x `probeIntervalSeconds`, about
  6 s) plus report and reconcile time.
- Probes are round trips, so a one-way fault makes both ends steer (L5).
