package psbackup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func TestBackupStatusErrStateDesc(t *testing.T) {
	namespace := "some-namespace"

	cr, err := readDefaultCRBackup("some-name", namespace)
	if err != nil {
		t.Fatal(err, "failed to read default backup")
	}
	cluster, err := readDefaultCR("ps-cluster1", namespace)
	if err != nil {
		t.Fatal(err, "failed to read default cr")
	}

	tests := []struct {
		name      string
		cluster   *apiv1.PerconaServerMySQL
		cr        *apiv1.PerconaServerMySQLBackup
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
			cluster: updateResource(
				cluster.DeepCopy(),
				func(cr *apiv1.PerconaServerMySQL) {
					cr.Namespace = namespace

					cr.Spec.Backup = &apiv1.BackupSpec{
						Image:    "some-image",
						Enabled:  false,
						Storages: make(map[string]*apiv1.BackupStorageSpec),
					}
				},
			),
			stateDesc: "spec.backup not found in PerconaServerMySQL CustomResource or backups are disabled",
		},
		{
			name: "without storage",
			cr:   cr,
			cluster: updateResource(
				cluster.DeepCopy(),
				func(cr *apiv1.PerconaServerMySQL) {
					cr.Namespace = namespace
					cr.Spec.Backup = &apiv1.BackupSpec{
						Image:    "some-image",
						Enabled:  true,
						Storages: make(map[string]*apiv1.BackupStorageSpec),
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
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr).WithStatusSubresource(tt.cr)
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
			cr := &apiv1.PerconaServerMySQLBackup{}
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
			if cr.Status.State != apiv1.BackupError {
				t.Fatalf("expected state %s, got %s", apiv1.RestoreError, cr.Status.State)
			}

			// Backup with an error state should not be reconciled.
			// We can verify this by using clientWithGetCount.
			//
			// If the reconcile loop calls Get more than once,
			// it means the loop continued running instead of stopping
			// after the state check.
			r.Client = &clientWithGetCount{
				Count:  1,
				Client: r.Client,
			}
			_, err = r.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.cr.Name,
					Namespace: tt.cr.Namespace,
				},
			})
			if err != nil {
				t.Fatal(err, "failed to reconcile")
			}
		})
	}
}

type clientWithGetCount struct {
	Count int

	client.Client
}

func (c *clientWithGetCount) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.Count <= 0 {
		return errors.New("unexpected Get call from client")
	}
	c.Count--
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestStateDescCleanup(t *testing.T) {
	ctx := t.Context()
	scheme := runtime.NewScheme()

	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	err = apiv1.AddToScheme(scheme)
	require.NoError(t, err)

	const namespace = "state-desc-cleanup"
	const storageName = "s3-us-west"

	cluster, err := readDefaultCR("ps-cluster1", namespace)
	require.NoError(t, err)

	cluster.Status.MySQL.State = apiv1.StateReady
	cluster.Spec.InitContainer.Image = "init-image"

	cr, err := readDefaultCRBackup("some-name", namespace)
	require.NoError(t, err)

	cr.Spec.ClusterName = cluster.Name
	cr.Spec.StorageName = storageName

	storage := cluster.Spec.Backup.Storages[storageName]

	newSecret := func(name string, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Data:       data,
		}
	}

	s3Secret := newSecret(storage.S3.CredentialsSecret, map[string][]byte{
		secret.CredentialsAWSAccessKey: []byte("access-key"),
		secret.CredentialsAWSSecretKey: []byte("secret-key"),
	})
	userSecret := newSecret(cluster.InternalSecretName(), map[string][]byte{
		string(apiv1.UserOperator): []byte("access-key"),
	})

	tests := []struct {
		name          string
		cr            *apiv1.PerconaServerMySQLBackup
		expectedState apiv1.BackupState
		failedJob     bool
	}{
		{
			name: "clean description on success",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Status.State = apiv1.BackupRunning
				cr.Status.StateDesc = "to-delete"
			}),
			expectedState: apiv1.BackupSucceeded,
		},
		{
			name: "clean description on fail",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Status.State = apiv1.BackupRunning
				cr.Status.StateDesc = "to-delete"
			}),
			failedJob:     true,
			expectedState: apiv1.BackupFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond := batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}
			if tt.failedJob {
				cond.Type = batchv1.JobFailed
			}

			job, err := xtrabackup.Job(cluster.DeepCopy(), tt.cr, "dest", "init-image", storage)
			require.NoError(t, err)

			job.Status.Conditions = append(job.Status.Conditions, cond)

			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr, cluster.DeepCopy(), s3Secret, userSecret, job).WithStatusSubresource(tt.cr, cluster.DeepCopy(), s3Secret, job)
			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
			}

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
			require.NoError(t, err)

			cr := new(apiv1.PerconaServerMySQLBackup)
			err = r.Get(ctx, types.NamespacedName{Name: tt.cr.Name, Namespace: tt.cr.Namespace}, cr)
			require.NoError(t, err)

			if cr.Status.State != apiv1.BackupFailed && cr.Status.State != apiv1.BackupSucceeded {
				t.Fatalf("wrong test setup. backup should succeeded or failed. Got: %s", cr.Status.State)
			}

			assert.Equal(t, cr.Status.State, tt.expectedState)
			assert.Empty(t, cr.Status.StateDesc)
		})
	}
}

