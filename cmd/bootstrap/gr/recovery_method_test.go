package gr

import (
	"context"
	"fmt"
	"testing"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

type mockSQLRunner struct {
	sqlResponses map[string]string
	gtidExecuted string
	gtidPurged   string
}

func newMockSQLRunner() *mockSQLRunner {
	return &mockSQLRunner{
		sqlResponses: make(map[string]string),
		gtidExecuted: "",
		gtidPurged:   "",
	}
}

func (m *mockSQLRunner) runSQL(ctx context.Context, sql string) (SQLResult, error) {
	if result, ok := m.sqlResponses[sql]; ok {
		return SQLResult{
			Rows: []map[string]string{
				{
					"sub":           result,
					"GTID_SUBTRACT": result, // for comparePrimaryPurged
				},
			},
		}, nil
	}

	return SQLResult{}, errors.Errorf("unexpected query: %s", sql)
}

func (m *mockSQLRunner) getGTIDExecuted(ctx context.Context) (string, error) {
	return m.gtidExecuted, nil
}

func (m *mockSQLRunner) getGTIDPurged(ctx context.Context) (string, error) {
	return m.gtidPurged, nil
}

func (m *mockSQLRunner) setGTIDSubtractResponse(a, b, result string) {
	query := fmt.Sprintf("SELECT GTID_SUBTRACT('%s', '%s') AS sub", a, b)
	m.sqlResponses[query] = result
}

func TestCompareGTIDs_Equal(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	// both subtractions return empty = equal sets
	mock.setGTIDSubtractResponse("a:1-5", "b:1-3", "")
	mock.setGTIDSubtractResponse("b:1-3", "a:1-5", "")

	result, err := compareGTIDs(ctx, mock, "a:1-5", "b:1-3")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetEqual {
		t.Errorf("expected GTIDSetEqual, got %v", result)
	}
}

func TestCompareGTIDs_AContainsB(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	// a - b = something, b - a = nothing = a contains b
	mock.setGTIDSubtractResponse("a:1-10", "a:1-5", "a:6-10")
	mock.setGTIDSubtractResponse("a:1-5", "a:1-10", "")

	result, err := compareGTIDs(ctx, mock, "a:1-10", "a:1-5")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetContains {
		t.Errorf("expected GTIDSetContains, got %v", result)
	}
}

func TestCompareGTIDs_BContainsA(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	// a - b = nothing, b - a = something = a contained in b
	mock.setGTIDSubtractResponse("a:1-5", "a:1-10", "")
	mock.setGTIDSubtractResponse("a:1-10", "a:1-5", "a:6-10")

	result, err := compareGTIDs(ctx, mock, "a:1-5", "a:1-10")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetContained {
		t.Errorf("expected GTIDSetContained, got %v", result)
	}
}

func TestCompareGTIDs_Disjoint(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	// both subtractions return non-empty, intersection is empty = disjoint
	mock.setGTIDSubtractResponse("a:1-5", "b:1-5", "a:1-5")
	mock.setGTIDSubtractResponse("b:1-5", "a:1-5", "b:1-5")
	// the intersection calculation: a - (a - b) should be empty for disjoint
	mock.setGTIDSubtractResponse("a:1-5", "GTID_SUBTRACT('a:1-5', 'b:1-5')", "")

	result, err := compareGTIDs(ctx, mock, "a:1-5", "b:1-5")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetDisjoint {
		t.Errorf("expected GTIDSetDisjoint, got %v", result)
	}
}

func TestCompareGTIDs_Intersects(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	// both subtractions return non-empty, intersection is non-empty = intersects
	mock.setGTIDSubtractResponse("a:1-10", "a:6-15", "a:1-5")
	mock.setGTIDSubtractResponse("a:6-15", "a:1-10", "a:11-15")
	// intersection calculation: a - (a - b) should be non-empty
	mock.setGTIDSubtractResponse("a:1-10", "GTID_SUBTRACT('a:1-10', 'a:6-15')", "a:6-10")

	result, err := compareGTIDs(ctx, mock, "a:1-10", "a:6-15")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetIntersects {
		t.Errorf("expected GTIDSetIntersects, got %v", result)
	}
}

func TestCompareGTIDs_BothEmpty(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	result, err := compareGTIDs(ctx, mock, "", "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetEqual {
		t.Errorf("expected GTIDSetEqual, got %v", result)
	}
}

func TestCompareGTIDs_AEmpty(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	result, err := compareGTIDs(ctx, mock, "", "b:1-5")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetContained {
		t.Errorf("expected GTIDSetContained, got %v", result)
	}
}

func TestCompareGTIDs_BEmpty(t *testing.T) {
	ctx := context.Background()
	mock := newMockSQLRunner()

	result, err := compareGTIDs(ctx, mock, "a:1-5", "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != GTIDSetContains {
		t.Errorf("expected GTIDSetContains, got %v", result)
	}
}

// helper to create mock with gtid values set
func newMockSQLRunnerWithGTIDs(executed, purged string) *mockSQLRunner {
	mock := newMockSQLRunner()
	mock.gtidExecuted = executed
	mock.gtidPurged = purged
	return mock
}

func TestCheckReplicaState_New(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-10", "")
	replica := newMockSQLRunnerWithGTIDs("", "")

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidNew {
		t.Errorf("expected ReplicaGtidNew, got %v", result)
	}
}

func TestCheckReplicaState_Identical(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-10", "")
	replica := newMockSQLRunnerWithGTIDs("a:1-10", "")

	// mock the compareGTIDs calls - both subtractions return empty
	primary.setGTIDSubtractResponse("a:1-10", "a:1-10", "")
	primary.setGTIDSubtractResponse("a:1-10", "a:1-10", "")

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidIdentical {
		t.Errorf("expected ReplicaGtidIdentical, got %v", result)
	}
}

func TestCheckReplicaState_Recoverable_NoPurged(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-10", "")
	replica := newMockSQLRunnerWithGTIDs("a:1-5", "")

	// primary contains replica (replica is behind)
	primary.setGTIDSubtractResponse("a:1-10", "a:1-5", "a:6-10")
	primary.setGTIDSubtractResponse("a:1-5", "a:1-10", "")

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidRecoverable {
		t.Errorf("expected ReplicaGtidRecoverable, got %v", result)
	}
}

func TestCheckReplicaState_Recoverable_WithPurgedButOK(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-10", "a:1-3")
	replica := newMockSQLRunnerWithGTIDs("a:1-5", "")

	// primary contains replica
	primary.setGTIDSubtractResponse("a:1-10", "a:1-5", "a:6-10")
	primary.setGTIDSubtractResponse("a:1-5", "a:1-10", "")

	// comparePrimaryPurged check - purged is subset of replica executed
	primary.sqlResponses["SELECT GTID_SUBTRACT('a:1-3', 'a:1-5') = ''"] = "0"

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidRecoverable {
		t.Errorf("expected ReplicaGtidRecoverable, got %v", result)
	}
}

func TestCheckReplicaState_Irrecoverable(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-10", "a:1-8")
	replica := newMockSQLRunnerWithGTIDs("a:1-5", "")

	// primary contains replica
	primary.setGTIDSubtractResponse("a:1-10", "a:1-5", "a:6-10")
	primary.setGTIDSubtractResponse("a:1-5", "a:1-10", "")

	// comparePrimaryPurged check fails - purged has more than replica executed
	primary.sqlResponses["SELECT GTID_SUBTRACT('a:1-8', 'a:1-5') = ''"] = "1"

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidIrrecoverable {
		t.Errorf("expected ReplicaGtidIrrecoverable, got %v", result)
	}
}

func TestCheckReplicaState_Diverged_Intersects(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-10", "")
	replica := newMockSQLRunnerWithGTIDs("a:5-15", "")

	// sets intersect but neither contains the other
	primary.setGTIDSubtractResponse("a:1-10", "a:5-15", "a:1-4")
	primary.setGTIDSubtractResponse("a:5-15", "a:1-10", "a:11-15")
	primary.setGTIDSubtractResponse("a:1-10", "GTID_SUBTRACT('a:1-10', 'a:5-15')", "a:5-10")

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidDiverged {
		t.Errorf("expected ReplicaGtidDiverged, got %v", result)
	}
}

func TestCheckReplicaState_Diverged_Disjoint(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-5", "")
	replica := newMockSQLRunnerWithGTIDs("b:1-5", "")

	// completely different gtid sets
	primary.setGTIDSubtractResponse("a:1-5", "b:1-5", "a:1-5")
	primary.setGTIDSubtractResponse("b:1-5", "a:1-5", "b:1-5")
	primary.setGTIDSubtractResponse("a:1-5", "GTID_SUBTRACT('a:1-5', 'b:1-5')", "")

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidDiverged {
		t.Errorf("expected ReplicaGtidDiverged, got %v", result)
	}
}

func TestCheckReplicaState_Diverged_Contained(t *testing.T) {
	ctx := context.Background()
	primary := newMockSQLRunnerWithGTIDs("a:1-5", "")
	replica := newMockSQLRunnerWithGTIDs("a:1-10", "")

	// replica ahead of primary = diverged
	primary.setGTIDSubtractResponse("a:1-5", "a:1-10", "")
	primary.setGTIDSubtractResponse("a:1-10", "a:1-5", "a:6-10")

	result, err := checkReplicaState(ctx, primary, replica)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != innodbcluster.ReplicaGtidDiverged {
		t.Errorf("expected ReplicaGtidDiverged, got %v", result)
	}
}
