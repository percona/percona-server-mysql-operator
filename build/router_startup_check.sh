#!/bin/bash

urlencode() {
  python3 -c 'import urllib.parse, sys; print(urllib.parse.quote(sys.argv[1]))' "$1"
}

OPERATOR_PASS=$(</etc/mysql/mysql-users-secret/operator)
OPERATOR_PASS_ESCAPED=$(urlencode "$OPERATOR_PASS")

if [[ $(curl -k -s -u operator:"${OPERATOR_PASS_ESCAPED}" -o /dev/null -w %{http_code} https://localhost:8443/api/20190715/router/status) != 200 ]]; then
	echo "Router is not ready"
fi
