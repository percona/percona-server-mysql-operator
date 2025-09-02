package psrestore

import (
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	fakestorage "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage/fake"
)

func readDefaultCluster(t *testing.T, name, namespace string) *apiv1.PerconaServerMySQL {
	t.Helper()

	cr := &apiv1.PerconaServerMySQL{}
	readDefaultFile(t, "cr.yaml", cr)

	cr.Name = name
	cr.Namespace = namespace
	cr.Spec.InitContainer.Image = "init-image"
	return cr
}

func readDefaultBackup(t *testing.T, name, namespace string) *apiv1.PerconaServerMySQLBackup {
	t.Helper()

	cr := &apiv1.PerconaServerMySQLBackup{}
	readDefaultFile(t, "backup.yaml", cr)

	cr.Status.Storage = new(apiv1.BackupStorageSpec)
	cr.Name = name
	cr.Namespace = namespace
	return cr
}

func readDefaultRestore(t *testing.T, name, namespace string) *apiv1.PerconaServerMySQLRestore {
	t.Helper()

	cr := &apiv1.PerconaServerMySQLRestore{}
	readDefaultFile(t, "restore.yaml", cr)

	cr.Name = name
	cr.Namespace = namespace
	return cr
}

func readDefaultS3Secret(t *testing.T, name, namespace string) *corev1.Secret {
	t.Helper()

	secret := new(corev1.Secret)
	readDefaultFile(t, "backup-s3.yaml", secret)

	secret.Name = name
	secret.Namespace = namespace
	return secret
}

func readDefaultAzureSecret(t *testing.T, name, namespace string) *corev1.Secret {
	t.Helper()

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"AZURE_STORAGE_ACCOUNT_NAME": []byte("accountName"),
			"AZURE_STORAGE_ACCOUNT_KEY":  []byte("accountKey"),
		},
	}
}

func readDefaultGCSSecret(t *testing.T, name, namespace string) *corev1.Secret {
	t.Helper()

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"ACCESS_KEY_ID":     []byte("accountName"),
			"SECRET_ACCESS_KEY": []byte("accountKey"),
		},
	}
}

func readDefaultFile[T any](t *testing.T, filename string, obj *T) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", filename))
	if err != nil {
		t.Fatal(err)
	}

	if err := yaml.Unmarshal(data, obj); err != nil {
		t.Fatal(err)
	}
}

func updateResource[T any](cr *T, updateFuncs ...func(cr *T)) *T {
	for _, f := range updateFuncs {
		f(cr)
	}
	return cr
}

func buildFakeClient(t *testing.T, objs ...runtime.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	toClientObj := func(objs []runtime.Object) []client.Object {
		cliObjs := make([]client.Object, 0, len(objs))
		for _, obj := range objs {
			cliObj, ok := obj.(client.Object)
			if ok {
				cliObjs = append(cliObjs, cliObj)
			}
		}
		return cliObjs
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(toClientObj(objs)...).
		Build()

	return cl
}

func reconciler(cl client.Client) *PerconaServerMySQLRestoreReconciler {
	return &PerconaServerMySQLRestoreReconciler{
		Client:           cl,
		Scheme:           cl.Scheme(),
		NewStorageClient: fakestorage.NewFakeClient,
	}
}
