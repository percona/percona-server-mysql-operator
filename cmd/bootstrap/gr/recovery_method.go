package gr

import (
	"context"
	"database/sql"
	"log"
	"strings"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

type SQLRunner interface {
	getGTIDExecuted(ctx context.Context) (string, error)
	getGTIDPurged(ctx context.Context) (string, error)
	gtidSubtract(ctx context.Context, set, subset string) (string, error)
	gtidSubtractIntersection(ctx context.Context, a, b string) (string, error)
	isPurgedSubsetOfExecuted(ctx context.Context, purged, executed string) (bool, error)
}

type GTIDSetRelation string

const (
	GTIDSetEqual      GTIDSetRelation = "EQUAL"
	GTIDSetContained  GTIDSetRelation = "CONTAINED"
	GTIDSetContains   GTIDSetRelation = "CONTAINS"
	GTIDSetDisjoint   GTIDSetRelation = "DISJOINT"
	GTIDSetIntersects GTIDSetRelation = "INTERSECTS"
)

func compareGTIDs(ctx context.Context, shell SQLRunner, a, b string) (GTIDSetRelation, error) {
	if a == "" || b == "" {
		if a == "" {
			if b == "" {
				return GTIDSetEqual, nil
			}
			return GTIDSetContained, nil
		}

		return GTIDSetContains, nil
	}

	aSubB, err := shell.gtidSubtract(ctx, a, b)
	if err != nil {
		return "", errors.Wrap(err, "a sub b")
	}

	bSubA, err := shell.gtidSubtract(ctx, b, a)
	if err != nil {
		return "", errors.Wrap(err, "b sub a")
	}

	if aSubB == "" && bSubA == "" {
		return GTIDSetEqual, nil
	}

	if aSubB == "" && bSubA != "" {
		return GTIDSetContained, nil
	}

	if aSubB != "" && bSubA == "" {
		return GTIDSetContains, nil
	}

	abIntersection, err := shell.gtidSubtractIntersection(ctx, a, b)
	if err != nil {
		return "", errors.Wrap(err, "intersection")
	}

	if abIntersection == "" {
		return GTIDSetDisjoint, nil
	}

	return GTIDSetIntersects, nil
}

func checkReplicaState(ctx context.Context, primary, replica SQLRunner) (innodbcluster.ReplicaGtidState, error) {
	primaryExecuted, err := primary.getGTIDExecuted(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get GTID_EXECUTED from primary")
	}
	log.Printf("Primary GTID_EXECUTED=%s", primaryExecuted)

	primaryPurged, err := primary.getGTIDPurged(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get GTID_PURGED from primary")
	}
	log.Printf("Primary GTID_PURGED=%s", primaryPurged)

	replicaExecuted, err := replica.getGTIDExecuted(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get GTID_EXECUTED from replica")
	}
	log.Printf("Replica GTID_EXECUTED=%s", replicaExecuted)

	if replicaExecuted == "" && primaryPurged == "" && primaryExecuted != "" {
		return innodbcluster.ReplicaGtidNew, nil
	}

	relation, err := compareGTIDs(ctx, primary, primaryExecuted, replicaExecuted)
	if err != nil {
		return "", errors.Wrap(err, "compare GTIDs")
	}

	switch relation {
	case GTIDSetIntersects, GTIDSetDisjoint, GTIDSetContained:
		return innodbcluster.ReplicaGtidDiverged, nil
	case GTIDSetEqual:
		return innodbcluster.ReplicaGtidIdentical, nil
	case GTIDSetContains:
		if primaryPurged == "" {
			return innodbcluster.ReplicaGtidRecoverable, nil
		}
		recoverable, err := primary.isPurgedSubsetOfExecuted(ctx, primaryPurged, replicaExecuted)
		if err != nil {
			return "", errors.Wrap(err, "check primary purged subset")
		}
		if recoverable {
			return innodbcluster.ReplicaGtidRecoverable, nil
		}
		return innodbcluster.ReplicaGtidIrrecoverable, nil
	}

	return "", errors.New("internal error")
}

func getRecoveryMethod(ctx context.Context, primary, replica string) (innodbcluster.RecoveryMethod, error) {
	primarySQLRunner, replicaSQLRunner, err := getSQLRunners(primary, replica)
	if err != nil {
		return "", errors.Wrap(err, "get databases")
	}

	replicaState, err := checkReplicaState(ctx, primarySQLRunner, replicaSQLRunner)
	if err != nil {
		return "", errors.Wrap(err, "check replica state")
	}

	switch replicaState {
	case innodbcluster.ReplicaGtidDiverged:
		return innodbcluster.RecoveryClone, nil
	case innodbcluster.ReplicaGtidIrrecoverable:
		return innodbcluster.RecoveryClone, nil
	case innodbcluster.ReplicaGtidRecoverable, innodbcluster.ReplicaGtidIdentical:
		return innodbcluster.RecoveryIncremental, nil
	case innodbcluster.ReplicaGtidNew:
		return innodbcluster.RecoveryIncremental, nil
	default:
		return innodbcluster.RecoveryClone, nil
	}
}

type sqlRunner struct {
	db *sql.DB
}

func (r *sqlRunner) getGTIDExecuted(ctx context.Context) (string, error) {
	var gtid string
	if err := r.db.QueryRowContext(ctx, "SELECT @@GTID_EXECUTED").Scan(&gtid); err != nil {
		return "", errors.Wrap(err, "get GTID_EXECUTED")
	}
	return strings.ReplaceAll(gtid, "\n", ""), nil
}

func (r *sqlRunner) getGTIDPurged(ctx context.Context) (string, error) {
	var gtid string
	if err := r.db.QueryRowContext(ctx, "SELECT @@GTID_PURGED").Scan(&gtid); err != nil {
		return "", errors.Wrap(err, "get GTID_PURGED")
	}
	return strings.ReplaceAll(gtid, "\n", ""), nil
}

func (r *sqlRunner) gtidSubtract(ctx context.Context, set, subset string) (string, error) {
	var sub sql.NullString
	if err := r.db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, ?)", set, subset).Scan(&sub); err != nil {
		return "", errors.Wrap(err, "GTID_SUBTRACT")
	}
	return sub.String, nil
}

func (r *sqlRunner) gtidSubtractIntersection(ctx context.Context, a, b string) (string, error) {
	var sub sql.NullString
	if err := r.db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, GTID_SUBTRACT(?, ?))", a, a, b).Scan(&sub); err != nil {
		return "", errors.Wrap(err, "GTID_SUBTRACT intersection")
	}
	return sub.String, nil
}

func (r *sqlRunner) isPurgedSubsetOfExecuted(ctx context.Context, purged, executed string) (bool, error) {
	var isSubset bool
	if err := r.db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, ?) = ''", purged, executed).Scan(&isSubset); err != nil {
		return false, errors.Wrap(err, "check purged subset")
	}
	return isSubset, nil
}
