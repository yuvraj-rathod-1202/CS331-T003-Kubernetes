# Shared settings for the edge testbed. Source it: `. testbed/env.sh`
#
# The testbed is one 3-node cluster with its own profile and kubeconfig, so it never
# touches another cluster on the same host. Two distributions are supported:
#   DISTRO=minikube  (default) minikube, Docker driver, profile edge-lab
#   DISTRO=k3s       K3s in Docker containers via k3d, cluster edge-k3s
# Export DISTRO before running any testbed script.

TESTBED_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd "$TESTBED_DIR/.." && pwd)

DISTRO=${DISTRO:-minikube}
K8S_VERSION=${K8S_VERSION:-v1.35.1}
MULTUS_VERSION=${MULTUS_VERSION:-v4.3.2}

case "$DISTRO" in
  minikube)
    PROFILE=${PROFILE:-edge-lab}
    STATE_DIR=${STATE_DIR:-$HOME/edge-lab}
    # Node roles: C = control plane (healthy relay), A = traffic source, B = destination.
    NODE_C=$PROFILE
    NODE_A=$PROFILE-m02
    NODE_B=$PROFILE-m03
    BACKUP_SUBNET=${BACKUP_SUBNET:-192.168.200.0/24}
    ;;
  k3s)
    PROFILE=${PROFILE:-edge-k3s}
    STATE_DIR=${STATE_DIR:-$HOME/edge-k3s}
    K3S_IMAGE=${K3S_IMAGE:-rancher/k3s:${K8S_VERSION}-k3s1}
    PRIMARY_SUBNET=${PRIMARY_SUBNET:-192.168.59.0/24}
    BACKUP_SUBNET=${BACKUP_SUBNET:-192.168.201.0/24}
    K3D=${K3D:-$STATE_DIR/bin/k3d}
    NODE_C=k3d-$PROFILE-server-0
    NODE_A=k3d-$PROFILE-agent-0
    NODE_B=k3d-$PROFILE-agent-1
    ;;
  *) echo "DISTRO must be minikube or k3s" >&2; exit 1 ;;
esac
NODES="$NODE_C $NODE_A $NODE_B"

export KUBECONFIG=${EDGE_KUBECONFIG:-$STATE_DIR/kubeconfig}
KUBECTL=${KUBECTL:-$STATE_DIR/bin/kubectl}
k() { "$KUBECTL" "$@"; }

# Second underlay network. Calico (primary) stays on eth0 (the cluster's own network);
# Flannel (backup) is bound to eth1 on this network, so the two paths share no link.
BACKUP_NET=${BACKUP_NET:-$PROFILE-backup}
PRIMARY_IF=eth0
BACKUP_IF=eth1

# Images built from this repository.
IMG_AGENT=cni-health-agent:dev
IMG_ROUTER=cni-link-router:dev
IMG_IOTSIM=edge-iotsim:dev

# The K3s node image has no tc or pgrep, so on K3s every command that touches a node's
# network or processes runs in a helper container that shares the node's network and
# PID namespaces (created by bootstrap-k3s.sh). On minikube it runs in the node itself.
NETBOX_IMAGE=${NETBOX_IMAGE:-gcr.io/k8s-minikube/kicbase:v0.0.50}
nbox() { if [ "$DISTRO" = k3s ]; then echo "$1-net"; else echo "$1"; fi; }

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# IPv4 address of a node container on a given underlay interface.
node_ip() { docker exec "$(nbox "$1")" ip -o -4 addr show dev "$2" | awk '{split($4, a, "/"); print a[1]; exit}'; }
