#!/bin/bash

set -e
set -o xtrace

if [ "$1" = 'haproxy' ]; then
	if [ ! -f '/etc/haproxy/mysql/haproxy.cfg' ]; then
		cp /opt/percona/haproxy.cfg /etc/haproxy/mysql
	fi

	haproxy_opt='-W -db -f /opt/percona/haproxy-global.cfg -f /etc/haproxy/mysql/haproxy.cfg -p /etc/haproxy/mysql/haproxy.pid -S /etc/haproxy/mysql/haproxy-main.sock'
fi

exec "$@" "${haproxy_opt}"
