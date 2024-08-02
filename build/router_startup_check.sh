#!/bin/bash

OPERATOR_PASS=$(</etc/mysql/mysql-users-secret/operator)

if [[ $(curl -k -s -u operator:"${OPERATOR_PASS}" -o /dev/null -w %{http_code} https://localhost:8443/api/20190715/router/status) != 200 ]]; then
	echo "Router is not ready"
fi