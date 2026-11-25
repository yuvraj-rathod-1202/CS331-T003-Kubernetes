#!/usr/bin/env bash
# Inject or clear faults between node A (traffic source) and node B (destination).
# Faults are applied only inside the testbed's node containers, never on the host.
#
# Link faults act on the underlay with tc netem, per direction and per peer address:
#   fault.sh link-cut   [primary|backup|both]   drop every packet A<->B on that link
#   fault.sh link-loss  PCT  [primary|backup]   random loss, e.g. 30
#   fault.sh link-delay MS   [primary|backup]   extra one-way delay, e.g. 200
#   fault.sh link-flap  SECS [primary|backup]   link down SECS, up SECS, repeating
# CNI faults act on the overlay with iptables, like the original experiments:
#   fault.sh overlay-cut                        drop Calico IPIP A<->B (L1)
#   fault.sh node-cni                           B drops all forwarded Calico pod traffic
# Other:
#   fault.sh clear                              remove every fault this script added
#   fault.sh status                             show active faults
# Add --oneway after the arguments to impair only A->B for link faults.
set -euo pipefail
. "$(dirname "$0")/env.sh"

TAG=edge-fault
ONEWAY=0
args=()
for a in "$@"; do [ "$a" = "--oneway" ] && ONEWAY=1 || args+=("$a"); done
set -- "${args[@]}"

link_ifs() {
  case "${1:-primary}" in
    primary) echo "$PRIMARY_IF" ;;
    backup) echo "$BACKUP_IF" ;;
    both) echo "$PRIMARY_IF $BACKUP_IF" ;;
    *) die "link must be primary, backup or both" ;;
  esac
}

# netem on node $1, interface $2, for packets to the peer's address $3.
netem_to_peer() {
  local node=$1 dev=$2 peer=$3 spec=$4
  docker exec "$node" sh -c "
    tc qdisc show dev $dev | grep -q 'qdisc prio 1:' ||
      tc qdisc replace dev $dev root handle 1: prio bands 4 priomap 1 2 2 2 1 2 0 0 1 1 1 1 1 1 1 1
    tc qdisc replace dev $dev parent 1:4 handle 40: netem $spec
    tc filter show dev $dev parent 1: | grep -q 'flowid 1:4' ||
      tc filter add dev $dev parent 1: protocol ip prio 1 u32 match ip dst $peer/32 flowid 1:4"
}

# Apply netem $2 on every selected link in both directions (or A->B only).
apply_link() {
  local links=$1 spec=$2 dev
  for dev in $(link_ifs "$links"); do
    netem_to_peer "$NODE_A" "$dev" "$(node_ip "$NODE_B" "$dev")" "$spec"
    [ "$ONEWAY" = 1 ] || netem_to_peer "$NODE_B" "$dev" "$(node_ip "$NODE_A" "$dev")" "$spec"
  done
}

start_flap() {
  local links=$1 secs=$2 dev node
  apply_link "$links" "loss 100%"
  for dev in $(link_ifs "$links"); do
    for node in $NODE_A $([ "$ONEWAY" = 1 ] || echo "$NODE_B"); do
      docker exec -d "$node" sh -c "echo \$\$ > /run/$TAG-flap-$dev.pid
        while :; do sleep $secs; tc qdisc change dev $dev parent 1:4 handle 40: netem loss 0%
                    sleep $secs; tc qdisc change dev $dev parent 1:4 handle 40: netem loss 100%; done"
    done
  done
}

clear_all() {
  local n dev
  for n in $NODES; do
    docker exec "$n" sh -c "
      for f in /run/$TAG-flap-*.pid; do [ -f \"\$f\" ] && kill \$(cat \"\$f\") 2>/dev/null; rm -f \"\$f\"; done
      for dev in $PRIMARY_IF $BACKUP_IF; do
        tc qdisc show dev \$dev 2>/dev/null | grep -q 'qdisc prio 1:' && tc qdisc del dev \$dev root
      done
      for t in filter raw; do
        iptables -t \$t -S | grep -- '$TAG' | sed 's/^-A/-D/' | while read -r r; do iptables -t \$t \$r; done
      done
      true"
  done
}

status() {
  local n dev
  for n in $NODES; do
    for dev in $PRIMARY_IF $BACKUP_IF; do
      docker exec "$n" tc qdisc show dev "$dev" | grep netem | sed "s/^/$n $dev: /" || true
    done
    docker exec "$n" sh -c "iptables -S; iptables -t raw -S" | grep -- "$TAG" | sed "s/^/$n: /" || true
  done
}

case "${1:-}" in
  link-cut) apply_link "${2:-primary}" "loss 100%" ;;
  link-loss) apply_link "${3:-primary}" "loss ${2:?percent}%" ;;
  link-delay) apply_link "${3:-primary}" "delay ${2:?ms}ms" ;;
  link-flap) start_flap "${3:-primary}" "${2:?seconds}" ;;
  overlay-cut)
    docker exec "$NODE_A" iptables -t raw -I PREROUTING -p 4 -s "$(node_ip "$NODE_B" "$PRIMARY_IF")" -j DROP -m comment --comment "$TAG"
    docker exec "$NODE_B" iptables -t raw -I PREROUTING -p 4 -s "$(node_ip "$NODE_A" "$PRIMARY_IF")" -j DROP -m comment --comment "$TAG"
    ;;
  node-cni) docker exec "$NODE_B" iptables -I FORWARD -s 172.16.0.0/16 -j DROP -m comment --comment "$TAG" ;;
  clear) clear_all ;;
  status) status ;;
  *) sed -n '2,19p' "$0"; exit 2 ;;
esac
