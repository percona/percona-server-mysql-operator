#!/bin/bash

set -e
set -o xtrace

echo "${CLUSTER_TYPE}" >/tmp/cluster_type

if [ "$1" = 'haproxy' ]; then
	if [ ! -f '/etc/haproxy/mysql/haproxy.cfg' ]; then
		cp /opt/percona/haproxy.cfg /etc/haproxy/mysql
	fi

	haproxy_opt='-W -db -f /opt/percona/haproxy-global.cfg -f /etc/haproxy/mysql/haproxy.cfg -p /etc/haproxy/mysql/haproxy.pid -S /etc/haproxy/mysql/haproxy-main.sock'

  if [ -f '/etc/haproxy/config/haproxy.cfg' ]; then
    haproxy_opt="${haproxy_opt} -f /etc/haproxy/config/haproxy.cfg"
  fi
fi

exec "$@" ${haproxy_opt}
