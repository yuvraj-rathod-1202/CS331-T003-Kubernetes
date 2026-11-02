rm -rf /tmp/hltest
echo "== ADD x25 with two /28 ranges in ONE inner array (28 usable if overhead=0..)"
for i in $(seq 1 25); do echo -n "$i: "; bash /tmp/hl.sh ADD c$i /tmp/conf2.json | grep -o "\"address\": *\"[^\"]*\"" | tr '\n' ' '; echo; done
echo "== lease files"; ls /tmp/hltest/t | tr '\n' ' '; echo
echo "== now remove 2nd range from config, DEL a pod that lives in 2nd block"
for i in 20 21; do bash /tmp/hl.sh DEL c$i /tmp/conf1.json; echo "DEL c$i rc=$?"; done
echo "== lease files after"; ls /tmp/hltest/t | tr '\n' ' '; echo
