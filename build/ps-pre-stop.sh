#!/bin/bash

set -e

if [ "${CLUSTER_TYPE}" == "async" ]; then
	exit 0
fi

LOG_FILE=/var/lib/mysql/pre-stop.log
NAMESPACE=$(</var/run/secrets/kubernetes.io/serviceaccount/namespace)
OPERATOR_PASSWORD=$(perl -MURI::Escape -e 'print uri_escape($ARGV[0]);' "$(</etc/mysql/mysql-users-secret/operator)")
FQDN="${HOSTNAME}.${SERVICE_NAME}.${NAMESPACE}"
POD_IP=$(hostname -I | awk '{print $1}')

echo "$(date +%Y-%m-%dT%H:%M:%S%Z): Removing ${FQDN} from cluster" >>${LOG_FILE}

mysqlsh --js -i \
  -h "${POD_IP}" -P 33062 \
  -u operator -p"${OPERATOR_PASSWORD}" \
  -e "dba.getCluster().removeInstance('${FQDN}:3306')" >>${LOG_FILE} 2>&1