func TestCheckFinalizers(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))
	namespace := "some-namespace"

	cr, err := readDefaultCRBackup("some-name", namespace)
	require.NoError(t, err)

	tests := []struct {
		name               string
		cr                 *apiv1.PerconaServerMySQLBackup
		expectedFinalizers []string
		finalizerJobFail   bool
	}{
		{
			name: "without finalizers",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{}
				cr.Status.State = apiv1.BackupError
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with finalizer and starting state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupStarting
			}),
			expectedFinalizers: []string{naming.FinalizerDeleteBackup},
		},
		{
			name: "with finalizer and running state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupRunning
			}),
			expectedFinalizers: []string{naming.FinalizerDeleteBackup},
		},
		{
			name: "with finalizer and error state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupError
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with finalizer and new state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupNew
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with failing finalizer and succeeded state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupSucceeded
			}),
			finalizerJobFail:   true,
			expectedFinalizers: []string{naming.FinalizerDeleteBackup},
		},
		{
			name: "with successful finalizer and succeeded state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupSucceeded
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with successful finalizer, unknown finalizer and succeeded state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup, "unknown-finalizer"}
				cr.Status.State = apiv1.BackupSucceeded
			}),
			expectedFinalizers: []string{"unknown-finalizer"},
		},
		{
			name: "with failing finalizer and failed state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupFailed
			}),
			finalizerJobFail:   true,
			expectedFinalizers: nil,
		},
		{
			name: "with successful finalizer and failed state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup}
				cr.Status.State = apiv1.BackupFailed
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with successful finalizer, unknown finalizer and failed state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{naming.FinalizerDeleteBackup, "unknown-finalizer"}
				cr.Status.State = apiv1.BackupFailed
			}),
			expectedFinalizers: []string{"unknown-finalizer"},
		},
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-secret",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			secret.CredentialsAWSAccessKey: []byte("access-key"),
			secret.CredentialsAWSSecretKey: []byte("secret-key"),
		},
	}
	storage := &apiv1.BackupStorageSpec{
		Type: apiv1.BackupStorageS3,
		S3: &apiv1.BackupStorageS3Spec{
			Bucket:            "some-bucket",
			CredentialsSecret: "some-secret",
		},
		Labels: make(map[string]string),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := tt.cr.DeepCopy()
			cr.Status.Storage = storage

			job := xtrabackup.GetDeleteJob(new(apiv1.PerconaServerMySQL), cr, new(xtrabackup.BackupConfig))
			cond := batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}
			if tt.finalizerJobFail {
				cond.Type = batchv1.JobFailed
			}
			job.Status.Conditions = append(job.Status.Conditions, cond)

			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr, sec, job)
			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
			}

			require.NoError(t, r.Delete(t.Context(), cr))

			// Getting backup with deletion timestamp
			if err := r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr); err != nil {
				if k8serrors.IsNotFound(err) && len(tt.expectedFinalizers) == 0 {
					return
				}
				t.Fatal(err)
			}

			assert.NoError(t, r.checkFinalizers(t.Context(), cr))

			// Getting backup with updated finalizers
			if err := r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr); err != nil {
				if k8serrors.IsNotFound(err) && len(tt.expectedFinalizers) == 0 {
					return
				}
				t.Fatal(err)
			}

			assert.Equal(t, tt.expectedFinalizers, cr.Finalizers)
		})
	}
}

