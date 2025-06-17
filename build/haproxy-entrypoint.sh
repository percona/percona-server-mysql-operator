#!/bin/bash

set -e
set -o xtrace

trap "exit" SIGTERM

log() {
	local message=$1
	local date=$(/usr/bin/date +"%d/%b/%Y:%H:%M:%S.%3N")

	echo "{\"time\":\"${date}\", \"message\": \"${message}\"}"
}

echo "${CLUSTER_TYPE}" >/tmp/cluster_type

if [ "$1" = 'haproxy' ]; then
  if [ ! -f '/etc/haproxy/mysql/haproxy.cfg' ]; then
    cp /opt/percona/haproxy.cfg /etc/haproxy/mysql
  fi

  custom_conf='/etc/haproxy-custom/haproxy.cfg'
  if [ -f "$custom_conf" ]; then
    log "haproxy -c -f $custom_conf -f /etc/haproxy/mysql/haproxy.cfg"
    haproxy -c -f $custom_conf -f /etc/haproxy/mysql/haproxy.cfg || EC=$?
    if [ -n "$EC" ]; then
      log "The custom config $custom_conf is not valid and will be ignored."
    fi
  fi

  haproxy_opt='-W -db '
  if [ -f "$custom_conf" -a -z "$EC" ]; then
    haproxy_opt+="-f $custom_conf "
  else
    haproxy_opt+='-f /opt/percona/haproxy-global.cfg '
  fi

  haproxy_opt+='-f /etc/haproxy/mysql/haproxy.cfg -p /etc/haproxy/mysql/haproxy.pid -S /etc/haproxy/mysql/haproxy-main.sock'

  if [ -f '/etc/haproxy/config/haproxy.cfg' ]; then
    haproxy_opt="${haproxy_opt} -f /etc/haproxy/config/haproxy.cfg"
  fi
fi

exec "$@" ${haproxy_opt}
