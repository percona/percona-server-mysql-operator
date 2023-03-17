package psrestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func TestRestoreStatusErrStateDesc(t *testing.T) {
	namespace := "some-namespace"
	clusterName := "some-name"
	backupName := "backup1"
	restoreName := "restore1"
	storageName := "some-storage"

	tests := []struct {
		name      string
		cr        *apiv1alpha1.PerconaServerMySQLRestore
		cluster   *apiv1alpha1.PerconaServerMySQL
		backup    *apiv1alpha1.PerconaServerMySQLBackup
		stateDesc string
	}{
		{
			name: "Test without cluster",
			cr: &apiv1alpha1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      restoreName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
					ClusterName: clusterName,
				},
			},
			cluster:   nil,
			stateDesc: fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", clusterName, namespace),
		},
		{
			name: "Test without storage name and backup source",
			cr: &apiv1alpha1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backupName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
					ClusterName: clusterName,
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
			},
			stateDesc: "spec.storageName and backupSource.storage are empty",
		},
		{
			name: "Test with empty destination in backup source",
			cr: &apiv1alpha1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      restoreName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
					ClusterName: clusterName,
					BackupSource: &apiv1alpha1.PerconaServerMySQLBackupStatus{
						Storage: &apiv1alpha1.BackupStorageSpec{},
					},
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
			},
			stateDesc: "backupSource.storage.destination is empty",
		},
		{
			name: "Test without PerconaServerMySQLBackup",
			cr: &apiv1alpha1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      restoreName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
					ClusterName: clusterName,
					BackupName:  backupName,
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
			},
			stateDesc: fmt.Sprintf("PerconaServerMySQLBackup %s in namespace %s is not found", backupName, namespace),
		},
		{
			name: "Test without backup storage in cluster",
			cr: &apiv1alpha1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      restoreName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
					ClusterName: clusterName,
					BackupName:  backupName,
				},
			},
			backup: &apiv1alpha1.PerconaServerMySQLBackup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backupName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
					ClusterName: clusterName,
					StorageName: storageName,
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Storages: make(map[string]*apiv1alpha1.BackupStorageSpec),
					},
				},
			},
			stateDesc: fmt.Sprintf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", storageName),
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
			if tt.backup != nil {
				cb.WithObjects(tt.backup)
			}

			r := PerconaServerMySQLRestoreReconciler{
				Client: cb.Build(),
				Scheme: scheme,
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
			cr := &apiv1alpha1.PerconaServerMySQLRestore{}
			err = r.Get(ctx, types.NamespacedName{
				Name:      tt.cr.Name,
				Namespace: tt.cr.Namespace,
			}, cr)
			if err != nil {
				t.Fatal(err, "failed to get restore")
			}
			if cr.Status.StateDesc != tt.stateDesc {
				t.Fatalf("expected stateDesc %s, got %s", tt.stateDesc, cr.Status.StateDesc)
			}
			if cr.Status.State != apiv1alpha1.RestoreError {
				t.Fatalf("expected state %s, got %s", apiv1alpha1.RestoreError, cr.Status.State)
			}
		})
	}
}

func readDefaultCRRestore(name, namespace string) (*apiv1alpha1.PerconaServerMySQLRestore, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "restore.yaml"))
	if err != nil {
		return nil, err
	}

	cr := &apiv1alpha1.PerconaServerMySQLRestore{}

	if err := yaml.Unmarshal(data, cr); err != nil {
		return nil, err
	}

	cr.Name = name
	cr.Namespace = namespace
	return cr, nil
}
