package main

import (
	"os"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/pkg/errors"
)

func getMySQLState() (string, error) {
	stateFilePath, ok := os.LookupEnv(naming.EnvMySQLStateFile)
	if !ok {
		return "", errors.New("MYSQL_STATE_FILE env variable is required")
	}

	mysqlState, err := os.ReadFile(stateFilePath)
	if err != nil {
		return "", errors.Wrap(err, "read mysql state")
	}

	return string(mysqlState), nil
}