func TestRunningState(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}
	namespace := "some-namespace"

	cr, err := readDefaultCRBackup("some-name", namespace)
	if err != nil {
		t.Fatal(err, "failed to read default backup")
	}
	cr.Status.State = apiv1.BackupStarting
	cr.Spec.StorageName = "s3-us-west"
	cr.Spec.SourcePod = "ps-cluster1-mysql-0"
	cluster, err := readDefaultCR("ps-cluster1", namespace)
	if err != nil {
		t.Fatal(err, "failed to read default cr")
	}
	cluster.Status.MySQL.State = apiv1.StateReady
	tests := []struct {
		name          string
		cr            *apiv1.PerconaServerMySQLBackup
		cluster       *apiv1.PerconaServerMySQL
		sidecarClient *fakeSidecarClient
		state         apiv1.BackupState
	}{
		{
			name:          "not running",
			cr:            cr.DeepCopy(),
			cluster:       cluster.DeepCopy(),
			state:         apiv1.BackupStarting,
			sidecarClient: &fakeSidecarClient{},
		},
		{
			name:    "other backup is running",
			cr:      cr.DeepCopy(),
			cluster: cluster.DeepCopy(),
			state:   apiv1.BackupStarting,
			sidecarClient: &fakeSidecarClient{
				destination: "other-container",
			},
		},
		{
			name:    "running",
			cr:      cr.DeepCopy(),
			cluster: cluster.DeepCopy(),
			state:   apiv1.BackupRunning,
			sidecarClient: &fakeSidecarClient{
				destination: "container",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage, ok := tt.cluster.Spec.Backup.Storages["s3-us-west"]
			if !ok {
				t.Fatal("storage not found")
			}
			job, err := xtrabackup.Job(tt.cluster, tt.cr, "s3://bucket/container", "init-image", storage)
			if err != nil {
				t.Fatal(err)
			}
			job.Status.Active = 1
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql-0",
					Namespace: tt.cr.Namespace,
				},
			}
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr, tt.cluster, job, pod).WithStatusSubresource(tt.cr, tt.cluster, job)

			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
				NewSidecarClient: func(srcNode string) xtrabackup.SidecarClient {
					return tt.sidecarClient
				},
			}
			_, err = r.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.cr.Name,
					Namespace: tt.cr.Namespace,
				},
			})
			if err != nil {
				t.Fatal(err, "failed to reconcile")
			}
			cr := &apiv1.PerconaServerMySQLBackup{}
			err = r.Get(ctx, types.NamespacedName{
				Name:      tt.cr.Name,
				Namespace: tt.cr.Namespace,
			}, cr)
			if err != nil {
				t.Fatal(err, "failed to get backup")
			}
			if cr.Status.State != tt.state {
				t.Fatalf("expected state %s, got %s (StateDesc: %s)", tt.state, cr.Status.State, cr.Status.StateDesc)
			}
		})
	}
}

func TestGetBackupSource(t *testing.T) {
	scheme := runtime.NewScheme()

	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	err = apiv1.AddToScheme(scheme)
	require.NoError(t, err)

	ctx := context.Background()

	tests := []struct {
		name        string
		cr          *apiv1.PerconaServerMySQLBackup
		cluster     *apiv1.PerconaServerMySQL
		want        string
		expectedErr string
	}{
		{
			name: "sourceHost from backup",
			cr: &apiv1.PerconaServerMySQLBackup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backup",
					Namespace: "test-ns",
				},
				Spec: apiv1.PerconaServerMySQLBackupSpec{
					SourcePod: "ps-cluster1-mysql-0",
				},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "test-ns",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{SourcePod: "ps-cluster1-mysql-1"},
				},
			},
			want: "ps-cluster1-mysql-0.ps-cluster1-mysql.test-ns",
		},
		{
			name: "host from cluster",
			cr: &apiv1.PerconaServerMySQLBackup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backup",
					Namespace: "test-ns",
				},
				Spec: apiv1.PerconaServerMySQLBackupSpec{},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "test-ns",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{SourcePod: "ps-cluster1-mysql-1"},
				},
			},
			want: "ps-cluster1-mysql-1.ps-cluster1-mysql.test-ns",
		},
		{
			name: "single node cluster",
			cr: &apiv1.PerconaServerMySQLBackup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backup",
					Namespace: "test-ns",
				},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "test-ns",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						PodSpec: apiv1.PodSpec{Size: 1},
					},
					Backup: &apiv1.BackupSpec{},
				},
			},
			want: "ps-cluster1-mysql-0.ps-cluster1-mysql.test-ns",
		},
		{
			name: "async cluster, orchestrator off, no host",
			cr: &apiv1.PerconaServerMySQLBackup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backup",
					Namespace: "test-ns",
				},
			},
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "test-ns",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Backup: &apiv1.BackupSpec{},
					Unsafe: apiv1.UnsafeFlags{
						Orchestrator: true,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: false,
					},
				},
			},
			want:        "",
			expectedErr: "Orchestrator is disabled. Please specify the backup source explicitly using either spec.backup.sourcePod in the cluster CR or spec.sourcePod in the PerconaServerMySQLBackup resource.",
		},
	}

	for _, tt := range tests {
		objs := []client.Object{
			tt.cluster,
			tt.cr,
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql-0",
					Namespace: "test-ns",
				},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql-1",
					Namespace: "test-ns",
				},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql-2",
					Namespace: "test-ns",
				},
			},
		}

		cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(tt.cr)

		t.Run(tt.name, func(t *testing.T) {
			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
			}

			got, err := r.getBackupSource(ctx, tt.cr, tt.cluster)
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

type fakeSidecarClient struct {
	destination string
}

func (f *fakeSidecarClient) GetRunningBackupConfig(ctx context.Context) (*xtrabackup.BackupConfig, error) {
	if f.destination == "" {
		return nil, nil
	}
	return &xtrabackup.BackupConfig{
		Destination: f.destination,
	}, nil
}

func (f *fakeSidecarClient) DeleteBackup(ctx context.Context, name string, cfg xtrabackup.BackupConfig) error {
	return nil
}

func readDefaultCR(name, namespace string) (*apiv1.PerconaServerMySQL, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "cr.yaml"))
	if err != nil {
		return nil, err
	}

	cr := &apiv1.PerconaServerMySQL{}

	if err := yaml.Unmarshal(data, cr); err != nil {
		return nil, err
	}

	cr.Name = name
	cr.Namespace = namespace
	return cr, nil
}

