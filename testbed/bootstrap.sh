#!/usr/bin/env bash
# Build the edge testbed from nothing: a 3-node minikube cluster with
#   - Calico (primary, IPIP, 172.16.0.0/16) on eth0, minikube's network
#   - Flannel (backup, VXLAN, 10.244.0.0/16) through Multus on eth1, a second
#     Docker network, so the primary and backup paths share no link
#   - the CRDs, the CNIResilience object, the health agent and the link router
# Safe to re-run: every step checks whether it is already done.
#
#   testbed/bootstrap.sh          create or complete the testbed
#   testbed/bootstrap.sh --delete remove the cluster and the backup network
set -euo pipefail
. "$(dirname "$0")/env.sh"

if [ "${1:-}" = "--delete" ]; then
  "$TESTBED_DIR/operator.sh" stop || true
  minikube delete -p "$PROFILE"
  docker network rm "$BACKUP_NET" 2>/dev/null || true
  exit 0
fi

mkdir -p "$STATE_DIR/bin"
if [ ! -x "$KUBECTL" ]; then
  log "Downloading kubectl $K8S_VERSION"
  curl -fsSLo "$KUBECTL" "https://dl.k8s.io/release/$K8S_VERSION/bin/linux/amd64/kubectl"
  chmod +x "$KUBECTL"
fi

# 1. Cluster
# minikube's own start/status fail on a node container with two networks, so judge by
# the containers, and detach the backup network before (re)starting a stopped cluster.
running=1
for n in $NODES; do
  [ "$(docker inspect -f '{{.State.Running}}' "$n" 2>/dev/null)" = true ] || running=0
done
if [ "$running" = 0 ]; then
  for n in $NODES; do docker network disconnect "$BACKUP_NET" "$n" >/dev/null 2>&1 || true; done
  log "Starting minikube profile $PROFILE"
  minikube start -p "$PROFILE" --nodes 3 --driver=docker --cni=calico \
    --kubernetes-version="$K8S_VERSION" --cpus=2 --memory=2500
fi
k wait --for=condition=Ready nodes --all --timeout=300s >/dev/null

# 2. Pin Calico to eth0 before a second interface exists, so it never picks eth1.
if ! k -n kube-system get ds calico-node -o yaml | grep -q "interface=$PRIMARY_IF"; then
  log "Pinning Calico to $PRIMARY_IF"
  k -n kube-system set env ds/calico-node IP_AUTODETECTION_METHOD="interface=$PRIMARY_IF"
fi
k -n kube-system rollout status ds/calico-node --timeout=300s >/dev/null

# 3. Backup underlay network, attached to every node as eth1.
docker network inspect "$BACKUP_NET" >/dev/null 2>&1 ||
  { log "Creating Docker network $BACKUP_NET ($BACKUP_SUBNET)"; docker network create --subnet "$BACKUP_SUBNET" "$BACKUP_NET" >/dev/null; }
for n in $NODES; do
  if ! docker inspect "$n" --format '{{json .NetworkSettings.Networks}}' | grep -q "\"$BACKUP_NET\""; then
    log "Attaching $n to $BACKUP_NET"
    docker network connect "$BACKUP_NET" "$n"
  fi
  ip=$(node_ip "$n" "$BACKUP_IF")
  [ -n "$ip" ] || die "$n has no IPv4 address on $BACKUP_IF"
  log "$n: $PRIMARY_IF=$(node_ip "$n" "$PRIMARY_IF") $BACKUP_IF=$ip"
done

# 4. Move Calico to 172.16.0.0/16 (Flannel needs the node podCIDRs in 10.244.0.0/16).
if k get ippools.crd.projectcalico.org default-ipv4-ippool >/dev/null 2>&1; then
  log "Moving Calico to 172.16.0.0/16"
  k apply -f "$REPO_DIR/operator/deploy/calico-primary-pool.yaml" >/dev/null
  k patch ippools.crd.projectcalico.org default-ipv4-ippool --type=merge -p '{"spec":{"disabled":true}}' >/dev/null
  k get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name} {.status.podIP}{"\n"}{end}' |
    awk '$3 ~ /^10\.244\./ {print "-n", $1, $2}' | xargs -L1 -r "$KUBECTL" delete pod --wait=false >/dev/null
  k delete ippools.crd.projectcalico.org default-ipv4-ippool >/dev/null
  k get ipamblocks.crd.projectcalico.org,blockaffinities.crd.projectcalico.org -o name |
    grep 10-244 | xargs -r "$KUBECTL" delete >/dev/null || true
fi

