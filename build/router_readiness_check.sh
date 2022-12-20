#!/bin/bash

OPERATOR_PASS=$(</etc/mysql/mysql-users-secret/operator)

if ! curl -k -s -u operator:${OPERATOR_PASS} https://localhost:8443/api/20190715/routes/bootstrap_rw/health | grep true; then
    echo "Read-write route is not healthy"
    exit 1
fi

if ! curl -k -s -u operator:${OPERATOR_PASS} https://localhost:8443/api/20190715/routes/bootstrap_ro/health | grep true; then
    echo "Read-only route is not healthy"
    exit 1
fi

exit 0