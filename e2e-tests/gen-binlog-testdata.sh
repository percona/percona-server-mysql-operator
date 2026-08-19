#!/bin/bash
# Regenerates pkg/binlogsource/testdata fixtures from a real Percona Server.

set -o errexit

IMAGE=${IMAGE_MYSQL:-percona/percona-server:8.4}
OUT=$(dirname "$0")/../pkg/binlogsource/testdata
CONTAINER=binlog-testdata

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
docker cp "${CONTAINER}":/var/lib/mysql/. /tmp/binlog-testdata
cp /tmp/binlog-testdata/binlog.00000{1..3} "${OUT}/"
docker rm -f "${CONTAINER}"
