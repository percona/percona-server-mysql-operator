package psbackup

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/internal/testutil"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func TestBackupStatusErrStateDesc(t *testing.T) {
	namespace := "some-namespace"

	cr := testutil.UpdateResource(
		testutil.DefaultResource(new(apiv1alpha1.PerconaServerMySQLBackup)),
		func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
			cr.Namespace = namespace
		})

	tests := []struct {
		name      string
		cluster   *apiv1alpha1.PerconaServerMySQL
		cr        *apiv1alpha1.PerconaServerMySQLBackup
		stateDesc string
	}{
		{
			name:      "without cluster",
			cr:        cr,
			stateDesc: fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", cr.Spec.ClusterName, namespace),
		},
		{
			name: "without enabled backup section",
			cr:   cr,
			cluster: testutil.UpdateResource(
				testutil.DefaultResource(new(apiv1alpha1.PerconaServerMySQL)),
				func(cr *apiv1alpha1.PerconaServerMySQL) {
					cr.Namespace = namespace

					cr.Spec.Backup = &apiv1alpha1.BackupSpec{
						Image:    "some-image",
						Enabled:  false,
						Storages: make(map[string]*apiv1alpha1.BackupStorageSpec),
					}
				},
			),
			stateDesc: "spec.backup stanza not found in PerconaServerMySQL CustomResource or backup is disabled",
		},
		{
			name: "without storage",
			cr:   cr,
			cluster: testutil.UpdateResource(
				testutil.DefaultResource(new(apiv1alpha1.PerconaServerMySQL)),
				func(cr *apiv1alpha1.PerconaServerMySQL) {
					cr.Namespace = namespace
					cr.Spec.Backup = &apiv1alpha1.BackupSpec{
						Image:    "some-image",
						Enabled:  true,
						Storages: make(map[string]*apiv1alpha1.BackupStorageSpec),
					}
				},
			),
			stateDesc: fmt.Sprintf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", cr.Spec.StorageName),
		},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr)
			if tt.cluster != nil {
				cb.WithObjects(tt.cluster)
			}

			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
			}
			_, err := r.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.cr.Name,
					Namespace: tt.cr.Namespace,
				},
			})
			if err != nil {
				t.Fatal(err, "failed to reconcile")
			}
			cr := &apiv1alpha1.PerconaServerMySQLBackup{}
			err = r.Get(ctx, types.NamespacedName{
				Name:      tt.cr.Name,
				Namespace: tt.cr.Namespace,
			}, cr)
			if err != nil {
				t.Fatal(err, "failed to get backup")
			}
			if cr.Status.StateDesc != tt.stateDesc {
				t.Fatalf("expected stateDesc %s, got %s", tt.stateDesc, cr.Status.StateDesc)
			}
			if cr.Status.State != apiv1alpha1.BackupError {
				t.Fatalf("expected state %s, got %s", apiv1alpha1.RestoreError, cr.Status.State)
			}
		})
	}
}
