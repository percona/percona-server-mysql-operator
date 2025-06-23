package testutil

import (
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	psv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

// creates a fake client to mock API calls with the mock objects
func BuildFakeClient(objs ...client.Object) client.Client {
	s := scheme.Scheme

	s.AddKnownTypes(psv1alpha1.GroupVersion,
		new(psv1alpha1.PerconaServerMySQL),
		new(psv1alpha1.PerconaServerMySQLList),
		new(psv1alpha1.PerconaServerMySQLBackup),
		new(psv1alpha1.PerconaServerMySQLBackupList),
		new(psv1alpha1.PerconaServerMySQLRestore),
		new(psv1alpha1.PerconaServerMySQLRestoreList),
	)

	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(objs...).Build()
}
