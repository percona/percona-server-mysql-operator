#!/bin/bash

set -e

if [ ${CLUSTER_TYPE} == "async" ]; then
	exit 0
fi

LOG_FILE=/var/lib/mysql/pre-stop.log
NAMESPACE=$(</var/run/secrets/kubernetes.io/serviceaccount/namespace)
OPERATOR_PASSWORD=$(</etc/mysql/mysql-users-secret/operator)
FQDN="${HOSTNAME}.${SERVICE_NAME}.${NAMESPACE}"

echo "$(date +%Y-%m-%dT%H:%M:%S%Z): Removing ${FQDN} from cluster" >>${LOG_FILE}
mysqlsh -i -P 33062 -u operator -p${OPERATOR_PASSWORD} -e "dba.getCluster().removeInstance('${FQDN}:3306')" >>${LOG_FILE} 2>&1
