#!/bin/bash
# usage: hl.sh <ADD|DEL> <id> <conf-file>
export CNI_COMMAND=$1 CNI_CONTAINERID=$2 CNI_NETNS=/var/run/netns/x CNI_IFNAME=eth0 CNI_PATH=/opt/cni/bin
/opt/cni/bin/host-local < $3
