package psrestore

import (
	"context"
	"fmt"
	"testing"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
	fakestorage "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage/fake"
)

func TestRestoreStatusErrStateDesc(t *testing.T) {
	namespace := "some-namespace"
	clusterName := "cluster1"
	backupName := "backup1"
	restoreName := "restore1"
	storageName := "some-storage"

	cr := readDefaultRestore(t, restoreName, namespace)
	tests := []struct {
		name          string
		cr            *apiv1.PerconaServerMySQLRestore
		cluster       *apiv1.PerconaServerMySQL
		objects       []runtime.Object
		stateDesc     string
		shouldSucceed bool
	}{
		{
			name:      "without cluster",
			cr:        cr.DeepCopy(),
			cluster:   nil,
			stateDesc: fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", clusterName, namespace),
		},
		{
			name: "without storage name and backup source",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.ClusterName = clusterName
			}),
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{},
			},
			stateDesc: "backupName and backupSource are empty",
		},
		{
			name: "with empty destination in backup source",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.BackupSource = &apiv1.PerconaServerMySQLBackupStatus{
					Storage: &apiv1.BackupStorageSpec{},
				}
			}),
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{},
			},
			stateDesc: "backupSource.destination is empty",
		},
		{
			name: "with empty storage in backup source",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.BackupSource = &apiv1.PerconaServerMySQLBackupStatus{
					Destination: "some-destination",
				}
			}),
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{},
			},
			stateDesc: "backupSource.storage is empty",
		},
		{
			name: "without PerconaServerMySQLBackup",
			cr:   cr,
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{},
			},
			stateDesc: fmt.Sprintf("PerconaServerMySQLBackup %s in namespace %s is not found", backupName, namespace),
		},
		{
			name: "without backup storage in cluster",
			cr:   cr,
			objects: []runtime.Object{
				&apiv1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
						ClusterName: clusterName,
						StorageName: storageName,
					},
				},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Storages: make(map[string]*apiv1.BackupStorageSpec),
					},
				},
			},
			stateDesc: fmt.Sprintf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", storageName),
		},
		{
			name: "without secret",
			cr:   cr,
			objects: []runtime.Object{
				&apiv1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
						ClusterName: clusterName,
						StorageName: storageName,
					},
				},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Storages: map[string]*apiv1.BackupStorageSpec{
							storageName: {
								S3: &apiv1.BackupStorageS3Spec{
									CredentialsSecret: "aws-secret",
								},
								Type: apiv1.BackupStorageS3,
							},
						},
						InitImage: "operator-image",
					},
				},
			},
			stateDesc: "secrets aws-secret not found",
		},
		{
			name: "should succeed",
			cr:   cr,
			objects: []runtime.Object{
				&apiv1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
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
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Storages: map[string]*apiv1.BackupStorageSpec{
							storageName: {
								S3: &apiv1.BackupStorageS3Spec{
									Bucket:            "some-bucket",
									CredentialsSecret: "aws-secret",
								},
								Type: apiv1.BackupStorageS3,
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
			objects: []runtime.Object{
				&apiv1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
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
				&apiv1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "running-restore",
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreRunning,
					},
				},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Storages: map[string]*apiv1.BackupStorageSpec{
							storageName: {
								S3: &apiv1.BackupStorageS3Spec{
									CredentialsSecret: "aws-secret",
								},
								Type: apiv1.BackupStorageS3,
							},
						},
						InitImage: "operator-image",
					},
				},
			},
			stateDesc:     "PerconaServerMySQLRestore running-restore is already running",
			shouldSucceed: true,
		},
		{
			name: "with new, failed, errored and succeeded restore",
			cr:   cr,
			objects: []runtime.Object{
				&apiv1.PerconaServerMySQLBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
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
				&apiv1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "new-restore",
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreNew,
					},
				},
				&apiv1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "failed-restore",
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreFailed,
					},
				},
				&apiv1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "succeeded-restore",
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreSucceeded,
					},
				},
				&apiv1.PerconaServerMySQLRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "error-restore",
						Namespace: namespace,
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: clusterName,
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreError,
					},
				},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Storages: map[string]*apiv1.BackupStorageSpec{
							storageName: {
								S3: &apiv1.BackupStorageS3Spec{
									CredentialsSecret: "aws-secret",
									Bucket:            "some-bucket",
								},
								Type: apiv1.BackupStorageS3,
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
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.objects = append(tt.objects, tt.cr)
			if tt.cluster != nil {
				if tt.cluster.Spec.Backup == nil {
					tt.cluster.Spec.Backup = &apiv1.BackupSpec{}
				}
				tt.cluster.Spec.Backup.InitImage = "operator-image"
				tt.objects = append(tt.objects, tt.cluster)
				tt.objects = append(tt.objects,
					&appsv1.StatefulSet{
						ObjectMeta: metav1.ObjectMeta{
							Name:      mysql.Name(tt.cluster),
							Namespace: namespace,
						},
					})
			}
			cl := buildFakeClient(t, tt.objects...)
			r := reconciler(cl)
			r.NewStorageClient = func(_ context.Context, opts storage.Options) (storage.Storage, error) {
				defaultFakeClient, err := fakestorage.NewFakeClient(ctx, opts)
				if err != nil {
					return nil, err
				}
				return &fakeStorageClient{Storage: defaultFakeClient}, nil
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
			cr := &apiv1.PerconaServerMySQLRestore{}
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
					t.Fatalf("expected state %s, got %s", apiv1.RestoreError, cr.Status.State)
				}
			} else {
				if cr.Status.State != apiv1.RestoreError {
					t.Fatalf("expected state %s, got %s", apiv1.RestoreError, cr.Status.State)
				}
			}
		})
	}
}

