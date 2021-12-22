package users

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

type Manager interface {
	UpdateUserPasswords(users []mysql.User) error
	Close() error
}

type dbImpl struct{ db *sql.DB }

func NewManager(user apiv2.SystemUser, pass, host string, port int32) (Manager, error) {
	connStr := fmt.Sprintf("%s:%s@tcp(%s:%d)/performance_schema?interpolateParams=true",
		user, pass, host, port)
	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "ping database")
	}

	return &dbImpl{db}, nil
}

func (d *dbImpl) UpdateUserPasswords(users []mysql.User) error {
	tx, err := d.db.Begin()
	if err != nil {
		return errors.Wrap(err, "begin transaction")
	}

	for _, user := range users {
		for _, host := range user.Hosts {
			_, err = tx.Exec("ALTER USER ?@? IDENTIFIED BY ?", user.Username, host, user.Password)
			if err != nil {
				err = errors.Wrap(err, "alter user")

				if errT := tx.Rollback(); errT != nil {
					return errors.Wrap(errors.Wrap(errT, "rollback"), err.Error())
				}

				return err
			}
		}
	}

	_, err = tx.Exec("FLUSH PRIVILEGES")
	if err != nil {
		err = errors.Wrap(err, "flush privileges")

		if errT := tx.Rollback(); errT != nil {
			return errors.Wrap(errors.Wrap(errT, "rollback"), err.Error())
		}

		return err
	}

	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit transaction")
	}

	return nil
}

func (d *dbImpl) Close() error {
	return d.db.Close()
}
