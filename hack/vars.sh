#!/bin/bash

export ROOT_REPO=${ROOT_REPO:-${PWD}}

export DEPLOY_DIR="${DEPLOY_DIR:-${ROOT_REPO}/deploy}"
export TESTS_DIR="${TESTS_DIR:-${ROOT_REPO}/tests}"
export TESTS_CONFIG_DIR="${TESTS_CONFIG_DIR:-${TESTS_DIR}/conf}"
export TEMP_DIR=$(mktemp -d)

export GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
export VERSION=${VERSION:-$(echo "${GIT_BRANCH}" | sed -e 's^/^-^g; s^[.]^-^g;' | tr '[:upper:]' '[:lower:]')}

export IMAGE=${IMAGE:-"perconalab/percona-server-mysql-operator:${VERSION}"}
export IMAGE_MYSQL=${IMAGE_MYSQL:-"percona/percona-server:8.0.25"}
export IMAGE_ORCHESTRATOR=${IMAGE_ORCHESTRATOR:-"perconalab/percona-server-mysql-operator:3.2.6-orchestrator"}
export IMAGE_PMM=${IMAGE_PMM:-"perconalab/pmm-client:2.18.0"}

export CR_VERSION=${CR_VERSION:-"2.0.0"}
