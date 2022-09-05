package client

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func New(user apiv1alpha1.SystemUser, pass, host string, port int32, database string) (*sql.DB, error) {
	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?interpolateParams=true", user, pass, host, port, database)

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "ping database")
	}

	return db, nil
}