# 4b. Tell kube-proxy the real pod network. With minikube's default clusterCIDR
# (10.244.0.0/16) every Service connection from a Calico pod is masqueraded, and Linux
# drops a masqueraded connection's NAT state when its output interface changes, so each
# steering switch (tunl0 <-> flannel.1) would reset long-lived Service connections.
# Traffic to backup (10.244) addresses is still masqueraded by Flannel's own rule.
if ! k -n kube-system get cm kube-proxy -o jsonpath='{.data.config\.conf}' | grep -q 'clusterCIDR: 172.16.0.0/16'; then
  log "Setting kube-proxy clusterCIDR to 172.16.0.0/16"
  k -n kube-system get cm kube-proxy -o yaml | sed 's#clusterCIDR: 10.244.0.0/16#clusterCIDR: 172.16.0.0/16#' | k apply -f - >/dev/null
  k -n kube-system rollout restart ds/kube-proxy >/dev/null
fi
k -n kube-system rollout status ds/kube-proxy --timeout=180s >/dev/null

# 5. Multus and Flannel (backup only, bound to eth1).
if ! k -n kube-system get ds kube-multus-ds >/dev/null 2>&1; then
  log "Installing Multus $MULTUS_VERSION"
  k apply -f "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/$MULTUS_VERSION/deployments/multus-daemonset.yml" >/dev/null
fi
# The upstream manifest uses the floating :snapshot image and a 50Mi limit that gets the
# container OOM-killed; pin the release image and give it room.
if ! k -n kube-system get ds kube-multus-ds -o jsonpath='{.spec.template.spec.containers[0].image}' | grep -q ":$MULTUS_VERSION$"; then
  k -n kube-system patch ds kube-multus-ds --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"ghcr.io/k8snetworkplumbingwg/multus-cni:$MULTUS_VERSION\"},
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/initContainers/0/image\",\"value\":\"ghcr.io/k8snetworkplumbingwg/multus-cni:$MULTUS_VERSION\"},
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/resources/limits/memory\",\"value\":\"256Mi\"}]" >/dev/null
fi
k -n kube-system rollout status ds/kube-multus-ds --timeout=300s >/dev/null
log "Installing Flannel bound to $BACKUP_IF"
awk -v iface="$BACKUP_IF" '{ print } /- --kube-subnet-mgr/ { sub(/- --kube-subnet-mgr.*/, ""); print $0 "- --iface=" iface }' \
  "$REPO_DIR/operator/deploy/flannel-backup.yaml" | k apply -f - >/dev/null
k -n kube-flannel rollout status ds/kube-flannel-ds --timeout=300s >/dev/null
k apply -f "$REPO_DIR/operator/deploy/multus-nad.yaml" >/dev/null

# 6. Operator CRDs and the CNIResilience object.
k apply --server-side -f "$REPO_DIR/operator/config/crd/bases/" >/dev/null
k apply -f "$REPO_DIR/operator/config/samples/cniresilience_sample.yaml" >/dev/null

# 7. Images, health agent and link router.
"$TESTBED_DIR/build.sh"
k apply -f "$REPO_DIR/operator/deploy/healthagent-daemonset.yaml" >/dev/null
k apply -f "$REPO_DIR/operator/deploy/linkrouter-daemonset.yaml" >/dev/null
k -n kube-system rollout restart ds/cni-health-agent ds/cni-link-router >/dev/null
k -n kube-system rollout status ds/cni-health-agent --timeout=300s >/dev/null
k -n kube-system rollout status ds/cni-link-router --timeout=300s >/dev/null

# 8. Checks.
log "Checks"
for n in $NODE_A $NODE_B; do
  pub=$(k get node "$n" -o jsonpath='{.metadata.annotations.flannel\.alpha\.coreos\.com/public-ip}')
  [ "$pub" = "$(node_ip "$n" "$BACKUP_IF")" ] || die "Flannel on $n uses $pub, not its $BACKUP_IF address"
  calico=$(k get node "$n" -o jsonpath='{.metadata.annotations.projectcalico\.org/IPv4Address}')
  [ "${calico%/*}" = "$(node_ip "$n" "$PRIMARY_IF")" ] || die "Calico on $n uses $calico, not its $PRIMARY_IF address"
  log "$n: Calico on $calico, Flannel on $pub"
done
k get pods -A -o wide --no-headers | awk '$4 != "Running" && $4 != "Completed" {bad=1; print} END {exit bad}' ||
  die "Some pods are not running"
log "Testbed ready. Start the operator with testbed/operator.sh start"
