iptables -t nat -S POSTROUTING | grep -E -- '-j CNI-' | while IFS= read -r line; do
  eval "iptables -t nat ${line/-A/-D}"
done
for c in $(iptables -t nat -S | grep -o '^-N CNI-[A-Za-z0-9]*' | cut -c4-); do iptables -t nat -F $c; iptables -t nat -X $c; done
echo "remaining CNI- rules: $(iptables -t nat -S | grep -c 'CNI-')"
