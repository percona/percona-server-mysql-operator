package users

import (
	"database/sql"
	"fmt"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

type Manager interface {
	UpdateUserPasswords(users []mysql.User) error
	DiscardOldPasswords(users []mysql.User) error
	Close() error
}

type dbImpl struct{ db *sql.DB }

func NewManager(user apiv1alpha1.SystemUser, pass, host string, port int32) (Manager, error) {
	config := mysqldriver.NewConfig()

	config.User = string(user)
	config.Passwd = pass
	config.Net = "tcp"
	config.Addr = fmt.Sprintf("%s:%d", host, port)
	config.DBName = "performance_schema"
	config.Params = map[string]string{
		"interpolateParams": "true",
		"timeout":           "20s",
		"readTimeout":       "20s",
		"writeTimeout":      "20s",
	}

	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, errors.Wrap(err, "connect to MySQL")
	}

	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "ping database")
	}

	return &dbImpl{db}, nil
}

// UpdateUserPasswords updates user passwords but retains the current password using Dual Password feature of MySQL 8
func (d *dbImpl) UpdateUserPasswords(users []mysql.User) error {
	tx, err := d.db.Begin()
	if err != nil {
		return errors.Wrap(err, "begin transaction")
	}

	for _, user := range users {
		for _, host := range user.Hosts {
			_, err = tx.Exec("ALTER USER ?@? IDENTIFIED BY ? RETAIN CURRENT PASSWORD", user.Username, host, user.Password)
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

// DiscardOldPasswords discards old passwords of givens users
func (d *dbImpl) DiscardOldPasswords(users []mysql.User) error {
	tx, err := d.db.Begin()
	if err != nil {
		return errors.Wrap(err, "begin transaction")
	}

	for _, user := range users {
		for _, host := range user.Hosts {
			_, err = tx.Exec("ALTER USER ?@? DISCARD OLD PASSWORD", user.Username, host)
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
