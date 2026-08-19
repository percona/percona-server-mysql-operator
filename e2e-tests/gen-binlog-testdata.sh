#!/bin/bash
# Regenerates pkg/binlogsource/testdata fixtures from a real Percona Server.
#
# Not IMAGE_MYSQL: e2e-tests/vars.sh points that at the operator's own build,
# and these fixtures have to come from an upstream server.

set -o errexit

IMAGE=${BINLOG_TESTDATA_IMAGE:-percona/percona-server:8.4}
OUT=$(dirname "$0")/../pkg/binlogsource/testdata
CONTAINER=binlog-testdata
NAMES=(binlog.000001 binlog.000002 binlog.000003)

docker rm -f "${CONTAINER}" 2>/dev/null || true
docker run -d --name "${CONTAINER}" \
	-e MYSQL_ALLOW_EMPTY_PASSWORD=1 \
	"${IMAGE}" \
	--gtid-mode=ON --enforce-gtid-consistency=ON \
	--log-bin=binlog --binlog-checksum=CRC32 --server-id=1

until docker exec "${CONTAINER}" mysql -uroot -e 'SELECT 1' >/dev/null 2>&1; do sleep 1; done

docker exec "${CONTAINER}" mysql -uroot -e "
	CREATE DATABASE t;
	CREATE TABLE t.a (id INT PRIMARY KEY);
	FLUSH BINARY LOGS;
	INSERT INTO t.a VALUES (1),(2),(3);
	FLUSH BINARY LOGS;
	INSERT INTO t.a VALUES (4),(5);"

mkdir -p "${OUT}"
for name in "${NAMES[@]}"; do
	docker cp "${CONTAINER}":/var/lib/mysql/"${name}" "${OUT}/${name}"
done

# The tests read the index, so it has to list exactly the files copied out --
# mysqld's own index names files the fixtures do not include.
printf './%s\n' "${NAMES[@]}" >"${OUT}/binlog.index"

docker rm -f "${CONTAINER}"
