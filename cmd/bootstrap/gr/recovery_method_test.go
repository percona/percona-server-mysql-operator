package gr

import (
	"context"
	"testing"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

type mockSQLRunner struct {
	gtidExecuted   string
	gtidPurged     string
	cloneThreshold uint64
	subtract       map[[2]string]string
	intersect      map[[2]string]string
	isSubset       map[[2]string]bool
}

func newMockSQLRunner() *mockSQLRunner {
	return &mockSQLRunner{
		subtract:  make(map[[2]string]string),
		intersect: make(map[[2]string]string),
		isSubset:  make(map[[2]string]bool),
	}
}

func (m *mockSQLRunner) getGTIDExecuted(ctx context.Context) (string, error) {
	return m.gtidExecuted, nil
}

func (m *mockSQLRunner) getGTIDPurged(ctx context.Context) (string, error) {
	return m.gtidPurged, nil
}

func (m *mockSQLRunner) gtidSubtract(ctx context.Context, a, b string) (string, error) {
	v, ok := m.subtract[[2]string{a, b}]
	if !ok {
		return "", errors.Errorf("unexpected gtidSubtract(%q, %q)", a, b)
	}
	return v, nil
}

func (m *mockSQLRunner) gtidSubtractIntersection(ctx context.Context, a, b string) (string, error) {
	v, ok := m.intersect[[2]string{a, b}]
	if !ok {
		return "", errors.Errorf("unexpected gtidSubtractIntersection(%q, %q)", a, b)
	}
	return v, nil
}

func (m *mockSQLRunner) isPurgedSubsetOfExecuted(ctx context.Context, purged, executed string) (bool, error) {
	v, ok := m.isSubset[[2]string{purged, executed}]
	if !ok {
		return false, errors.Errorf("unexpected isPurgedSubsetOfExecuted(%q, %q)", purged, executed)
	}
	return v, nil
}

func (m *mockSQLRunner) getCloneThreshold(ctx context.Context) (uint64, error) {
	return m.cloneThreshold, nil
}

func (m *mockSQLRunner) setGTIDSubtractResponse(a, b, result string) {
	m.subtract[[2]string{a, b}] = result
}

func (m *mockSQLRunner) setGTIDSubtractIntersectionResponse(a, b, result string) {
	m.intersect[[2]string{a, b}] = result
}

func (m *mockSQLRunner) setIsPurgedSubsetResponse(purged, executed string, result bool) {
	m.isSubset[[2]string{purged, executed}] = result
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
	mock.setGTIDSubtractIntersectionResponse("a:1-5", "b:1-5", "")

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
	mock.setGTIDSubtractIntersectionResponse("a:1-10", "a:6-15", "a:6-10")

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

	primary.setIsPurgedSubsetResponse("a:1-3", "a:1-5", true)

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

	primary.setIsPurgedSubsetResponse("a:1-8", "a:1-5", false)

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
	primary.setGTIDSubtractIntersectionResponse("a:1-10", "a:5-15", "a:5-10")

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
	primary.setGTIDSubtractIntersectionResponse("a:1-5", "b:1-5", "")

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

func TestCloneThresholdExceeded(t *testing.T) {
	tests := []struct {
		name            string
		primaryExecuted string
		replicaExecuted string
		threshold       uint64
		// missing is the GTID set returned by GTID_SUBTRACT on the primary; it
		// is only consulted when both primary and replica have executed GTIDs.
		missing string
		want    bool
	}{
		{
			name:            "primary empty - nothing missing",
			primaryExecuted: "",
			replicaExecuted: "aaaa:1-5",
			threshold:       0,
			want:            false,
		},
		{
			name:            "replica empty - all primary transactions missing, exceeds",
			primaryExecuted: "aaaa:1-10",
			replicaExecuted: "",
			threshold:       5,
			want:            true,
		},
		{
			name:            "replica empty - all primary transactions missing, within threshold",
			primaryExecuted: "aaaa:1-3",
			replicaExecuted: "",
			threshold:       5,
			want:            false,
		},
		{
			name:            "gap exceeds threshold",
			primaryExecuted: "aaaa:1-20",
			replicaExecuted: "aaaa:1-5",
			threshold:       10,
			missing:         "aaaa:6-20", // 15 transactions
			want:            true,
		},
		{
			name:            "gap within threshold",
			primaryExecuted: "aaaa:1-8",
			replicaExecuted: "aaaa:1-5",
			threshold:       10,
			missing:         "aaaa:6-8", // 3 transactions
			want:            false,
		},
		{
			name:            "gap equals threshold - not exceeded (strictly greater)",
			primaryExecuted: "aaaa:1-10",
			replicaExecuted: "aaaa:1-5",
			threshold:       5,
			missing:         "aaaa:6-10", // 5 transactions
			want:            false,
		},
		{
			name:            "gap one more than threshold - exceeded",
			primaryExecuted: "aaaa:1-11",
			replicaExecuted: "aaaa:1-5",
			threshold:       5,
			missing:         "aaaa:6-11", // 6 transactions
			want:            true,
		},
		{
			name:            "no missing transactions",
			primaryExecuted: "aaaa:1-5",
			replicaExecuted: "aaaa:1-5",
			threshold:       0,
			missing:         "",
			want:            false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			primary := newMockSQLRunnerWithGTIDs(tt.primaryExecuted, "")
			replica := newMockSQLRunnerWithGTIDs(tt.replicaExecuted, "")
			replica.cloneThreshold = tt.threshold

			// Only the both-non-empty branch performs a GTID_SUBTRACT.
			if tt.primaryExecuted != "" && tt.replicaExecuted != "" {
				primary.setGTIDSubtractResponse(tt.primaryExecuted, tt.replicaExecuted, tt.missing)
			}

			got, err := cloneThresholdExceeded(ctx, primary, replica)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("cloneThresholdExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountGTIDs(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want uint64
	}{
		{"empty", "", 0},
		{"single transaction", "3E11FA47-71CA-11E1-9E33-C80AA9429562:1", 1},
		{"range", "3E11FA47-71CA-11E1-9E33-C80AA9429562:1-10", 10},
		{"multiple intervals", "3E11FA47-71CA-11E1-9E33-C80AA9429562:1-5:8-10", 8},
		{"multiple uuids", "aaaa:1-5,bbbb:1-3", 8},
		{"tagged gtid (8.4)", "3E11FA47-71CA-11E1-9E33-C80AA9429562:mytag:1-5", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := countGTIDs(tt.set)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("countGTIDs(%q) = %d, want %d", tt.set, got, tt.want)
			}
		})
	}
}
