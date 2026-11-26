#!/usr/bin/env bash
# Run a measurement campaign: every configuration x fault x repetition.
#
#   testbed/run.sh <campaign-dir> [--reps N] [--configs "vanilla node full full-norelay"]
#                  [--faults "link-cut ..."] [--steady S] [--fault-secs S] [--recover S]
#                  [--fresh-clients] [--probe-interval S]
#
# Configurations
#   vanilla       stock Kubernetes: no operator, no health agent, no link router, no gates
#   node          node-level failover only (autoFailover on, linkFailover off)
#   full          node-level failover + per-link steering + relay
#   full-norelay  as full, relay off (only run with link-cut-both by default)
# Faults (see fault.sh)
#   link-cut link-cut-both link-loss30 link-flap overlay-cut node-cni
#
# Each run: wait until healthy, STEADY s of normal traffic, inject, FAULT s, clear,
# RECOVER s, then save logs and meta.json to <campaign>/<config>/<fault>/repNN/.
# Faults rotate inside each repetition so slow drifts do not line up with one fault.
#
# --fresh-clients recreates the client pods before every run. Without it, MQTT
# connections live across runs, and a connection opened during a node failover stays
# DNAT'ed to the broker's backup-network address after failback (conntrack keeps the
# old translation). Later runs then measure a connection that is not on the primary path.
set -euo pipefail
. "$(dirname "$0")/env.sh"

CAMPAIGN=${1:?usage: run.sh <campaign-dir> [options]}; shift
REPS=10
CONFIGS="vanilla node full full-norelay"
FAULTS="link-cut link-cut-both link-loss30 link-flap overlay-cut node-cni"
STEADY=20 FAULT_SECS=60 RECOVER=45
FRESH_CLIENTS=0
PROBE_INTERVAL=2  # seconds between probe rounds (Delta); the agents read it from the CR
while [ $# -gt 0 ]; do
  case "$1" in
    --reps) REPS=$2; shift 2 ;;
    --configs) CONFIGS=$2; shift 2 ;;
    --faults) FAULTS=$2; shift 2 ;;
    --steady) STEADY=$2; shift 2 ;;
    --fault-secs) FAULT_SECS=$2; shift 2 ;;
    --recover) RECOVER=$2; shift 2 ;;
    --fresh-clients) FRESH_CLIENTS=1; shift ;;
    --probe-interval) PROBE_INTERVAL=$2; shift 2 ;;
    *) die "unknown option $1" ;;
  esac
done
mkdir -p "$CAMPAIGN"
CAMPAIGN=$(cd "$CAMPAIGN" && pwd)
exec > >(tee -a "$CAMPAIGN/run.log") 2>&1

now_ns() { date +%s%N; }
rfc3339() { date -u -d "@$(( $1 / 1000000000 ))" +%Y-%m-%dT%H:%M:%SZ; }

fault_cmd() {
  case "$1" in
    link-cut) echo "link-cut primary" ;;
    link-cut-both) echo "link-cut both" ;;
    link-loss30) echo "link-loss 30 primary" ;;
    link-flap) echo "link-flap 5 primary" ;;
    overlay-cut) echo "overlay-cut" ;;
    node-cni) echo "node-cni" ;;
    *) die "unknown fault $1" ;;
  esac
}

faults_for() {
  if [ "$1" = full-norelay ] && [ -z "${FAULTS_OVERRIDDEN:-}" ]; then echo link-cut-both; else echo "$FAULTS"; fi
}
case " $* " in *" --faults "*) FAULTS_OVERRIDDEN=1 ;; esac

remove_routes() {
  for n in $NODES; do
    docker exec "$n" sh -c 'for p in 90 91 100; do while ip rule del pref $p 2>/dev/null; do :; done; done
                            ip route flush table 200 2>/dev/null; true'
  done
}

set_cr() {  # autoFailover linkFailover linkRelay
  k apply -f "$REPO_DIR/operator/config/samples/cniresilience_sample.yaml" >/dev/null
  k patch cniresilience default-resilience --type=merge \
    -p "{\"spec\":{\"autoFailover\":$1,\"linkFailover\":$2,\"linkRelay\":$3,\"probeIntervalSeconds\":$PROBE_INTERVAL}}" >/dev/null
}

apply_config() {
  log "=== Config $1"
  "$TESTBED_DIR/fault.sh" clear
  "$TESTBED_DIR/workload.sh" down
  k taint nodes --all cniresilience.cn-operator/recovering- >/dev/null 2>&1 || true
  case "$1" in
    vanilla)
      "$TESTBED_DIR/operator.sh" stop
      k delete cniresilience --all --ignore-not-found >/dev/null
      k -n kube-system delete ds cni-link-router cni-health-agent --ignore-not-found --wait=true >/dev/null
      remove_routes
      "$TESTBED_DIR/workload.sh" up --no-gates
      ;;
    node|full|full-norelay)
      case "$1" in
        node) set_cr true false false ;;
        full) set_cr true true true ;;
        full-norelay) set_cr true true false ;;
      esac
      k apply -f "$REPO_DIR/operator/deploy/healthagent-daemonset.yaml" >/dev/null
      k apply -f "$REPO_DIR/operator/deploy/linkrouter-daemonset.yaml" >/dev/null
      k -n kube-system rollout status ds/cni-health-agent --timeout=180s >/dev/null
      k -n kube-system rollout status ds/cni-link-router --timeout=180s >/dev/null
      "$TESTBED_DIR/operator.sh" start
      "$TESTBED_DIR/workload.sh" up
      ;;
    *) die "unknown config $1" ;;
  esac
  sleep 15
}

