#!/usr/bin/env bash
# Run the CNIResilience operator on the host, outside the cluster, in a tmux session.
#
#   testbed/operator.sh start | stop | status
set -euo pipefail
. "$(dirname "$0")/env.sh"

SESSION=${PROFILE}-operator
LOG=$STATE_DIR/operator.log

case "${1:-status}" in
  start)
    if tmux has-session -t "$SESSION" 2>/dev/null; then log "Operator already running"; exit 0; fi
    [ -x "$REPO_DIR/operator/bin/manager" ] || die "operator/bin/manager missing; run testbed/build.sh"
    mkdir -p "$STATE_DIR"; touch "$LOG"
    before=$(wc -l < "$LOG")
    tmux new-session -d -s "$SESSION" \
      "KUBECONFIG='$KUBECONFIG' '$REPO_DIR/operator/bin/manager' --controllers=cniresilience \
       --metrics-bind-address=0 --health-probe-bind-address=127.0.0.1:18081 >>'$LOG' 2>&1"
    for _ in $(seq 1 30); do
      tail -n "+$((before + 1))" "$LOG" | grep -q "Starting workers" && { log "Operator started (log: $LOG)"; exit 0; }
      sleep 1
    done
    die "Operator did not start; see $LOG"
    ;;
  stop)
    tmux kill-session -t "$SESSION" 2>/dev/null && log "Operator stopped" || log "Operator not running"
    ;;
  status)
    tmux has-session -t "$SESSION" 2>/dev/null && echo running || echo stopped
    ;;
  *) die "usage: $0 start|stop|status" ;;
esac
