package psbackup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

// TestBackupStatusErrStateDesc tests error descriptions in the backup status.
func TestBackupStatusErrStateDesc(t *testing.T) {
	namespace := "some-namespace"

	cr, err := readDefaultCRBackup("some-name", namespace)
	if err != nil {
		t.Fatal(err, "failed to read default backup")
	}
	cluster, err := readDefaultCR("cluster1", namespace)
	if err != nil {
		t.Fatal(err, "failed to read default cr")
	}

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
			cluster: updateResource(
				cluster.DeepCopy(),
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
			cluster: updateResource(
				cluster.DeepCopy(),
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

// TestCheckFinalizers verifies the behavior of finalizers on PerconaServerMySQLBackup objects.
func TestCheckFinalizers(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}
	namespace := "some-namespace"
	cr, err := readDefaultCRBackup("some-name", namespace)
	if err != nil {
		t.Fatal(err, "failed to read default backup")
	}

	tests := []struct {
		name               string
		cr                 *apiv1alpha1.PerconaServerMySQLBackup
		expectedFinalizers []string
		finalizerJobFail   bool
	}{
		{
			name: "without finalizers",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{}
				cr.Status.State = apiv1alpha1.BackupError
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with finalizer and starting state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupStarting
			}),
			expectedFinalizers: []string{finalizerDeleteBackup},
		},
		{
			name: "with finalizer and running state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupRunning
			}),
			expectedFinalizers: []string{finalizerDeleteBackup},
		},
		{
			name: "with finalizer and error state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupError
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with finalizer and new state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupNew
			}),
			expectedFinalizers: nil,
		},
		{
			name: "with failing finalizer and succeeded state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupSucceeded
			}),
			finalizerJobFail:   true,
			expectedFinalizers: []string{finalizerDeleteBackup},
		},
		{
			name: "with successful finalizer and succeeded state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupSucceeded
			}),
			expectedFinalizers: []string{},
		},
		{
			name: "with successful finalizer, unknown finalizer and succeeded state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup, "unknown-finalizer"}
				cr.Status.State = apiv1alpha1.BackupSucceeded
			}),
			expectedFinalizers: []string{"unknown-finalizer"},
		},
		{
			name: "with failing finalizer and failed state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupFailed
			}),
			finalizerJobFail:   true,
			expectedFinalizers: []string{finalizerDeleteBackup},
		},
		{
			name: "with successful finalizer and failed state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup}
				cr.Status.State = apiv1alpha1.BackupFailed
			}),
			expectedFinalizers: []string{},
		},
		{
			name: "with successful finalizer, unknown finalizer and failed state",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQLBackup) {
				cr.Finalizers = []string{finalizerDeleteBackup, "unknown-finalizer"}
				cr.Status.State = apiv1alpha1.BackupFailed
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
	storage := &apiv1alpha1.BackupStorageSpec{
		Type: apiv1alpha1.BackupStorageS3,
		S3: &apiv1alpha1.BackupStorageS3Spec{
			Bucket:            "some-bucket",
			CredentialsSecret: "some-secret",
		},
		Labels: make(map[string]string),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cr.Status.Storage = storage

			job := xtrabackup.GetDeleteJob(tt.cr, new(xtrabackup.BackupConfig))
			cond := batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}
			if tt.finalizerJobFail {
				cond.Type = batchv1.JobFailed
			}
			job.Status.Conditions = append(job.Status.Conditions, cond)

			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr, sec, job)
			r := PerconaServerMySQLBackupReconciler{
				Client:        cb.Build(),
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
			}
			err := r.Delete(ctx, tt.cr)
			if err != nil {
				t.Fatal(err)
			}
			cr := new(apiv1alpha1.PerconaServerMySQLBackup)
			if err := r.Get(ctx, types.NamespacedName{Name: tt.cr.Name, Namespace: tt.cr.Namespace}, cr); err != nil {
				if k8serrors.IsNotFound(err) && len(cr.Finalizers) == 0 {
					return
				}
				t.Fatal(err)
			}

			r.checkFinalizers(ctx, cr)
			if !reflect.DeepEqual(cr.Finalizers, tt.expectedFinalizers) {
				t.Fatalf("expected finalizers %v, got %v", tt.expectedFinalizers, tt.cr.Finalizers)
			}
		})
	}
}

// Loads the default PerconaServerMySQL CR from a file and updates its metadata.
func readDefaultCR(name, namespace string) (*apiv1alpha1.PerconaServerMySQL, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "cr.yaml"))
	if err != nil {
		return nil, err
	}

	cr := &apiv1alpha1.PerconaServerMySQL{}

	if err := yaml.Unmarshal(data, cr); err != nil {
		return nil, err
	}

	cr.Name = name
	cr.Namespace = namespace
	return cr, nil
}

// Loads the default PerconaServerMySQLBackup CR from a file and updates its metadata.
func readDefaultCRBackup(name, namespace string) (*apiv1alpha1.PerconaServerMySQLBackup, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "backup.yaml"))
	if err != nil {
		return nil, err
	}

	cr := &apiv1alpha1.PerconaServerMySQLBackup{}

	if err := yaml.Unmarshal(data, cr); err != nil {
		return nil, err
	}

	cr.Name = name
	cr.Namespace = namespace
	return cr, nil
}

// Applies a series of update functions to a given resource.
func updateResource[T any](cr *T, updateFuncs ...func(cr *T)) *T {
	for _, f := range updateFuncs {
		f(cr)
	}
	return cr
}
