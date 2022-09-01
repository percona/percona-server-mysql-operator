#!/bin/bash

set -o errexit
set -o xtrace

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /ps-entrypoint.sh /opt/percona/ps-entrypoint.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /heartbeat-entrypoint.sh /opt/percona/heartbeat-entrypoint.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /bootstrap /opt/percona/bootstrap
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /healthcheck /opt/percona/healthcheck
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /sidecar /opt/percona/sidecar
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /peer-list /opt/percona/peer-list
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /run-backup.sh /opt/percona/run-backup.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /run-restore.sh /opt/percona/run-restore.sh

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy-entrypoint.sh /opt/percona/haproxy-entrypoint.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy_add_mysql_nodes.sh /opt/percona/haproxy_add_mysql_nodes.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy_check_primary.sh /opt/percona/haproxy_check_primary.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy_check_replicas.sh /opt/percona/haproxy_check_replicas.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy_liveness_check.sh /opt/percona/haproxy_liveness_check.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy_readiness_check.sh /opt/percona/haproxy_readiness_check.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy.cfg /opt/percona/haproxy.cfg
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /haproxy-global.cfg /opt/percona/haproxy-global.cfg
