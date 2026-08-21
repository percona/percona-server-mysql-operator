package ps

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

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

// A selected PVC can exist before it reports a capacity, for example a claim
// that is still Pending during a scale up. Sizing it must not panic, and it must
// not look like a finished resize, so that the operator waits for the claim.
func TestPVCSizeWithoutCapacity(t *testing.T) {
	requested := resource.MustParse("3Gi")

	tests := []struct {
		name    string
		pvc     corev1.PersistentVolumeClaim
		mounted bool
	}{
		{
			name:    "pending claim with a pod",
			pvc:     corev1.PersistentVolumeClaim{},
			mounted: true,
		},
		{
			name:    "pending claim without a pod",
			pvc:     corev1.PersistentVolumeClaim{},
			mounted: false,
		},
		{
			// the branch that reads allocatedResources, before that map is filled
			name: "resize reported before allocatedResources is set",
			pvc: corev1.PersistentVolumeClaim{
				Status: corev1.PersistentVolumeClaimStatus{
					AllocatedResourceStatuses: map[corev1.ResourceName]corev1.ClaimResourceStatus{
						corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending,
					},
				},
			},
			mounted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := pvcSize(tt.pvc, tt.mounted)
			if size == nil {
				t.Fatal("pvcSize returned nil, callers dereference it")
			}
			if !size.IsZero() {
				t.Fatalf("pvcSize = %s, want zero for a claim with no capacity", size)
			}
			if size.Cmp(requested) == 0 {
				t.Fatal("a claim with no capacity must not count as resized")
			}
		})
	}
}
