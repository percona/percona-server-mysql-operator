#!/bin/bash

until [ ! -f /var/lib/mysql/bootstrap.lock ]; do
	echo "waiting for MySQL initialization"
	sleep 1
done

ls -l /var/lib/mysql/

sleep infinity
