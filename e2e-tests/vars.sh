#!/bin/bash

export ROOT_REPO=${ROOT_REPO:-${PWD}}

export DEPLOY_DIR="${DEPLOY_DIR:-${ROOT_REPO}/deploy}"
export TESTS_DIR="${TESTS_DIR:-${ROOT_REPO}/e2e-tests}"
export TESTS_CONFIG_DIR="${TESTS_CONFIG_DIR:-${TESTS_DIR}/conf}"
export TEMP_DIR="/tmp/kuttl/ps/${test_name}"

export GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
export VERSION=${VERSION:-$(echo "${GIT_BRANCH}" | sed -e 's^/^-^g; s^[.]^-^g;' | tr '[:upper:]' '[:lower:]')}
export OPERATOR_VERSION="$(<realpath $(dirname ${BASH_SOURCE[0]})/../pkg/version/version.txt)"

export IMAGE=${IMAGE:-"perconalab/percona-server-mysql-operator:${VERSION}"}
if [[ -z ${MYSQL_VERSION-} && -n ${IMAGE_MYSQL-} ]]; then
	export MYSQL_VERSION=$(echo "$IMAGE_MYSQL" | sed -E 's/.*://; s/^[^0-9]*([0-9]+\.[0-9]+).*/\1/')
else
	export MYSQL_VERSION=${MYSQL_VERSION:-"8.4"}
fi
export IMAGE_MYSQL=${IMAGE_MYSQL:-"perconalab/percona-server-mysql-operator:main-psmysql${MYSQL_VERSION}"}
export IMAGE_BACKUP=${IMAGE_BACKUP:-"perconalab/percona-server-mysql-operator:main-backup${MYSQL_VERSION}"}
export IMAGE_ORCHESTRATOR=${IMAGE_ORCHESTRATOR:-"perconalab/percona-server-mysql-operator:main-orchestrator"}
export IMAGE_ROUTER=${IMAGE_ROUTER:-"perconalab/percona-server-mysql-operator:main-router${MYSQL_VERSION}"}
export IMAGE_TOOLKIT=${IMAGE_TOOLKIT:-"perconalab/percona-server-mysql-operator:main-toolkit"}
export IMAGE_HAPROXY=${IMAGE_HAPROXY:-"perconalab/percona-server-mysql-operator:main-haproxy"}
export PMM_SERVER_VERSION=${PMM_SERVER_VERSION:-"1.4.3"}
export IMAGE_PMM_CLIENT=${IMAGE_PMM_CLIENT:-"perconalab/pmm-client:3-dev-latest"}
export IMAGE_PMM_SERVER=${IMAGE_PMM_SERVER:-"perconalab/pmm-server:3-dev-latest"}
export CERT_MANAGER_VER="1.18.2"
export MINIO_VER="5.4.0"
export CHAOS_MESH_VER="2.7.2"
export VAULT_VER="0.16.1"

date=$(which gdate || which date)

oc get projects &> /dev/null && export OPENSHIFT=4 || :

if kubectl get nodes | grep "^minikube" >/dev/null; then
	export MINIKUBE=1
fi
