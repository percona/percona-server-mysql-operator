#!/bin/bash

set -e

ROUTER_DIR=${ROUTER_DIR:-/tmp/router}
OPERATOR_USER=${OPERATOR_USER:-operator}
NAMESPACE=$(</var/run/secrets/kubernetes.io/serviceaccount/namespace)

urlencode() {
  python3 -c 'import urllib.parse, sys; print(urllib.parse.quote(sys.argv[1]))' "$1"
}

if [ -f "/etc/mysql/mysql-users-secret/${OPERATOR_USER}" ]; then
	OPERATOR_PASS=$(<"/etc/mysql/mysql-users-secret/${OPERATOR_USER}")
	OPERATOR_PASS_ESCAPED=$(urlencode "$OPERATOR_PASS")
fi

mysqlrouter --force \
	--bootstrap "${OPERATOR_USER}:${OPERATOR_PASS_ESCAPED}@${MYSQL_SERVICE_NAME}-0.${MYSQL_SERVICE_NAME}.${NAMESPACE}.svc" \
	--conf-bind-address 0.0.0.0 \
	--conf-set-option http_auth_backend:default_auth_backend.backend=file \
	--conf-set-option http_auth_backend:default_auth_backend.filename="${ROUTER_DIR}/realm.txt" \
	--directory "${ROUTER_DIR}" \
	--logger.level=DEBUG

echo ${OPERATOR_PASS_ESCAPED} | mysqlrouter_passwd set "${ROUTER_DIR}/realm.txt" ${OPERATOR_USER}

sed -i 's/logging_folder=.*/logging_folder=/g' "${ROUTER_DIR}/mysqlrouter.conf"
sed -i "/\[logger\]/a destination=/dev/stdout" "${ROUTER_DIR}/mysqlrouter.conf"

cmd=("$@")

custom_conf=/etc/mysql/config/mysqlrouter.conf
if test -f "$custom_conf"; then
	cmd+=(--extra-config "$custom_conf")
fi

set -o xtrace

exec "${cmd[@]}"
