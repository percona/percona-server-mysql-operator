#!/bin/bash

set -e

/opt/percona/pitr setup
mysqld --skip-replica-start --user=mysql &
until mysqladmin ping --silent 2>/dev/null; do sleep 1; done
/opt/percona/pitr apply
mysqladmin shutdown
