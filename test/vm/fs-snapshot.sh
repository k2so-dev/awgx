#!/usr/bin/env bash
set -u
find /etc /usr/local /usr/src /var/lib/dkms /lib/modules /opt -xdev 2>/dev/null | grep -v -e '^/etc/ssh/ssh_host' -e '/etc/machine-id' | sort
dpkg-query -W -f '${Package} ${Status}\n' | awk '$4=="installed"{print "pkg:"$1}' | sort
systemctl list-unit-files --no-legend 2>/dev/null | awk '{print "unit:"$1" "$2}' | sort
ip -br link | awk '{print "link:"$1}' | sort
nft list tables 2>/dev/null | sed 's/^/nft:/' | sort
