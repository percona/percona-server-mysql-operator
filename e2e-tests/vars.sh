#!/bin/bash

export ROOT_REPO=${ROOT_REPO:-${PWD}}

export DEPLOY_DIR="${DEPLOY_DIR:-${ROOT_REPO}/deploy}"
export TESTS_DIR="${TESTS_DIR:-${ROOT_REPO}/e2e-tests}"
export TESTS_CONFIG_DIR="${TESTS_CONFIG_DIR:-${TESTS_DIR}/conf}"
export TEMP_DIR="/tmp/kuttl/ps/${test_name}"

export GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
export VERSION=${VERSION:-$(echo "${GIT_BRANCH}" | sed -e 's^/^-^g; s^[.]^-^g;' | tr '[:upper:]' '[:lower:]')}

export IMAGE=${IMAGE:-"perconalab/percona-server-mysql-operator:${VERSION}"}
export IMAGE_MYSQL=${IMAGE_MYSQL:-"percona/percona-server:8.4"}
export IMAGE_BACKUP=${IMAGE_BACKUP:-"percona/percona-xtrabackup:8.4"}
export IMAGE_ORCHESTRATOR=${IMAGE_ORCHESTRATOR:-"perconalab/percona-server-mysql-operator:main-orchestrator"}
export IMAGE_ROUTER=${IMAGE_ROUTER:-"percona/percona-mysql-router:8.4"}
export IMAGE_TOOLKIT=${IMAGE_TOOLKIT:-"perconalab/percona-server-mysql-operator:main-toolkit"}
export IMAGE_HAPROXY=${IMAGE_HAPROXY:-"perconalab/percona-server-mysql-operator:main-haproxy"}
export PMM_SERVER_VERSION=${PMM_SERVER_VERSION:-"9.9.9"}
export IMAGE_PMM_CLIENT=${IMAGE_PMM_CLIENT:-"perconalab/pmm-client:dev-latest"}
export IMAGE_PMM_SERVER=${IMAGE_PMM_SERVER:-"perconalab/pmm-server:dev-latest"}
export CERT_MANAGER_VER="1.15.1"

date=$(which gdate || which date)

if command -v oc &> /dev/null; then
	if oc get projects; then
		export OPENSHIFT=4
	fi
fi

if kubectl get nodes | grep "^minikube" >/dev/null; then
	export MINIKUBE=1
fi
