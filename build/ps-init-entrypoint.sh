#!/bin/bash

set -o errexit
set -o xtrace

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /ps-entrypoint.sh /opt/percona/ps-entrypoint.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /heartbeat-entrypoint.sh /opt/percona/heartbeat-entrypoint.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /router-entrypoint.sh /opt/percona/router-entrypoint.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /orc-entrypoint.sh /opt/percona/orc-entrypoint.sh

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /bootstrap /opt/percona/bootstrap
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /healthcheck /opt/percona/healthcheck
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /sidecar /opt/percona/sidecar

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /run-backup.sh /opt/percona/run-backup.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /run-restore.sh /opt/percona/run-restore.sh
