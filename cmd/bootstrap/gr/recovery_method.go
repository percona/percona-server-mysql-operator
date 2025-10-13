package gr

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

type SQLRunner interface {
	runSQL(ctx context.Context, sql string) (SQLResult, error)
	getGTIDExecuted(ctx context.Context) (string, error)
	getGTIDPurged(ctx context.Context) (string, error)
}

func gtidSubtract(ctx context.Context, shell SQLRunner, a, b string) (string, error) {
	a = strings.ReplaceAll(a, "\n", "")
	b = strings.ReplaceAll(b, "\n", "")

	query := fmt.Sprintf("SELECT GTID_SUBTRACT('%s', '%s') AS sub", a, b)

	result, err := shell.runSQL(ctx, query)
	if err != nil {
		return "", errors.Wrapf(err, "execute %s", query)
	}

	v, ok := result.Rows[0]["sub"]
	if !ok {
		return "", errors.Errorf("unexpected output: %+v", result)
	}

	s, ok := v.(string)
	if !ok {
		return "", errors.Errorf("unexpected type: %T", v)
	}

	return s, nil
}

func gtidSubtractIntersection(ctx context.Context, shell SQLRunner, a, b string) (string, error) {
	a = strings.ReplaceAll(a, "\n", "")
	b = strings.ReplaceAll(b, "\n", "")

	query := fmt.Sprintf("SELECT GTID_SUBTRACT('%s', GTID_SUBTRACT('%s', '%s')) AS sub", a, a, b)

	result, err := shell.runSQL(ctx, query)
	if err != nil {
		return "", errors.Wrapf(err, "execute %s", query)
	}

	v, ok := result.Rows[0]["sub"]
	if !ok {
		return "", errors.Errorf("unexpected output: %+v", result)
	}

	s, ok := v.(string)
	if !ok {
		return "", errors.Errorf("unexpected type: %T", v)
	}

	return s, nil
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

	aSubB, err := gtidSubtract(ctx, shell, a, b)
	if err != nil {
		return "", errors.Wrapf(err, "a sub b")
	}

	bSubA, err := gtidSubtract(ctx, shell, b, a)
	if err != nil {
		return "", errors.Wrapf(err, "b sub a")
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

	abIntersection, err := gtidSubtractIntersection(ctx, shell, a, b)
	if err != nil {
		return "", errors.Wrapf(err, "intersection")
	}

	if abIntersection == "" {
		return GTIDSetDisjoint, nil
	}

	return GTIDSetIntersects, nil
}

// If purged has more gtids than the executed on the replica
// it means some data will not be recoverable
func comparePrimaryPurged(ctx context.Context, shell SQLRunner, purged, executed string) (bool, error) {
	query := fmt.Sprintf("SELECT GTID_SUBTRACT('%s', '%s') = '' AS result", purged, executed)

	result, err := shell.runSQL(ctx, query)
	if err != nil {
		return false, errors.Wrap(err, "run sql")
	}

	v, ok := result.Rows[0]["result"]
	if !ok {
		return false, errors.Errorf("unexpected output: %+v", result)
	}

	s, ok := v.(float64)
	if !ok {
		return false, errors.Errorf("unexpected type: %T", v)
	}

	return s == 0, nil
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
		compareRes, err := comparePrimaryPurged(ctx, primary, primaryPurged, replicaExecuted)
		if err != nil {
			return "", errors.Wrap(err, "compare primary purged")
		}
		if compareRes {
			return innodbcluster.ReplicaGtidRecoverable, nil
		}
		return innodbcluster.ReplicaGtidIrrecoverable, nil
	}

	return "", errors.New("internal error")
}

func getRecoveryMethod(ctx context.Context, primary, replica SQLRunner) (innodbcluster.RecoveryMethod, error) {
	replicaState, err := checkReplicaState(ctx, primary, replica)
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
