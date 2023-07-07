package psrestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func TestRestoreStatusErrStateDesc(t *testing.T) {
	namespace := "some-namespace"
	clusterName := "cluster1"
	backupName := "backup1"
	restoreName := "restore1"
	storageName := "some-storage"

	cr, err := readDefaultCRRestore(restoreName, namespace)
	if err != nil {
		t.Fatal(err, "failed to read restore file")
	}

	tests := []struct {
		name          string
		cr            *apiv1alpha1.PerconaServerMySQLRestore
		cluster       *apiv1alpha1.PerconaServerMySQL
		objects       []client.Object
		stateDesc     string
		shouldSucceed bool
	}{
		{
			name:      "without cluster",
			cr:        cr,
			cluster:   nil,
			stateDesc: fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", clusterName, namespace),
		},
		{
			name: "without storage name and backup source",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.ClusterName = clusterName
			}),
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
			},
			stateDesc: "backupName and backupSource are empty",
		},
		{
			name: "with empty destination in backup source",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.BackupSource = &apiv1alpha1.PerconaServerMySQLBackupStatus{
					Storage: &apiv1alpha1.BackupStorageSpec{},
				}
			}),
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
			},
			stateDesc: "backupSource.destination is empty",
		},
		{
			name: "with empty storage in backup source",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.BackupSource = &apiv1alpha1.PerconaServerMySQLBackupStatus{
					Destination: "some-destination",
				}
			}),
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
			},
			stateDesc: "backupSource.storage is empty",
		},
		{
			name: "without PerconaServerMySQLBackup",
			cr:   cr,
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
			name: "without backup storage in cluster",
			cr:   cr,
			objects: []client.Object{
				&apiv1alpha1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
						ClusterName: clusterName,
						StorageName: storageName,
					},
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
		{
			name: "without secret",
			cr:   cr,
			objects: []client.Object{
				&apiv1alpha1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
						ClusterName: clusterName,
						StorageName: storageName,
					},
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Storages: map[string]*apiv1alpha1.BackupStorageSpec{
							storageName: {
								S3: &apiv1alpha1.BackupStorageS3Spec{
									CredentialsSecret: "aws-secret",
								},
								Type: apiv1alpha1.BackupStorageS3,
							},
						},
						InitImage: "operator-image",
					},
				},
			},
			stateDesc: "secret aws-secret is not found",
		},
		{
			name: "should succeed",
			cr:   cr,
			objects: []client.Object{
				&apiv1alpha1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
						ClusterName: clusterName,
						StorageName: storageName,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "aws-secret",
						Namespace: namespace,
					},
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Storages: map[string]*apiv1alpha1.BackupStorageSpec{
							storageName: {
								S3: &apiv1alpha1.BackupStorageS3Spec{
									CredentialsSecret: "aws-secret",
								},
								Type: apiv1alpha1.BackupStorageS3,
							},
						},
						InitImage: "operator-image",
					},
				},
			},
			stateDesc:     "",
			shouldSucceed: true,
		},
		{
			name: "with running restore",
			cr:   cr,
			objects: []client.Object{
				&apiv1alpha1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
						ClusterName: clusterName,
						StorageName: storageName,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "aws-secret",
						Namespace: namespace,
					},
				},
				&apiv1alpha1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "running-restore",
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1alpha1.PerconaServerMySQLRestoreStatus{
						State: apiv1alpha1.RestoreRunning,
					},
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Storages: map[string]*apiv1alpha1.BackupStorageSpec{
							storageName: {
								S3: &apiv1alpha1.BackupStorageS3Spec{
									CredentialsSecret: "aws-secret",
								},
								Type: apiv1alpha1.BackupStorageS3,
							},
						},
						InitImage: "operator-image",
					},
				},
			},
			stateDesc:     "PerconaServerMySQLRestore running-restore is already running",
			shouldSucceed: false,
		},
		{
			name: "with new, failed, errored and succeeded restore",
			cr:   cr,
			objects: []client.Object{
				&apiv1alpha1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
						ClusterName: clusterName,
						StorageName: storageName,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "aws-secret",
						Namespace: namespace,
					},
				},
				&apiv1alpha1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "new-restore",
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1alpha1.PerconaServerMySQLRestoreStatus{
						State: apiv1alpha1.RestoreNew,
					},
				},
				&apiv1alpha1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "failed-restore",
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1alpha1.PerconaServerMySQLRestoreStatus{
						State: apiv1alpha1.RestoreFailed,
					},
				},
				&apiv1alpha1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "succeeded-restore",
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1alpha1.PerconaServerMySQLRestoreStatus{
						State: apiv1alpha1.RestoreSucceeded,
					},
				},
				&apiv1alpha1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "error-restore",
						Namespace: namespace,
					},
					Spec: apiv1alpha1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1alpha1.PerconaServerMySQLRestoreStatus{
						State: apiv1alpha1.RestoreError,
					},
				},
			},
			cluster: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Storages: map[string]*apiv1alpha1.BackupStorageSpec{
							storageName: {
								S3: &apiv1alpha1.BackupStorageS3Spec{
									CredentialsSecret: "aws-secret",
								},
								Type: apiv1alpha1.BackupStorageS3,
							},
						},
						InitImage: "operator-image",
					},
				},
			},
			stateDesc:     "",
			shouldSucceed: true,
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
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr).WithStatusSubresource(tt.cr).WithObjects(tt.objects...)
			if tt.cluster != nil {
				cb.WithObjects(tt.cluster, &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      mysql.Name(tt.cluster),
						Namespace: namespace,
					},
				})
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
			if tt.shouldSucceed {
				if cr.Status.State != "" {
					t.Fatalf("expected state %s, got %s", apiv1alpha1.RestoreError, cr.Status.State)
				}
			} else {
				if cr.Status.State != apiv1alpha1.RestoreError {
					t.Fatalf("expected state %s, got %s", apiv1alpha1.RestoreError, cr.Status.State)
				}
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

func updateResource[T any](cr *T, updateFuncs ...func(cr *T)) *T {
	for _, f := range updateFuncs {
		f(cr)
	}
	return cr
}
