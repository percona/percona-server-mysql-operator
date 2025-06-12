#!/bin/bash


urlencode() {
  python3 -c 'import urllib.parse, sys; print(urllib.parse.quote(sys.argv[1]))' "$1"
}

OPERATOR_PASS=$(</etc/mysql/mysql-users-secret/operator)
OPERATOR_PASS_ESCAPED=$(urlencode "$OPERATOR_PASS")

if ! curl -k -s -u operator:"${OPERATOR_PASS_ESCAPED}" https://localhost:8443/api/20190715/routes/bootstrap_rw/health | grep true; then
	echo "Read-write route is not healthy"
	exit 1
fi

if ! curl -k -s -u operator:"${OPERATOR_PASS_ESCAPED}" https://localhost:8443/api/20190715/routes/bootstrap_ro/health | grep true; then
	echo "Read-only route is not healthy"
	exit 1
fi

exit 0
