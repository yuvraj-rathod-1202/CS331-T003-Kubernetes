# usage: setup-node.sh <blockA> <blockB> <peer routes: "cidr@ip ...">
A=$1; B=$2; shift 2
rm -f /etc/cni/net.d/87-podman-bridge.conflist
ip link del cni-podman0 2>/dev/null
cat > /etc/cni/net.d/10-buddy.conflist <<JSON
{"cniVersion":"0.4.0","name":"buddy","plugins":[
 {"type":"ptp","ipMasq":false,"ipam":{"type":"host-local","dataDir":"/var/lib/cni/networks",
   "ranges":[[{"subnet":"$A"},{"subnet":"$B"}]],"routes":[{"dst":"0.0.0.0/0"}]}},
 {"type":"portmap","capabilities":{"portMappings":true}}]}
JSON
for r in "$@"; do ip route replace ${r%@*} via ${r#*@}; done
ip route | grep -E "10\.(244|200)"
