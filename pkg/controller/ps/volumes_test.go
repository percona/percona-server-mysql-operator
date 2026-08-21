package ps

import "testing"

func TestPVCOrdinal(t *testing.T) {
	const stsName = "cluster1-mysql"

	tests := []struct {
		name    string
		pvcName string
		ordinal int
		ok      bool
	}{
		{name: "first replica", pvcName: "datadir-cluster1-mysql-0", ordinal: 0, ok: true},
		{name: "tenth replica", pvcName: "datadir-cluster1-mysql-9", ordinal: 9, ok: true},
		{name: "two digit ordinal", pvcName: "datadir-cluster1-mysql-10", ordinal: 10, ok: true},

		{name: "another cluster", pvcName: "datadir-cluster2-mysql-0", ok: false},
		{name: "another volume", pvcName: "backup-cluster1-mysql-0", ok: false},
		{name: "no ordinal", pvcName: "datadir-cluster1-mysql-", ok: false},
		{name: "not a number", pvcName: "datadir-cluster1-mysql-primary", ok: false},

		// a statefulset never creates these, and accepting them would let a claim
		// no replica can ever use take part in a resize
		{name: "negative ordinal", pvcName: "datadir-cluster1-mysql--1", ok: false},
		{name: "explicitly signed ordinal", pvcName: "datadir-cluster1-mysql-+1", ok: false},
		{name: "zero padded ordinal", pvcName: "datadir-cluster1-mysql-01", ok: false},
		{name: "negative zero", pvcName: "datadir-cluster1-mysql--0", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ordinal, ok := pvcOrdinal(tt.pvcName, stsName)
			if ok != tt.ok {
				t.Fatalf("pvcOrdinal(%q) ok = %v, want %v (ordinal %d)", tt.pvcName, ok, tt.ok, ordinal)
			}
			if ok && ordinal != tt.ordinal {
				t.Fatalf("pvcOrdinal(%q) = %d, want %d", tt.pvcName, ordinal, tt.ordinal)
			}
		})
	}
}
