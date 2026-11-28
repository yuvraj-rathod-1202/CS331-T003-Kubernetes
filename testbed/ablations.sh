#!/usr/bin/env bash
# The two experiments behind statements outside the main campaign:
#   pinning/      connections opened during node-level failover stay on the backup
#                 network: node CNI fault, then both links cut, clients NOT recreated
#                 between runs (Limitations, "Connections stay on the failover path")
#   nat-default/  kube-proxy left with its default pod range (the cluster's, the backup
#                 network's), so Service traffic from Calico pods is masqueraded:
#                 primary link cut, full system (Section 3.4, "Source NAT")
# The second one rebuilds the cluster twice (without, then again with, kube-proxy's
# pod-range setting), so run it only when no campaign is running. K3s only.
#
#   DISTRO=k3s testbed/ablations.sh <results-dir> [--reps N]
set -u
export DISTRO=k3s
. "$(dirname "$0")/env.sh"
R=${1:?usage: ablations.sh <results-dir> [--reps N]}; shift
REPS=5
[ "${1:-}" = "--reps" ] && REPS=$2
mkdir -p "$R"; R=$(cd "$R" && pwd)
RUN="$TESTBED_DIR/run.sh"

log "=== Pinning: node CNI fault then both links cut, persistent clients"
"$RUN" "$R/pinning" --reps "$REPS" --configs full --faults "node-cni link-cut-both" ||
  log "WARN pinning failed"

log "=== Source NAT: rebuild with kube-proxy's default pod range"
"$TESTBED_DIR/bootstrap-k3s.sh" --delete
KUBE_PROXY_POD_CIDR= "$TESTBED_DIR/bootstrap-k3s.sh" || die "bootstrap without pod range failed"
"$RUN" "$R/nat-default" --reps "$REPS" --configs full --faults link-cut --fresh-clients ||
  log "WARN nat-default failed"

log "=== Restoring the testbed (kube-proxy with Calico's pod range)"
"$TESTBED_DIR/bootstrap-k3s.sh" --delete
"$TESTBED_DIR/bootstrap-k3s.sh" || die "restoring bootstrap failed"
log "=== Ablations finished"