func readDefaultCRBackup(name, namespace string) (*apiv1.PerconaServerMySQLBackup, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "backup.yaml"))
	if err != nil {
		return nil, err
	}

	cr := &apiv1.PerconaServerMySQLBackup{}

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

// fakeClientCmd implements clientcmd.Client interface for testing
type fakeClientCmd struct {
	beginDowntimeError error
}

// Compile-time check to ensure fakeClientCmd implements clientcmd.Client
var _ clientcmd.Client = (*fakeClientCmd)(nil)

func (f *fakeClientCmd) Exec(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	if f.beginDowntimeError != nil {
		return f.beginDowntimeError
	}
	if stdout != nil {
		stdout.Write([]byte(`{"Code":"OK","Message":"success"}`))
	}
	return nil
}

func (f *fakeClientCmd) REST() restclient.Interface {
	return nil
}

func TestPrepareBackupSource(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()

	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	err = apiv1.AddToScheme(scheme)
	require.NoError(t, err)

	namespace := "test-namespace"
	backupSource := "test-mysql-0.test-mysql.test-namespace"

	cr := &apiv1.PerconaServerMySQLBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: namespace,
		},
		Spec: apiv1.PerconaServerMySQLBackupSpec{
			ClusterName: "test-cluster",
		},
	}

	backupPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mysql-0",
			Namespace: namespace,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	orchestratorPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-orc-0",
			Namespace: namespace,
			Labels: map[string]string{
				naming.LabelInstance:  "test-cluster",
				naming.LabelComponent: naming.ComponentOrchestrator,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.ContainersReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	tests := []struct {
		name     string
		cluster  *apiv1.PerconaServerMySQL
		pods     []client.Object
		wantErr  bool
		errorMsg string
	}{
		{
			name: "success with async cluster and orchestrator enabled",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: true,
					},
				},
			},
			pods:    []client.Object{backupPod, orchestratorPod},
			wantErr: false,
		},
		{
			name: "success with group replication cluster (no orchestrator needed)",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeGR,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: false,
					},
				},
			},
			pods:    []client.Object{backupPod},
			wantErr: false,
		},
		{
			name: "success with async cluster but orchestrator disabled",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: false,
					},
				},
			},
			pods:    []client.Object{backupPod},
			wantErr: false,
		},
		{
			name: "error when orchestrator pod not ready",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: true,
					},
				},
			},
			pods:     []client.Object{backupPod},
			wantErr:  true,
			errorMsg: "get ready orchestrator pod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := fake.NewClientBuilder().WithScheme(scheme)
			if len(tt.pods) > 0 {
				cb.WithObjects(tt.pods...)
			}

			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
				ClientCmd:     &fakeClientCmd{},
			}

			testBackupSource := backupSource
			if tt.name == "error with malformed backup source" {
				testBackupSource = "" // empty string to trigger pod not found error
			}

			err := r.prepareBackupSource(ctx, cr, tt.cluster, testBackupSource)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetBackupSourcePod(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()

	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	namespace := "test-namespace"
	podName := "test-mysql-0"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	tests := []struct {
		name         string
		backupSource string
		pods         []client.Object
		wantErr      bool
		errorMsg     string
		wantPodName  string
	}{
		{
			name:         "success with full FQDN",
			backupSource: "test-mysql-0.test-mysql.test-namespace",
			pods:         []client.Object{pod},
			wantErr:      false,
			wantPodName:  "test-mysql-0",
		},
		{
			name:         "success with pod name only",
			backupSource: "test-mysql-0",
			pods:         []client.Object{pod},
			wantErr:      false,
			wantPodName:  "test-mysql-0",
		},
		{
			name:         "error when pod not found",
			backupSource: "nonexistent-pod.test-mysql.test-namespace",
			pods:         []client.Object{},
			wantErr:      true,
			errorMsg:     "get pod",
		},
		{
			name:         "error with empty backup source",
			backupSource: "",
			pods:         []client.Object{},
			wantErr:      true,
			errorMsg:     "pods \"\" not found",
		},
		{
			name:         "error with malformed backup source",
			backupSource: ".",
			pods:         []client.Object{},
			wantErr:      true,
			errorMsg:     "pods \"\" not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := fake.NewClientBuilder().WithScheme(scheme)
			if len(tt.pods) > 0 {
				cb.WithObjects(tt.pods...)
			}

			cl := cb.Build()

			resultPod, err := getBackupSourcePod(ctx, cl, namespace, tt.backupSource)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, resultPod)
				assert.Equal(t, tt.wantPodName, resultPod.Name)
				assert.Equal(t, namespace, resultPod.Namespace)
			}
		})
	}
}

