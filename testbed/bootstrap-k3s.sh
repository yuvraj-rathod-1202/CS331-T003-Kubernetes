#!/usr/bin/env bash
# Build the edge testbed on K3s: a 3-node K3s cluster (k3d, K3s in Docker containers,
# no root needed) with the same networks as the minikube testbed:
#   - Calico (primary, IPIP, 172.16.0.0/16) on eth0, the cluster's own network
#   - Flannel (backup, VXLAN, 10.244.0.0/16) through Multus on eth1, a second
#     Docker network, so the primary and backup paths share no link
#   - the CRDs, the CNIResilience object, the health agent and the link router
# K3s's bundled Flannel, network policy controller, Traefik, ServiceLB and
# metrics-server are disabled; kube-proxy (embedded in K3s) gets Calico's pod range.
# Safe to re-run: every step checks whether it is already done.
#
#   DISTRO=k3s testbed/bootstrap-k3s.sh          create or complete the testbed
#   DISTRO=k3s testbed/bootstrap-k3s.sh --delete remove the cluster and networks
set -euo pipefail
export DISTRO=k3s
. "$(dirname "$0")/env.sh"

CALICO_VERSION=${CALICO_VERSION:-v3.31.3}
K3D_VERSION=${K3D_VERSION:-v5.8.3}

if [ "${1:-}" = "--delete" ]; then
  "$TESTBED_DIR/operator.sh" stop || true
  for n in $NODES; do docker rm -f "$(nbox "$n")" >/dev/null 2>&1 || true; done
  "$K3D" cluster delete "$PROFILE" || true
  # k3d can leave a node it failed to stop; remove whatever is left.
  for n in $NODES "k3d-$PROFILE-tools"; do docker rm -f "$n" >/dev/null 2>&1 || true; done
  docker network rm "$BACKUP_NET" 2>/dev/null || true
  exit 0
fi

mkdir -p "$STATE_DIR/bin"
if [ ! -x "$KUBECTL" ]; then
  log "Downloading kubectl $K8S_VERSION"
  curl -fsSLo "$KUBECTL" "https://dl.k8s.io/release/$K8S_VERSION/bin/linux/amd64/kubectl"
  chmod +x "$KUBECTL"
fi
if [ ! -x "$K3D" ]; then
  log "Downloading k3d $K3D_VERSION"
  curl -fsSLo "$K3D" "https://github.com/k3d-io/k3d/releases/download/$K3D_VERSION/k3d-linux-amd64"
  chmod +x "$K3D"
fi

# 1. Cluster: 1 server (C) and 2 agents (A, B), API only on localhost.
# kube-proxy gets Calico's pod range (Section 3.4 of the paper). KUBE_PROXY_POD_CIDR=
# (empty) leaves K3s's default, the cluster range 10.244.0.0/16, which masquerades
# Service traffic from Calico pods; only for the source-NAT ablation.
KUBE_PROXY_POD_CIDR=${KUBE_PROXY_POD_CIDR-172.16.0.0/16}
proxy_args=()
if [ -n "$KUBE_PROXY_POD_CIDR" ]; then
  proxy_args=(--k3s-arg "--kube-proxy-arg=cluster-cidr=$KUBE_PROXY_POD_CIDR@server:*"
              --k3s-arg "--kube-proxy-arg=cluster-cidr=$KUBE_PROXY_POD_CIDR@agent:*")
fi
# (k3d lists a half-deleted cluster too, so judge by the server container.)
if ! docker inspect "$NODE_C" >/dev/null 2>&1; then
  "$K3D" cluster delete "$PROFILE" >/dev/null 2>&1 || true
  log "Creating K3s cluster $PROFILE ($K3S_IMAGE, kube-proxy pod range ${KUBE_PROXY_POD_CIDR:-default})"
  "$K3D" cluster create "$PROFILE" --image "$K3S_IMAGE" --servers 1 --agents 2 \
    --subnet "$PRIMARY_SUBNET" --no-lb --api-port 127.0.0.1:6551 \
    --kubeconfig-update-default=false --kubeconfig-switch-context=false \
    --k3s-arg "--flannel-backend=none@server:*" \
    --k3s-arg "--disable-network-policy@server:*" \
    --k3s-arg "--disable=traefik@server:*" \
    --k3s-arg "--disable=servicelb@server:*" \
    --k3s-arg "--disable=metrics-server@server:*" \
    --k3s-arg "--cluster-cidr=10.244.0.0/16@server:*" \
    "${proxy_args[@]}"
fi
"$K3D" cluster start "$PROFILE" >/dev/null 2>&1 || true
"$K3D" kubeconfig get "$PROFILE" > "$KUBECONFIG"

# 2. Per node: the standard CNI plugins in /opt/cni/bin (K3s keeps its own copies
# elsewhere; containerd looks in /opt/cni/bin when K3s's Flannel is off), and a helper
# container in the node's network and PID namespaces for tc, ip, iptables and pgrep.
for n in $NODES; do
  docker exec "$n" sh -c 'mkdir -p /opt/cni/bin /etc/cni/net.d
    for p in /var/lib/rancher/k3s/data/cni/*; do
      [ -e "/opt/cni/bin/${p##*/}" ] || ln -s "$p" "/opt/cni/bin/${p##*/}"
    done'
  if [ "$(docker inspect -f '{{.State.Running}}' "$(nbox "$n")" 2>/dev/null)" != true ]; then
    docker rm -f "$(nbox "$n")" >/dev/null 2>&1 || true
    docker run -d --name "$(nbox "$n")" --network "container:$n" --pid "container:$n" \
      --cap-add NET_ADMIN --cap-add NET_RAW --entrypoint sleep "$NETBOX_IMAGE" infinity >/dev/null
    log "Helper $(nbox "$n") started"
  fi
done

# 3. Calico, pinned to eth0 before a second interface exists, with no default pool:
# the primary pool (172.16.0.0/16, IPIP) is the same object as on minikube.
if ! k -n kube-system get ds calico-node >/dev/null 2>&1; then
  log "Installing Calico $CALICO_VERSION"
  k apply -f "https://raw.githubusercontent.com/projectcalico/calico/$CALICO_VERSION/manifests/calico.yaml" >/dev/null
  k wait --for=condition=established crd/ippools.crd.projectcalico.org --timeout=120s >/dev/null
  k apply -f "$REPO_DIR/operator/deploy/calico-primary-pool.yaml" >/dev/null
fi
if ! k -n kube-system get ds calico-node -o yaml | grep -q "interface=$PRIMARY_IF"; then
  log "Pinning Calico to $PRIMARY_IF"
  k -n kube-system set env ds/calico-node -c calico-node \
    IP_AUTODETECTION_METHOD="interface=$PRIMARY_IF" NO_DEFAULT_POOLS=true >/dev/null
fi
k -n kube-system rollout status ds/calico-node --timeout=300s >/dev/null
k get ippools.crd.projectcalico.org -o name | { grep -v primary-ipv4-ippool || true; } | xargs -r "$KUBECTL" delete >/dev/null
k wait --for=condition=Ready nodes --all --timeout=300s >/dev/null

# 4. Backup underlay network, attached to every node as eth1.
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

# 5. Multus and Flannel (backup only, bound to eth1), as on minikube.
if ! k -n kube-system get ds kube-multus-ds >/dev/null 2>&1; then
  log "Installing Multus $MULTUS_VERSION"
  k apply -f "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/$MULTUS_VERSION/deployments/multus-daemonset.yml" >/dev/null
fi
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
log "K3s testbed ready. Start the operator with DISTRO=k3s testbed/operator.sh start"
