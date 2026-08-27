#!/bin/bash

set -e

INPUT_DIR="/etc/s3/certs-in"
OUTPUT_FILE="/etc/s3/certs/ca-bundle.crt"
SYSTEM_CA_FILE="/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"

echo -n >"${OUTPUT_FILE}"
if [ -f "${SYSTEM_CA_FILE}" ]; then
	cat "${SYSTEM_CA_FILE}" >>"${OUTPUT_FILE}"
	echo >>"${OUTPUT_FILE}"
fi
for cert in "${INPUT_DIR}"/*.crt; do
	if [ -f "${cert}" ]; then
		cat "${cert}" >>"${OUTPUT_FILE}"
		echo >>"${OUTPUT_FILE}"
	fi
done

exec "$@"
