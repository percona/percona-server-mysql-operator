package gr

import (
	"context"
	"database/sql"
	"log"
	"strconv"
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
	getCloneThreshold(ctx context.Context) (uint64, error)
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
	defer func() {
		if err := primarySQLRunner.db.Close(); err != nil {
			log.Printf("Failed to close primary DB: %v", err)
		}
		if err := replicaSQLRunner.db.Close(); err != nil {
			log.Printf("Failed to close local DB: %v", err)
		}
	}()

	replicaState, err := checkReplicaState(ctx, primarySQLRunner, replicaSQLRunner)
	if err != nil {
		return "", errors.Wrap(err, "check replica state")
	}

	switch replicaState {
	case innodbcluster.ReplicaGtidDiverged:
		return innodbcluster.RecoveryClone, nil
	case innodbcluster.ReplicaGtidIrrecoverable:
		return innodbcluster.RecoveryClone, nil
	case innodbcluster.ReplicaGtidRecoverable, innodbcluster.ReplicaGtidIdentical, innodbcluster.ReplicaGtidNew:
		if exceeded, err := cloneThresholdExceeded(ctx, primarySQLRunner, replicaSQLRunner); err != nil {
			return "", errors.Wrap(err, "check clone threshold")
		} else if exceeded {
			return innodbcluster.RecoveryClone, nil
		}
		return innodbcluster.RecoveryIncremental, nil
	default:
		return innodbcluster.RecoveryClone, nil
	}
}

// cloneThresholdExceeded reports whether the number of transactions the replica
// is missing relative to the primary is greater than the replica's configured
// group_replication_clone_threshold. When it is, Group Replication uses a remote
// clone for distributed recovery instead of incremental state transfer.
func cloneThresholdExceeded(ctx context.Context, primary, replica SQLRunner) (bool, error) {
	threshold, err := replica.getCloneThreshold(ctx)
	if err != nil {
		return false, errors.Wrap(err, "get clone threshold")
	}

	primaryExecuted, err := primary.getGTIDExecuted(ctx)
	if err != nil {
		return false, errors.Wrap(err, "get GTID_EXECUTED from primary")
	}

	replicaExecuted, err := replica.getGTIDExecuted(ctx)
	if err != nil {
		return false, errors.Wrap(err, "get GTID_EXECUTED from replica")
	}

	var missing string
	switch {
	case primaryExecuted == "":
		missing = ""
	case replicaExecuted == "":
		missing = primaryExecuted
	default:
		missing, err = primary.gtidSubtract(ctx, primaryExecuted, replicaExecuted)
		if err != nil {
			return false, errors.Wrap(err, "compute missing transactions")
		}
	}

	count, err := countGTIDs(missing)
	if err != nil {
		return false, errors.Wrap(err, "count missing transactions")
	}

	log.Printf("Replica is missing %d transaction(s); group_replication_clone_threshold=%d", count, threshold)

	// Group Replication switches to clone when the gap is strictly greater than
	// the threshold.
	return count > threshold, nil
}

// countGTIDs returns the number of transactions in a GTID set string such as
// "uuid:1-5:8-10,uuid2:1-3". Tagged GTIDs (MySQL 8.4+), e.g. "uuid:tag:1-5", are
// supported - the non-numeric tag component is ignored.
func countGTIDs(gtidSet string) (uint64, error) {
	gtidSet = strings.TrimSpace(gtidSet)
	if gtidSet == "" {
		return 0, nil
	}

	var total uint64
	for _, uuidSet := range strings.Split(gtidSet, ",") {
		uuidSet = strings.TrimSpace(uuidSet)
		if uuidSet == "" {
			continue
		}

		parts := strings.Split(uuidSet, ":")
		if len(parts) < 2 {
			return 0, errors.Errorf("invalid GTID set %q", gtidSet)
		}

		// parts[0] is the source UUID; the remaining parts are intervals,
		// optionally preceded by a non-numeric tag.
		for _, part := range parts[1:] {
			if !isGTIDInterval(part) {
				// tag component, not an interval
				continue
			}

			start, end, found := strings.Cut(part, "-")
			s, err := strconv.ParseUint(start, 10, 64)
			if err != nil {
				return 0, errors.Wrapf(err, "parse GTID interval %q", part)
			}
			if !found {
				total++
				continue
			}
			e, err := strconv.ParseUint(end, 10, 64)
			if err != nil {
				return 0, errors.Wrapf(err, "parse GTID interval %q", part)
			}
			total += e - s + 1
		}
	}

	return total, nil
}

// isGTIDInterval reports whether s is a GTID interval ("5" or "5-10") rather than
// a GTID tag. Intervals contain only digits and dashes and begin with a digit.
func isGTIDInterval(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
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

func (r *sqlRunner) getCloneThreshold(ctx context.Context) (uint64, error) {
	var threshold uint64
	if err := r.db.QueryRowContext(ctx, "SELECT @@GLOBAL.group_replication_clone_threshold").Scan(&threshold); err != nil {
		return 0, errors.Wrap(err, "get group_replication_clone_threshold")
	}
	return threshold, nil
}
