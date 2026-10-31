echo "== state from before: leases up to .77, c20/c21 (.72,.73) freed. Free c1..c3 (.2-.4) then ADD 3 more"
for i in 1 2 3; do bash /tmp/hl.sh DEL c$i /tmp/conf2.json >/dev/null; done
for i in 31 32 33; do echo -n "c$i: "; bash /tmp/hl.sh ADD c$i /tmp/conf2.json | grep -o '"address": *"[^"]*"'; done
echo "== wrap-around: fill rest of second block then ADD again"
for i in $(seq 40 60); do echo -n "c$i: "; bash /tmp/hl.sh ADD c$i /tmp/conf2.json 2>&1 | grep -o '"address": *"[^"]*"\|"msg".*' ; done | tail -12

try() { # name json
 rm -rf /tmp/hltest2; echo "== $1"
 echo "$2" > /tmp/c.json
 for i in 1 2 3; do echo -n " ADD: "; bash /tmp/hl.sh ADD x$i /tmp/c.json 2>&1 | tr -d '\n ' | grep -o '"address":"[^"]*"\|"msg":"[^"]*"'; echo; done
}
try "rangeStart .1 rangeEnd .15 (no gateway)" '{"cniVersion":"0.4.0","name":"t","ipam":{"type":"host-local","dataDir":"/tmp/hltest2","ranges":[[{"subnet":"10.244.1.0/28","rangeStart":"10.244.1.0","rangeEnd":"10.244.1.15"}]]}}'
try "rangeStart .1 + explicit gateway .1" '{"cniVersion":"0.4.0","name":"t","ipam":{"type":"host-local","dataDir":"/tmp/hltest2","ranges":[[{"subnet":"10.244.1.0/28","rangeStart":"10.244.1.1","rangeEnd":"10.244.1.14","gateway":"10.244.1.1"}]]}}'
try "gateway outside subnet (169.254.1.1)" '{"cniVersion":"0.4.0","name":"t","ipam":{"type":"host-local","dataDir":"/tmp/hltest2","ranges":[[{"subnet":"10.244.1.0/28","gateway":"169.254.1.1"}]]}}'
