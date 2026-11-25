#!/usr/bin/env bash
# Deploy or remove the measurement workload.
#
#   testbed/workload.sh up [--no-gates]   targets on B, then clients with their addresses
#   testbed/workload.sh down
#
# --no-gates is for the vanilla baseline, where no operator sets the readiness gate.
set -euo pipefail
. "$(dirname "$0")/env.sh"
M=$TESTBED_DIR/manifests

down() {
  k delete -f "$M/clients.yaml" --ignore-not-found --wait=true --grace-period=5 >/dev/null 2>&1 || true
  k delete pod -l edge-testbed=client --ignore-not-found --wait=true >/dev/null
  k delete -f "$M/targets.yaml" --ignore-not-found --wait=true >/dev/null
}

up() {
  local gates='[{conditionType: cniresilience.cn-operator/primary-path}]'
  [ "${1:-}" = "--no-gates" ] && gates='[]'
  sed -e "s|__NODE_B__|$NODE_B|g" -e "s|__GATES__|$gates|g" "$M/targets.yaml" | k apply -f - >/dev/null
  k rollout status deploy/mqtt-broker deploy/web-target --timeout=180s >/dev/null 2>&1 ||
    { k rollout status deploy/mqtt-broker --timeout=120s >/dev/null && k rollout status deploy/web-target --timeout=120s >/dev/null; }
  local web_pod broker_pod web_svc broker_svc
  web_pod=$(k get pod -l app=web-target -o jsonpath='{.items[0].status.podIP}')
  broker_pod=$(k get pod -l app=mqtt-broker -o jsonpath='{.items[0].status.podIP}')
  web_svc=$(k get svc web-target -o jsonpath='{.spec.clusterIP}')
  broker_svc=$(k get svc mqtt-broker -o jsonpath='{.spec.clusterIP}')
  [[ $web_pod == 172.16.* && $broker_pod == 172.16.* ]] || die "Targets lack Calico addresses ($web_pod, $broker_pod)"
  k delete -f "$M/clients.yaml" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  sed -e "s|__NODE_A__|$NODE_A|g" -e "s|__NODE_B__|$NODE_B|g" -e "s|__IMG__|$IMG_IOTSIM|g" \
      -e "s|__WEB_POD_IP__|$web_pod|g" -e "s|__WEB_SVC_IP__|$web_svc|g" \
      -e "s|__BROKER_POD_IP__|$broker_pod|g" -e "s|__BROKER_SVC_IP__|$broker_svc|g" \
      "$M/clients.yaml" | k apply -f - >/dev/null
  k wait --for=condition=Ready pod -l edge-testbed=client --timeout=180s >/dev/null
  log "Workload up: web $web_pod/$web_svc, broker $broker_pod/$broker_svc, gates=$gates"
}

case "${1:-}" in
  up) shift; up "$@" ;;
  down) down ;;
  *) die "usage: $0 up [--no-gates] | down" ;;
esac