# Healthy = no active fault, operator (if any) reports Healthy, no steering routes,
# and the last 3 s of Service probes all succeeded.
wait_healthy() {
  local config=$1 deadline=$(( $(date +%s) + 180 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    local ok=1
    if [ "$config" != vanilla ]; then
      [ "$(k get cniresilience default-resilience -o jsonpath='{.status.phase}' 2>/dev/null)" = Healthy ] || ok=0
      for n in $NODE_A $NODE_B; do
        [ -z "$(docker exec "$n" ip route show table 200 2>/dev/null)" ] || ok=0
      done
    fi
    local lines bad
    lines=$(k logs http-prober -c http-svc --since=3s 2>/dev/null | wc -l)
    bad=$(k logs http-prober -c http-svc --since=3s 2>/dev/null | awk '$4 != 200' | wc -l)
    [ "$lines" -ge 20 ] && [ "$bad" -eq 0 ] || ok=0
    [ "$ok" = 1 ] && return 0
    sleep 2
  done
  return 1
}

collect() {  # dir since
  local dir=$1 since=$2 pod
  { k logs http-prober -c http-pod --since-time="$since"; k logs http-prober -c http-svc --since-time="$since"; } > "$dir/http.log" 2>/dev/null || true
  k logs iot-sensors -c pub-q1 --since-time="$since" > "$dir/pub-q1.log" 2>/dev/null || true
  k logs iot-sensors -c pub-q0 --since-time="$since" > "$dir/pub-q0.log" 2>/dev/null || true
  k logs iot-sub --since-time="$since" > "$dir/sub.log" 2>/dev/null || true
  for side in src:$NODE_A dst:$NODE_B; do
    pod=$(k -n kube-system get pod -l app=cni-link-router --field-selector "spec.nodeName=${side#*:}" -o name 2>/dev/null | head -1)
    [ -n "$pod" ] && k -n kube-system logs --timestamps "$pod" --since-time="$since" > "$dir/router-${side%%:*}.log" 2>/dev/null || true
  done
  k get cniresilience default-resilience -o json > "$dir/cr.json" 2>/dev/null || true
  k get events --field-selector involvedObject.kind=CNIResilience -o json > "$dir/events.json" 2>/dev/null || true
  k get pods -l edge-testbed -o custom-columns='NAME:.metadata.name,RESTARTS:.status.containerStatuses[*].restartCount' --no-headers > "$dir/restarts.txt" 2>/dev/null || true
}

run_one() {  # config fault rep
  local config=$1 fault=$2 rep=$3 dir healthy=true
  dir=$CAMPAIGN/$config/$fault/$(printf 'rep%02d' "$rep")
  if [ -f "$dir/meta.json" ]; then log "skip $config/$fault/rep$rep (done)"; return; fi
  mkdir -p "$dir"
  "$TESTBED_DIR/fault.sh" clear
  if [ "$FRESH_CLIENTS" = 1 ]; then
    if [ "$config" = vanilla ]; then "$TESTBED_DIR/workload.sh" up --no-gates; else "$TESTBED_DIR/workload.sh" up; fi
  fi
  wait_healthy "$config" || { healthy=false; log "WARN not healthy before $config/$fault/rep$rep"; }
  local t_start t_fault t_clear t_end
  t_start=$(now_ns); sleep "$STEADY"
  t_fault=$(now_ns)
  # shellcheck disable=SC2046
  "$TESTBED_DIR/fault.sh" $(fault_cmd "$fault")
  sleep "$FAULT_SECS"
  t_clear=$(now_ns)
  "$TESTBED_DIR/fault.sh" clear
  sleep "$RECOVER"
  t_end=$(now_ns)
  collect "$dir" "$(rfc3339 $(( t_start - 2000000000 )))"
  cat > "$dir/meta.json" <<EOF
{"config": "$config", "fault": "$fault", "rep": $rep, "t_start_ns": $t_start, "t_fault_ns": $t_fault,
 "t_clear_ns": $t_clear, "t_end_ns": $t_end, "src_node": "$NODE_A", "dst_node": "$NODE_B",
 "fault_cmd": "$(fault_cmd "$fault")", "healthy_before": $healthy, "fresh_clients": $FRESH_CLIENTS, "probe_interval_s": $PROBE_INTERVAL,
 "steady_s": $STEADY, "fault_s": $FAULT_SECS, "recover_s": $RECOVER}
EOF
  log "done $config/$fault/rep$rep"
}

trap '"$TESTBED_DIR/fault.sh" clear' EXIT
log "Campaign $CAMPAIGN: configs=[$CONFIGS] faults=[$FAULTS] reps=$REPS fresh_clients=$FRESH_CLIENTS probe_interval=$PROBE_INTERVAL"
git -C "$REPO_DIR" log --oneline -1 > "$CAMPAIGN/commit.txt" 2>/dev/null || true
for config in $CONFIGS; do
  apply_config "$config"
  for rep in $(seq 1 "$REPS"); do
    for fault in $(faults_for "$config"); do
      run_one "$config" "$fault" "$rep"
    done
  done
done
log "Campaign finished"