func TestRestorerValidate(t *testing.T) {
	ctx := context.Background()

	const clusterName = "test-cluster"
	const namespace = "namespace"
	const backupName = clusterName + "-backup"
	const restoreName = clusterName + "-restore"
	const s3SecretName = "my-cluster-name-backup-s3"
	const gcsSecretName = "my-cluster-name-backup-gcs"
	const azureSecretName = "azure-secret"

	cluster := readDefaultCluster(t, clusterName, namespace)
	cluster.Spec.Backup.Storages["azure-blob"] = &apiv1.BackupStorageSpec{
		Type: apiv1.BackupStorageAzure,
		Azure: &apiv1.BackupStorageAzureSpec{
			ContainerName:     "some-bucket",
			CredentialsSecret: azureSecretName,
		},
	}
	cluster.Spec.Backup.Storages["gcs"] = &apiv1.BackupStorageSpec{
		Type: apiv1.BackupStorageGCS,
		GCS: &apiv1.BackupStorageGCSSpec{
			Bucket:            "some-bucket",
			CredentialsSecret: gcsSecretName,
		},
	}
	cluster.Spec.Backup.Storages["s3-us-west"].S3.CredentialsSecret = s3SecretName

	s3Bcp := readDefaultBackup(t, backupName, namespace)
	s3Bcp.Spec.StorageName = "s3-us-west"
	s3Bcp.Status.Destination.SetS3Destination("some-dest", "dest")
	s3Bcp.Status.Storage.S3 = &apiv1.BackupStorageS3Spec{
		Bucket:            "some-bucket",
		CredentialsSecret: s3SecretName,
	}
	s3Bcp.Status.State = apiv1.BackupSucceeded
	s3Bcp.Status.Storage.Type = apiv1.BackupStorageS3

	azureBcp := readDefaultBackup(t, backupName, namespace)
	azureBcp.Spec.StorageName = "azure-blob"
	azureBcp.Status.Destination.SetAzureDestination("some-dest", "dest")
	azureBcp.Status.Storage.Azure = &apiv1.BackupStorageAzureSpec{
		ContainerName:     "some-bucket",
		CredentialsSecret: azureSecretName,
	}
	azureBcp.Status.State = apiv1.BackupSucceeded
	azureBcp.Status.Storage.Type = apiv1.BackupStorageAzure

	gcsBcp := readDefaultBackup(t, backupName, namespace)
	gcsBcp.Spec.StorageName = "gcs"
	gcsBcp.Status.Destination.SetAzureDestination("some-dest", "dest")
	gcsBcp.Status.Storage.GCS = &apiv1.BackupStorageGCSSpec{
		Bucket:            "some-bucket",
		CredentialsSecret: gcsSecretName,
	}
	gcsBcp.Status.State = apiv1.BackupSucceeded
	gcsBcp.Status.Storage.Type = apiv1.BackupStorageGCS

	cr := readDefaultRestore(t, restoreName, namespace)
	cr.Spec.BackupName = backupName
	s3Secret := readDefaultS3Secret(t, s3SecretName, namespace)
	azureSecret := readDefaultAzureSecret(t, azureSecretName, namespace)
	gcsSecret := readDefaultGCSSecret(t, gcsSecretName, namespace)

	tests := []struct {
		name                  string
		cr                    *apiv1.PerconaServerMySQLRestore
		bcp                   *apiv1.PerconaServerMySQLBackup
		cluster               *apiv1.PerconaServerMySQL
		objects               []runtime.Object
		expectedErr           string
		fakeStorageClientFunc storage.NewClientFunc
	}{
		{
			name:    "s3",
			cr:      cr.DeepCopy(),
			cluster: cluster.DeepCopy(),
			bcp:     s3Bcp.DeepCopy(),
			objects: []runtime.Object{
				s3Secret,
			},
		},
		{
			name:        "s3 without secrets",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         s3Bcp.DeepCopy(),
			expectedErr: "failed to validate job: secrets my-cluster-name-backup-s3 not found",
		},
		{
			name: "s3 without credentialsSecret",
			cr:   cr.DeepCopy(),
			bcp:  s3Bcp.DeepCopy(),
			objects: []runtime.Object{
				s3Secret,
			},
			cluster: updateResource(cluster.DeepCopy(), func(cluster *apiv1.PerconaServerMySQL) {
				cluster.Spec.Backup.Storages["s3-us-west"].S3.CredentialsSecret = ""
			}),
			expectedErr: "",
		},
		{
			name:        "s3 with failing storage client",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         s3Bcp.DeepCopy(),
			expectedErr: "failed to validate storage: failed to list objects: failListObjects",
			objects: []runtime.Object{
				s3Secret,
			},
			fakeStorageClientFunc: func(_ context.Context, opts storage.Options) (storage.Storage, error) {
				return &fakeStorageClient{failListObjects: true}, nil
			},
		},
		{
			name: "s3 without provided bucket",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.BackupSource = &apiv1.PerconaServerMySQLBackupStatus{
					Destination: s3Bcp.Status.Destination,
					Storage: &apiv1.BackupStorageSpec{
						S3:   s3Bcp.Status.Storage.S3,
						Type: apiv1.BackupStorageS3,
					},
				}
				cr.Spec.BackupSource.Storage.S3.Bucket = ""
			},
			),
			cluster: cluster.DeepCopy(),
			objects: []runtime.Object{
				s3Secret,
			},
		},
		{
			name:        "s3 with empty bucket",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         s3Bcp.DeepCopy(),
			expectedErr: "failed to validate storage: backup not found",
			objects: []runtime.Object{
				s3Secret,
			},
			fakeStorageClientFunc: func(_ context.Context, opts storage.Options) (storage.Storage, error) {
				return &fakeStorageClient{emptyListObjects: true}, nil
			},
		},
		{
			name:    "gcs",
			cr:      cr.DeepCopy(),
			cluster: cluster.DeepCopy(),
			bcp:     gcsBcp.DeepCopy(),
			objects: []runtime.Object{
				gcsSecret,
			},
		},
		{
			name:        "gcs without secrets",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         gcsBcp.DeepCopy(),
			expectedErr: "failed to validate job: secrets my-cluster-name-backup-gcs not found",
		},
		{
			name:        "gcs with failing storage client",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         gcsBcp.DeepCopy(),
			expectedErr: "failed to validate storage: failed to list objects: failListObjects",
			objects: []runtime.Object{
				gcsSecret,
			},
			fakeStorageClientFunc: func(_ context.Context, opts storage.Options) (storage.Storage, error) {
				return &fakeStorageClient{failListObjects: true}, nil
			},
		},
		{
			name: "gcs without provided bucket",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.BackupSource = &apiv1.PerconaServerMySQLBackupStatus{
					Destination: gcsBcp.Status.Destination,
					Storage: &apiv1.BackupStorageSpec{
						GCS:  gcsBcp.Status.Storage.GCS,
						Type: apiv1.BackupStorageGCS,
					},
				}
				cr.Spec.BackupSource.Storage.GCS.Bucket = ""
			},
			),
			cluster: cluster.DeepCopy(),
			objects: []runtime.Object{
				gcsSecret,
			},
		},
		{
			name:        "gcs with empty bucket",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         gcsBcp.DeepCopy(),
			expectedErr: "failed to validate storage: backup not found",
			objects: []runtime.Object{
				gcsSecret,
			},
			fakeStorageClientFunc: func(_ context.Context, opts storage.Options) (storage.Storage, error) {
				return &fakeStorageClient{emptyListObjects: true}, nil
			},
		},
		{
			name:    "azure",
			bcp:     azureBcp,
			cr:      cr.DeepCopy(),
			cluster: cluster.DeepCopy(),
			objects: []runtime.Object{
				azureSecret,
			},
		},
		{
			name:        "azure without secrets",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         azureBcp,
			expectedErr: "failed to validate job: secrets azure-secret not found",
		},
		{
			name:        "azure with failing storage client",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         azureBcp,
			expectedErr: "failed to validate storage: failed to list objects: failListObjects",
			objects: []runtime.Object{
				azureSecret,
			},
			fakeStorageClientFunc: func(_ context.Context, opts storage.Options) (storage.Storage, error) {
				return &fakeStorageClient{failListObjects: true}, nil
			},
		},
		{
			name: "azure without provided bucket",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLRestore) {
				cr.Spec.BackupName = ""
				cr.Spec.BackupSource = &apiv1.PerconaServerMySQLBackupStatus{
					Destination: azureBcp.Status.Destination,
					Storage: &apiv1.BackupStorageSpec{
						Azure: azureBcp.Status.Storage.Azure,
						Type:  apiv1.BackupStorageAzure,
					},
				}
				cr.Spec.BackupSource.Storage.Azure.ContainerName = ""
			},
			),
			cluster: cluster.DeepCopy(),
			objects: []runtime.Object{
				azureSecret,
			},
		},
		{
			name:        "azure with empty bucket",
			cr:          cr.DeepCopy(),
			cluster:     cluster.DeepCopy(),
			bcp:         azureBcp,
			expectedErr: "failed to validate storage: backup not found",
			objects: []runtime.Object{
				azureSecret,
			},
			fakeStorageClientFunc: func(_ context.Context, opts storage.Options) (storage.Storage, error) {
				return &fakeStorageClient{emptyListObjects: true}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fakeStorageClientFunc == nil {
				tt.fakeStorageClientFunc = func(ctx context.Context, opts storage.Options) (storage.Storage, error) {
					defaultFakeClient, err := fakestorage.NewFakeClient(ctx, opts)
					if err != nil {
						return nil, err
					}
					return &fakeStorageClient{defaultFakeClient, false, false}, nil
				}
			}

			if err := tt.cluster.CheckNSetDefaults(ctx, new(platform.ServerVersion)); err != nil {
				t.Fatal(err)
			}
			if tt.bcp != nil {
				tt.objects = append(tt.objects, tt.bcp)
			}
			tt.objects = append(tt.objects, tt.cr, tt.cluster)

			cl := buildFakeClient(t, tt.objects...)
			r := reconciler(cl)
			r.NewStorageClient = tt.fakeStorageClientFunc

			restorer, err := r.getRestorer(ctx, tt.cr, tt.cluster)
			if err != nil {
				t.Fatal(err)
			}
			err = restorer.Validate(ctx)
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			if errStr != tt.expectedErr {
				t.Fatal("expected err:", tt.expectedErr, "; got:", errStr)
			}
		})
	}
}

type fakeStorageClient struct {
	storage.Storage
	failListObjects  bool
	emptyListObjects bool
}

func (c *fakeStorageClient) ListObjects(_ context.Context, _ string) ([]string, error) {
	switch {
	case c.emptyListObjects:
		return nil, nil
	case c.failListObjects:
		return nil, errors.New("failListObjects")
	}
	return []string{"some-dest/backup1", "some-dest/backup2"}, nil
}
