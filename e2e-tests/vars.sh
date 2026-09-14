#!/bin/bash

export ROOT_REPO=${ROOT_REPO:-${PWD}}
export DEPLOY_DIR="${DEPLOY_DIR:-${ROOT_REPO}/deploy}"
export TESTS_DIR="${TESTS_DIR:-${ROOT_REPO}/e2e-tests}"
export TESTS_CONFIG_DIR="${TESTS_CONFIG_DIR:-${TESTS_DIR}/conf}"
export TEST_CONFIG_DIR="${TEST_CONFIG_DIR:-${TESTS_DIR}/tests/${test_name}/conf}"
export TEMP_DIR="/tmp/kuttl/ps/${test_name}"

export GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
export VERSION=${VERSION:-$(echo "${GIT_BRANCH}" | sed -e 's^/^-^g; s^[.]^-^g;' | tr '[:upper:]' '[:lower:]')}

export PMM_SERVER_VERSION=${PMM_SERVER_VERSION:-"1.4.3"}
export CERT_MANAGER_VER="1.20.3"
export MINIO_VER="5.4.0"
export CHAOS_MESH_VER="2.7.2"
export VAULT_VER="0.16.1"

if [[ -z ${MYSQL_VERSION-} && -n ${IMAGE_MYSQL-} ]]; then
	export MYSQL_VERSION=$(echo "$IMAGE_MYSQL" | sed -E 's/.*://; s/^[^0-9]*([0-9]+\.[0-9]+).*/\1/')
fi

export MYSQL_VERSION=${MYSQL_VERSION:-"8.4"}

export date=$(which gdate || which date)
export sed=$(which gsed || which sed)

detect_k8s_platform() {
	local platform=""
	local xtrace_enabled=false

	if [[ -o xtrace ]]; then
		xtrace_enabled=true
		set +o xtrace
	fi

	if [[ -n ${PLATFORM:-} ]]; then
		platform="${PLATFORM}"
	elif kubectl get nodes -o json | jq -r '.items[0].spec.providerID' | grep -q "gce://"; then
		platform="gke"
	elif oc get projects >/dev/null 2>&1; then
		platform="openshift"
	elif kubectl get nodes -o json | jq -r '.items[0].spec.providerID' | grep -q "aws://"; then
		platform="eks"
	elif kubectl get nodes -o json | jq -r '.items[0].spec.providerID' | grep -q "digitalocean://"; then
		platform="doks"
	elif kubectl get nodes -o json | jq -r '.items[0].spec.providerID' | grep -q "azure://"; then
		platform="aks"
	elif kubectl get nodes -o json | jq -e '.items[0].metadata.labels["minikube.k8s.io/name"] != null' >/dev/null; then
		platform="minikube"
	elif kubectl get nodes -o json | jq -r '.items[0].spec.providerID' | grep -qi "rke2"; then
		platform="rancher"
	else
		platform="unknown"
	fi

	export PLATFORM="${platform}"

	if [[ ${xtrace_enabled} == true ]]; then
		set -o xtrace
	fi
}

detect_k8s_platform

image_with_registry() {
	local image="$1"
	local registry="${IMAGE_REGISTRY:-docker.io}"

	case "$image" in
		percona/* | perconalab/*)
			echo "${registry%/}/${image}"
			;;
		*)
			echo "$image"
			;;
	esac
}

set_image() {
	local image_var="$1"
	local default_image="$2"
	local image="${!image_var:-$default_image}"

	export "${image_var}=$(image_with_registry "$image")"
}

set_image IMAGE "perconalab/percona-server-mysql-operator:${VERSION}"
set_image IMAGE_MYSQL "perconalab/percona-server-mysql-operator:main-psmysql${MYSQL_VERSION}"
set_image IMAGE_BACKUP "perconalab/percona-server-mysql-operator:main-backup${MYSQL_VERSION}"
set_image IMAGE_ORCHESTRATOR "perconalab/percona-server-mysql-operator:main-orchestrator"
set_image IMAGE_ROUTER "perconalab/percona-server-mysql-operator:main-router${MYSQL_VERSION}"
set_image IMAGE_TOOLKIT "perconalab/percona-server-mysql-operator:main-toolkit"
set_image IMAGE_HAPROXY "perconalab/percona-server-mysql-operator:main-haproxy"
set_image IMAGE_BINLOG_SERVER "perconalab/percona-binlog-server:0.4.0"
set_image IMAGE_PMM_CLIENT "perconalab/pmm-client:3-dev-latest"
set_image IMAGE_PMM_SERVER "perconalab/pmm-server:3-dev-latest"