func TestRunPostFinishTasks(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()

	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	err = apiv1.AddToScheme(scheme)
	require.NoError(t, err)

	namespace := "test-namespace"
	backupSource := "test-mysql-0.test-mysql.test-namespace"

	cr := &apiv1.PerconaServerMySQLBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: namespace,
		},
		Spec: apiv1.PerconaServerMySQLBackupSpec{
			ClusterName: "test-cluster",
		},
		Status: apiv1.PerconaServerMySQLBackupStatus{
			BackupSource: backupSource,
		},
	}

	orchestratorPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-orc-0",
			Namespace: namespace,
			Labels: map[string]string{
				naming.LabelInstance:  "test-cluster",
				naming.LabelComponent: naming.ComponentOrchestrator,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.ContainersReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	tests := []struct {
		name      string
		cluster   *apiv1.PerconaServerMySQL
		pods      []client.Object
		clientCmd clientcmd.Client
		wantErr   bool
		errorMsg  string
	}{
		{
			name: "success with async cluster and orchestrator enabled",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: true,
					},
				},
			},
			pods:      []client.Object{orchestratorPod},
			clientCmd: &fakeClientCmd{},
			wantErr:   false,
		},
		{
			name: "skip with group replication cluster",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeGR,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: false,
					},
				},
			},
			pods:      []client.Object{},
			clientCmd: &fakeClientCmd{},
			wantErr:   false,
		},
		{
			name: "skip with async cluster but orchestrator disabled",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: false,
					},
				},
			},
			pods:      []client.Object{},
			clientCmd: &fakeClientCmd{},
			wantErr:   false,
		},
		{
			name: "error when orchestrator pod not ready",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: true,
					},
				},
			},
			pods:      []client.Object{},
			clientCmd: &fakeClientCmd{},
			wantErr:   true,
			errorMsg:  "get ready orchestrator pod",
		},
		{
			name: "error when end downtime fails",
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeAsync,
					},
					Orchestrator: apiv1.OrchestratorSpec{
						Enabled: true,
					},
				},
			},
			pods:      []client.Object{orchestratorPod},
			clientCmd: &fakeClientCmd{beginDowntimeError: fmt.Errorf("orchestrator api error")},
			wantErr:   true,
			errorMsg:  "end downtime for",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := fake.NewClientBuilder().WithScheme(scheme)
			if len(tt.pods) > 0 {
				cb.WithObjects(tt.pods...)
			}

			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
				ClientCmd:     tt.clientCmd,
			}

			testCR := cr.DeepCopy()

			err := r.runPostFinishTasks(ctx, testCR, tt.cluster)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
