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
#
# Link profile: with LINK_PROFILE set, every node pair gets background link conditions
# on both links (delay, jitter, loss, rate), faults are applied on top of them (a fault's
# loss or delay replaces the profile's), and `clear` restores the profile instead of
# clean links. `fault.sh profile` applies it alone. Profiles:
#   edge   primary: 5 ms +- 2 ms one way; backup: 20 ms +- 10 ms, 1 % loss, 10 Mbit/s
set -euo pipefail
. "$(dirname "$0")/env.sh"

TAG=edge-fault
LINK_PROFILE=${LINK_PROFILE:-}
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

# The profile's netem parameters for a link, as "delay|loss|rate" (empty: none).
profile_parts() {
  case "$LINK_PROFILE:$1" in
    :*) echo "||" ;;
    edge:primary) echo "delay 5ms 2ms distribution normal||" ;;
    edge:backup) echo "delay 20ms 10ms distribution normal|loss 1%|rate 10mbit" ;;
    *) die "unknown link profile '$LINK_PROFILE'" ;;
  esac
}

# netem spec for link $1 with fault spec $2 ("loss N%", "delay Nms" or empty): the
# fault's loss or delay replaces the profile's; the profile's other parts stay.
compose() {
  local link=$1 fault=${2:-} d l r spec
  IFS='|' read -r d l r <<<"$(profile_parts "$link")"
  case "$fault" in *loss*) l=${fault#*loss }; l="loss ${l%% *}" ;; esac
  case "$fault" in *delay*) d=${fault#*delay }; d="delay ${d%% *}" ;; esac
  spec=$(echo $d $l $r)
  echo "${spec:-delay 0ms}"
}

link_of() { if [ "$1" = "$PRIMARY_IF" ]; then echo primary; else echo backup; fi; }

# Root qdisc on device $1: band 1:4 carries A<->B (faults and the profile), band 1:5
# every other peer (profile only); normal traffic keeps bands 1-3.
root_cmd() { echo "tc qdisc replace dev $1 root handle 1: prio bands 5 priomap 1 2 2 2 1 2 0 0 1 1 1 1 1 1 1 1"; }

# netem on node $1, interface $2, for packets to the peer's address $3.
netem_to_peer() {
  local node=$1 dev=$2 peer=$3 spec
  spec=$(compose "$(link_of "$dev")" "$4")
  docker exec "$node" sh -c "
    tc qdisc show dev $dev | grep -q 'qdisc prio 1:' || $(root_cmd "$dev")
    tc qdisc replace dev $dev parent 1:4 handle 40: netem $spec
    tc filter show dev $dev parent 1: | grep -q 'flowid 1:4' ||
      tc filter add dev $dev parent 1: protocol ip prio 1 u32 match ip dst $peer/32 flowid 1:4"
}

# Background conditions on every node pair and link (no-op without LINK_PROFILE).
apply_profile() {
  [ -n "$LINK_PROFILE" ] || return 0
  local n m dev band spec cmds
  for n in $NODES; do
    for dev in $PRIMARY_IF $BACKUP_IF; do
      spec=$(compose "$(link_of "$dev")")
      cmds="tc qdisc del dev $dev root 2>/dev/null; $(root_cmd "$dev")
        tc qdisc replace dev $dev parent 1:4 handle 40: netem $spec
        tc qdisc replace dev $dev parent 1:5 handle 50: netem $spec"
      for m in $NODES; do
        [ "$m" = "$n" ] && continue
        band=5
        if { [ "$n" = "$NODE_A" ] && [ "$m" = "$NODE_B" ]; } || { [ "$n" = "$NODE_B" ] && [ "$m" = "$NODE_A" ]; }; then
          band=4
        fi
        cmds="$cmds
        tc filter add dev $dev parent 1: protocol ip prio 1 u32 match ip dst $(node_ip "$m" "$dev")/32 flowid 1:$band"
      done
      docker exec "$n" sh -c "$cmds"
    done
  done
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
  local links=$1 secs=$2 dev node up down
  apply_link "$links" "loss 100%"
  for dev in $(link_ifs "$links"); do
    up=$(compose "$(link_of "$dev")" "loss 0%")
    down=$(compose "$(link_of "$dev")" "loss 100%")
    for node in $NODE_A $([ "$ONEWAY" = 1 ] || echo "$NODE_B"); do
      docker exec -d "$node" sh -c "echo \$\$ > /run/$TAG-flap-$dev.pid
        while :; do sleep $secs; tc qdisc change dev $dev parent 1:4 handle 40: netem $up
                    sleep $secs; tc qdisc change dev $dev parent 1:4 handle 40: netem $down; done"
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
  apply_profile
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
  profile) apply_profile ;;
  status) status ;;
  *) sed -n '2,25p' "$0"; exit 2 ;;
esac
