# Shared settings for the edge testbed. Source it: `. testbed/env.sh`
#
# The testbed is one minikube cluster with its own profile and kubeconfig, so it never
# touches another cluster on the same host.

TESTBED_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd "$TESTBED_DIR/.." && pwd)

PROFILE=${PROFILE:-edge-lab}
K8S_VERSION=${K8S_VERSION:-v1.35.1}
MULTUS_VERSION=${MULTUS_VERSION:-v4.3.2}

STATE_DIR=${STATE_DIR:-$HOME/edge-lab}
export KUBECONFIG=${EDGE_KUBECONFIG:-$STATE_DIR/kubeconfig}
KUBECTL=${KUBECTL:-$STATE_DIR/bin/kubectl}
k() { "$KUBECTL" "$@"; }

# Node roles: C = control plane (healthy relay), A = traffic source, B = traffic destination.
NODE_C=$PROFILE
NODE_A=$PROFILE-m02
NODE_B=$PROFILE-m03
NODES="$NODE_C $NODE_A $NODE_B"

# Second underlay network. Calico (primary) stays on eth0 (minikube's network);
# Flannel (backup) is bound to eth1 on this network, so the two paths share no link.
BACKUP_NET=${BACKUP_NET:-$PROFILE-backup}
BACKUP_SUBNET=${BACKUP_SUBNET:-192.168.200.0/24}
PRIMARY_IF=eth0
BACKUP_IF=eth1

# Images built from this repository.
IMG_AGENT=cni-health-agent:dev
IMG_ROUTER=cni-link-router:dev
IMG_IOTSIM=edge-iotsim:dev

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# IPv4 address of a node container on a given underlay interface.
node_ip() { docker exec "$1" ip -o -4 addr show dev "$2" | awk '{split($4, a, "/"); print a[1]; exit}'; }
