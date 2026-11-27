#!/usr/bin/env bash
# Measure the resilience components' cost on an idle, healthy cluster.
#
#   testbed/overhead.sh [SECONDS]   (default 120)
#
# Per node: the health agent's traffic on its primary (eth0) and backup (net1)
# interfaces, which is its probes, the probes it answers and its API traffic, plus CPU
# and memory of the agent and the link router. Also the operator on the host.
# Read-only: it only reads /proc inside the node containers.
set -euo pipefail
. "$(dirname "$0")/env.sh"
SECS=${1:-120}
HZ=$(getconf CLK_TCK)

# pid of the newest process named $2 inside node $1
pid_in() { docker exec "$(nbox "$1")" pgrep -n -x "$2"; }

snap() {  # node pid -> "rx_eth0 tx_eth0 rx_net1 tx_net1 cpu_ticks rss_kb"
  docker exec "$(nbox "$1")" sh -c "
    awk '/eth0:|net1:/ {gsub(\":\",\" \"); print \$1, \$2, \$10}' /proc/$2/net/dev
    awk '{print \"cpu\", \$14 + \$15}' /proc/$2/stat
    awk '/VmRSS/ {print \"rss\", \$2}' /proc/$2/status"
}

cpu_rss() {  # node pid -> "cpu_ticks rss_kb"
  docker exec "$(nbox "$1")" sh -c "awk '{printf \"%d \", \$14 + \$15}' /proc/$2/stat; awk '/VmRSS/ {print \$2}' /proc/$2/status"
}

declare -A A0 R0 AP RP
for n in $NODES; do
  AP[$n]=$(pid_in "$n" healthagent)
  RP[$n]=$(pid_in "$n" linkrouter)
  A0[$n]=$(snap "$n" "${AP[$n]}")
  R0[$n]=$(cpu_rss "$n" "${RP[$n]}")
done
OP=$(pgrep -n -f "^[^ ]*operator/bin/manager ")
O0=$(awk '{print $14 + $15}' /proc/$OP/stat)
sleep "$SECS"

printf '%-14s %10s %10s %10s %10s %9s %8s %9s %8s\n' node eth0_rx_Bps eth0_tx_Bps net1_rx_Bps net1_tx_Bps agent_cpu agent_MB router_cpu router_MB
for n in $NODES; do
  A1=$(snap "$n" "${AP[$n]}")
  R1=$(cpu_rss "$n" "${RP[$n]}")
  python3 - "$n" "$SECS" "$HZ" "${A0[$n]}" "$A1" "${R0[$n]}" "$R1" <<'EOF'
import sys
node, secs, hz = sys.argv[1], float(sys.argv[2]), float(sys.argv[3])
def parse(s):
    d = {}
    for line in s.splitlines():
        f = line.split()
        if f[0] in ("eth0", "net1"):
            d[f[0]] = (int(f[1]), int(f[2]))
        else:
            d[f[0]] = int(f[1])
    return d
a0, a1 = parse(sys.argv[4]), parse(sys.argv[5])
r0, r1 = [int(x) for x in sys.argv[6].split()], [int(x) for x in sys.argv[7].split()]
rate = lambda i, k: (a1[i][k] - a0[i][k]) / secs
print("%-14s %10.0f %10.0f %10.0f %10.0f %8.2f%% %8.1f %8.2f%% %8.1f" % (
    node, rate("eth0", 0), rate("eth0", 1), rate("net1", 0), rate("net1", 1),
    100 * (a1["cpu"] - a0["cpu"]) / hz / secs, a1["rss"] / 1024,
    100 * (r1[0] - r0[0]) / hz / secs, r1[1] / 1024))
EOF
done
O1=$(awk '{print $14 + $15}' /proc/$OP/stat)
python3 -c "import sys; print('operator cpu %.2f%%, rss %.1f MB' % (100*($O1-$O0)/$HZ/$SECS, $(awk '/VmRSS/ {print $2}' /proc/$OP/status)/1024))"
