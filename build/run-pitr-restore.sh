#!/bin/bash

set -e

# TODO: Add support for data at rest encryption

echo "Starting mysqld"
mysqld \
  --admin-address=127.0.0.1 \
  --skip-replica-start \
  --user=mysql \
  --read-only=ON \
  --super-read-only=ON \
  --gtid-mode=ON \
  --enforce-gtid-consistency=ON >/tmp/mysqld.log 2>&1 &

until mysqladmin -u operator -p$(</etc/mysql/mysql-users-secret/operator) ping --silent 2>/dev/null; do
    sleep 1;
done

/opt/percona/pitr setup
/opt/percona/pitr apply

echo "Stopping mysqld"
mysqladmin -u operator -p$(</etc/mysql/mysql-users-secret/operator) shutdown
