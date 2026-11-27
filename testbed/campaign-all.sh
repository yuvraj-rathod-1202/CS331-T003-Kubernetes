#!/usr/bin/env bash
# Run the paper's whole evaluation into one results directory, in this order:
#   main/          4 configurations x 6 faults (full-norelay: both links cut only)
#   profile-edge/  full system under the "edge" link profile (realistic links)
#   sweep-d1/, sweep-d4/   probe interval 1 s and 4 s, primary link cut
#   overhead-d{1,2,4}.txt  component cost on the healthy cluster at each interval
#   rp-strict/     full system, 30 % loss, with strict reverse-path filtering on the
#                  tunnel devices (the ablation of Section 3.4)
# Every run recreates the client pods first (--fresh-clients). Resumable: run.sh skips
# runs whose meta.json exists.
#
#   [DISTRO=k3s] testbed/campaign-all.sh <results-dir> [--reps N]
set -u
. "$(dirname "$0")/env.sh"
R=${1:?usage: campaign-all.sh <results-dir> [--reps N]}; shift
REPS=10
[ "${1:-}" = "--reps" ] && REPS=$2
mkdir -p "$R"; R=$(cd "$R" && pwd)
RUN="$TESTBED_DIR/run.sh"

set_rp() {  # value for tunl0 and flannel.1 on every node (the router sets 2 at start)
  for n in $NODES; do
    docker exec "$n" sh -c "for d in tunl0 flannel.1; do echo $1 > /proc/sys/net/ipv4/conf/\$d/rp_filter; done"
  done
}

log "=== Full evaluation into $R (distro $DISTRO, $REPS reps)"
"$RUN" "$R/main" --reps "$REPS" --fresh-clients || log "WARN main campaign failed"
"$RUN" "$R/profile-edge" --reps "$REPS" --configs full \
  --faults "link-cut link-cut-both link-loss30" --fresh-clients --link-profile edge ||
  log "WARN profile campaign failed"
for d in 1 4; do
  "$RUN" "$R/sweep-d$d" --reps "$REPS" --configs full --faults link-cut --fresh-clients \
    --probe-interval "$d" || log "WARN sweep d$d failed"
done
for d in 1 2 4; do
  k patch cniresilience default-resilience --type=merge -p "{\"spec\":{\"probeIntervalSeconds\":$d}}" >/dev/null
  sleep 40
  { echo "=== overhead delta=$d"; "$TESTBED_DIR/overhead.sh" 120; } > "$R/overhead-d$d.txt" 2>&1 ||
    log "WARN overhead d$d failed"
done
k patch cniresilience default-resilience --type=merge -p '{"spec":{"probeIntervalSeconds":2}}' >/dev/null

# Ablation: strict reverse-path filtering on the tunnels. run.sh keeps the DaemonSets,
# so the value set here stays until the router pods restart; restore loose afterwards.
log "=== rp_filter ablation (strict on tunl0 and flannel.1)"
set_rp 1
"$RUN" "$R/rp-strict" --reps "$REPS" --configs full --faults link-loss30 --fresh-clients ||
  log "WARN rp-strict failed"
set_rp 2
log "=== Full evaluation finished"
