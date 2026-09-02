# Experiment Setup

## Prerequisites

Install these tools on your Linux/WSL machine:

- Docker
- kubectl
- minikube
- calicoctl

## Start the Cluster

Start a 2-node minikube cluster with Calico CNI using the docker driver:

```bash
minikube start --nodes 2 --driver=docker --cni=calico --cpus=2 --memory=4096 -p k8s-experiments
```

Wait for all nodes to become Ready:

```bash
kubectl get nodes -o wide
```

Expected output — both nodes `Ready`:

```
NAME              STATUS   ROLES           AGE   VERSION
k8s-experiments       Ready    control-plane   1m    v1.x.x
k8s-experiments-m02   Ready    <none>          30s   v1.x.x
```

Verify Calico pods are running:

```bash
kubectl get pods -n kube-system -l k8s-app=calico-node
```

Both `calico-node` pods should be `Running` (one per node).

## Deploy Test Pods

Apply all manifests from the `manifests/` directory:

```bash
kubectl apply -f experiments/manifests/backend-api-deployment.yaml
kubectl apply -f experiments/manifests/frontend-probe-deployment.yaml
kubectl apply -f experiments/manifests/dns-checker-deployment.yaml
```

Wait for all pods to be Running:

```bash
kubectl get pods -o wide
```

You should see 6 pods (2 replicas each of backend-api, frontend-probe, dns-checker) spread across both nodes.

## Deploy Prometheus and Grafana

```bash
kubectl apply -f experiments/manifests/prometheus-stack.yaml
```

Wait for the monitoring pods:

```bash
kubectl get pods -n monitoring
```

Access the dashboards:

```bash
# Prometheus UI
minikube service prometheus -n monitoring -p k8s-experiments --url

# Grafana UI (login: admin / admin)
minikube service grafana -n monitoring -p k8s-experiments --url
```

## Verify Baseline

Before running any experiment, confirm everything works.

**1. frontend-probe logs show successful requests:**

```bash
kubectl logs -l app=frontend-probe --tail=5
```

Expected:

```
10:30:01 | status=200 | latency=12ms
10:30:03 | status=200 | latency=8ms
```

**2. dns-checker logs show successful DNS resolution:**

```bash
kubectl logs -l app=dns-checker --tail=5
```

Expected:

```
--- 10:30:01 ---
INTERNAL | OK   | 3ms
EXTERNAL | OK   | 15ms
```

**3. Prometheus is scraping CoreDNS metrics:**

Open Prometheus UI and query:

```
coredns_dns_requests_total
```

You should see results from the CoreDNS targets.

**4. Pod-to-pod cross-node connectivity:**

```bash
# Get pod IPs
kubectl get pods -o wide

# Exec into a dns-checker pod on node-1 and ping a pod on node-2 (busybox has ping)
kubectl exec -it <dns-checker-on-node1> -- ping -c 3 <backend-api-IP-on-node2>
```

All 3 pings should succeed.

## Useful Commands During Experiments

```bash
# Watch pod status live
kubectl get pods -o wide -w

# Stream frontend-probe logs
kubectl logs -f -l app=frontend-probe

# Stream dns-checker logs
kubectl logs -f -l app=dns-checker

# SSH into a minikube node
minikube ssh -p k8s-experiments                    # node-1 (control plane)
minikube ssh -p k8s-experiments -n k8s-experiments-m02 # node-2 (worker)

# Check Calico node status
kubectl get pods -n kube-system -l k8s-app=calico-node -o wide

# Check CoreDNS status
kubectl get pods -n kube-system -l k8s-app=kube-dns -o wide
```

## Cleanup

After all experiments are done:

```bash
minikube delete -p k8s-experiments
```
