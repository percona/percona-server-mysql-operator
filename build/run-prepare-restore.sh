#!/bin/bash

set -e

LIB_PATH='/opt/percona/lib'
# shellcheck source=build/lib/util.sh
. ${LIB_PATH}/util.sh

function cleanup_clusterset_meta() {
	local has_cs_table cs_rows=0
	has_cs_table=$(mysql_exec "SELECT COUNT(*) FROM information_schema.tables
          WHERE table_schema='mysql_innodb_cluster_metadata' AND table_name='clustersets';")
	if [ "${has_cs_table}" != "0" ]; then
		cs_rows=$(mysql_exec "SELECT COUNT(*) FROM mysql_innodb_cluster_metadata.clustersets;")
	fi
	if [ "${cs_rows}" == "0" ]; then
		log "No ClusterSet metadata detected; leaving restored data untouched"
		return
	fi
	log "ClusterSet metadata detected, cleaning up restored data"

	mysql_exec "SELECT group_replication_reset_member_actions();"
	mysql_exec "SET GLOBAL super_read_only = OFF; SET GLOBAL read_only = OFF; STOP REPLICA; RESET REPLICA ALL; RESET PERSIST;"
}

trap remove_keyring_component EXIT

configure_keyring

start_mysqld \
	--loose-group-replication-start-on-boot=OFF \
	--skip-replica-start=ON \
	--plugin-load-add=group_replication.so \
	--gtid-mode=ON \
	--enforce-gtid-consistency=ON \
	--skip-networking

cleanup_clusterset_meta
stop_mysqld
