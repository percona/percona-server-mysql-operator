package ps

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

var _ = Describe("Keep user secrets", Ordered, func() {
	ctx := context.Background()

	const crName = "user-keep-secret"
	const ns = crName

	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
	}

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("create and delete PS cluster", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)
		It("should read and create default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("should reconcile once to create user secret", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})
		It("should create user secret without owner references", func() {
			secret := new(corev1.Secret)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cr.Spec.SecretsName, Namespace: cr.Namespace}, secret)).
				Should(Succeed())

			Expect(secret.OwnerReferences).Should(BeEmpty())
		})
	})
})

func TestEnsureUserSecrets(t *testing.T) {
	ctx := context.Background()
	secretsName := "some-secret"
	ns := "some-namespace"
	tests := []struct {
		name   string
		cr     *apiv1.PerconaServerMySQL
		secret *corev1.Secret
	}{
		{
			name: "without user secret",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
		},
		{
			name: "with user secret",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretsName,
					Namespace: ns,
				},
				Data: map[string][]byte{
					string(apiv1.UserHeartbeat):    []byte("hb-password"),
					string(apiv1.UserMonitor):      []byte("m-password"),
					string(apiv1.UserOperator):     []byte("op-password"),
					string(apiv1.UserOrchestrator): []byte("orc-password"),
					string(apiv1.UserReplication):  []byte("repl-password"),
					string(apiv1.UserRoot):         []byte("root-password"),
					string(apiv1.UserXtraBackup):   []byte("backup-password"),
					string(apiv1.UserClusterSet):   []byte("clusterset-password"),
				},
			},
		},
		{
			name: "with partially filled secret",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretsName,
					Namespace: ns,
				},
				Data: map[string][]byte{
					string(apiv1.UserHeartbeat):   []byte("hb-password"),
					string(apiv1.UserMonitor):     []byte("m-password"),
					string(apiv1.UserReplication): []byte("repl-password"),
					string(apiv1.UserXtraBackup):  []byte("backup-password"),
				},
			},
		},
		{
			name: "with existing empty secret",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretsName,
					Namespace: ns,
				},
				Data: nil,
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr)
			if tt.secret != nil {
				cb = cb.WithObjects(tt.secret)
			}
			r := PerconaServerMySQLReconciler{
				Client: cb.Build(),
				Scheme: scheme,
			}
			if _, err := r.ensureUserSecrets(ctx, tt.cr); err != nil {
				t.Fatal(err, "failed to ensure user secrets")
			}
			uSecret := new(corev1.Secret)
			if err := r.Get(ctx, types.NamespacedName{Name: tt.cr.Spec.SecretsName, Namespace: tt.cr.Namespace}, uSecret); err != nil {
				t.Fatal(err, "failed to get user secret")
			}

			for _, user := range secret.SystemUsers(tt.cr) {
				if _, ok := uSecret.Data[string(user)]; !ok {
					t.Fatalf("user %s not found in secret", user)
				}
			}

			if tt.secret != nil {
				for k, v := range tt.secret.Data {
					newV, ok := uSecret.Data[k]
					if !ok {
						t.Fatalf("user %s not found in secret", k)
					}
					if string(v) != string(newV) {
						t.Fatalf("old password for %s is not equal to the new one", k)
					}
				}
			}
		})
	}
}

func TestValidateUserSecret(t *testing.T) {
	s := func(clusterType apiv1.ClusterType, user apiv1.SystemUser, password []byte) *corev1.Secret {
		cr := &apiv1.PerconaServerMySQL{
			Spec: apiv1.PerconaServerMySQLSpec{
				CRVersion: version.Version(),
				MySQL: apiv1.MySQLSpec{
					ClusterType: clusterType,
				},
			},
		}
		secret := &corev1.Secret{
			Data: make(map[string][]byte),
		}
		for systemUser := range allSystemUsers(cr) {
			secret.Data[string(systemUser)] = []byte("password")
		}
		secret.Data[string(user)] = password
		return secret
	}

	tests := []struct {
		name        string
		clusterType apiv1.ClusterType
		secret      *corev1.Secret
		wantError   string
	}{
		{
			name:        "nil secret",
			clusterType: apiv1.ClusterTypeGR,
			wantError:   "user secret is empty",
		},
		{
			name:        "empty secret data",
			clusterType: apiv1.ClusterTypeGR,
			secret:      &corev1.Secret{},
			wantError:   "user secret is empty",
		},
		{
			name:        "missing password",
			clusterType: apiv1.ClusterTypeGR,
			secret: func() *corev1.Secret {
				secret := s(apiv1.ClusterTypeGR, apiv1.UserRoot, []byte("password"))
				delete(secret.Data, string(apiv1.UserMonitor))
				return secret
			}(),
			wantError: "missing password for monitor user",
		},
		{
			name:        "unknown user",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeGR, apiv1.SystemUser("unknown"), []byte("password")),
			wantError:   "unknown user unknown is specified in the secret",
		},
		{
			name:        "empty password",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeGR, apiv1.UserRoot, nil),
			wantError:   "password is empty for root user",
		},
		{
			name:        "NUL byte",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeGR, apiv1.UserRoot, []byte{'a', 0, 'b'}),
			wantError:   "password for root user must not contain NUL bytes",
		},
		{
			name:        "maximum MySQL password length",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeGR, apiv1.UserRoot, bytes.Repeat([]byte{'a'}, mySQLPasswordMaxLength)),
		},
		{
			name:        "MySQL password too long",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeGR, apiv1.UserRoot, bytes.Repeat([]byte{'a'}, mySQLPasswordMaxLength+1)),
			wantError:   "password for root user must not exceed 256 bytes",
		},
		{
			name:        "maximum async replication password length",
			clusterType: apiv1.ClusterTypeAsync,
			secret:      s(apiv1.ClusterTypeAsync, apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength)),
		},
		{
			name:        "async replication password too long",
			clusterType: apiv1.ClusterTypeAsync,
			secret:      s(apiv1.ClusterTypeAsync, apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength+1)),
			wantError:   "password for replication user must not exceed 32 bytes",
		},
		{
			name:        "async replication limit counts bytes",
			clusterType: apiv1.ClusterTypeAsync,
			secret:      s(apiv1.ClusterTypeAsync, apiv1.UserReplication, []byte(strings.Repeat("ї", 17))),
			wantError:   "password for replication user must not exceed 32 bytes",
		},
		{
			name:        "group replication does not use source password limit",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeGR, apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength+1)),
		},
		{
			name:        "PMM server token is not a MySQL password",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeGR, apiv1.UserPMMServerToken, nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{
				Spec: apiv1.PerconaServerMySQLSpec{
					CRVersion: version.Version(),
					MySQL: apiv1.MySQLSpec{
						ClusterType: tt.clusterType,
					},
				},
			}
			err := validateUserSecret(cr, tt.secret)
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantError)
		})
	}
}

// TestReconcileUsersCreatesMissingClusterSetUser covers the upgrade path from
// operator versions older than 1.2.0: the clusterset user appears in the users
// secret but doesn't exist in MySQL, because the entrypoint creates users only
// on initial datadir initialization. reconcileUsers must create the user
// before altering its password, otherwise ALTER USER fails with ERROR 1396.
func TestReconcileUsersCreatesClusterSetUser(t *testing.T) {
	ctx := context.Background()

	const crName = "upgraded-cluster"
	const ns = "some-namespace"
	const operatorPass = "op-password"
	const clusterSetPass = "cs-password"

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion:   "1.2.0",
			SecretsName: "some-secret",
			MySQL: apiv1.MySQLSpec{
				ClusterType: apiv1.ClusterTypeGR,
			},
		},
		Status: apiv1.PerconaServerMySQLStatus{
			MySQL: apiv1.StatefulAppStatus{State: apiv1.StateReady},
			State: apiv1.StateInitializing,
		},
	}

	oldData := map[string][]byte{
		string(apiv1.UserHeartbeat):    []byte("hb-password"),
		string(apiv1.UserMonitor):      []byte("m-password"),
		string(apiv1.UserOperator):     []byte(operatorPass),
		string(apiv1.UserOrchestrator): []byte("orc-password"),
		string(apiv1.UserReplication):  []byte("repl-password"),
		string(apiv1.UserRoot):         []byte("root-password"),
		string(apiv1.UserXtraBackup):   []byte("backup-password"),
	}

	// The users secret got the new clusterset entry on upgrade, the internal
	// secret still holds the pre-upgrade data without it.
	newData := make(map[string][]byte, len(oldData)+1)
	maps.Copy(newData, oldData)
	newData[string(apiv1.UserClusterSet)] = []byte(clusterSetPass)

	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Spec.SecretsName,
			Namespace: ns,
		},
		Data: newData,
	}
	internalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.InternalSecretName(),
			Namespace: ns,
		},
		Data: oldData,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mysql.PodName(cr, 0),
			Namespace: ns,
			Labels:    mysql.MatchLabels(cr),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	host := mysql.FQDN(cr, 0)
	mysqlCmd := func(query string) []string {
		return []string{
			"mysql",
			"--database", "performance_schema",
			"-p" + operatorPass,
			"-u", "operator",
			"-h", host,
			"-e", query,
		}
	}

	scripts := []fakeClientScript{
		{
			cmd:    mysqlCmd("SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'"),
			stdout: []byte("host\n" + host + "\n"),
		},
		{
			cmd: mysqlCmd("CREATE USER IF NOT EXISTS 'clusterset'@'%' IDENTIFIED BY '" + clusterSetPass + "' PASSWORD EXPIRE NEVER; " +
				"GRANT SELECT, RELOAD, SHUTDOWN, PROCESS, FILE, REPLICATION SLAVE, REPLICATION CLIENT, CREATE USER, EXECUTE ON *.* TO 'clusterset'@'%' WITH GRANT OPTION; " +
				"GRANT BACKUP_ADMIN, CLONE_ADMIN, CONNECTION_ADMIN, GROUP_REPLICATION_ADMIN, REPLICATION_SLAVE_ADMIN, REPLICATION_APPLIER, PERSIST_RO_VARIABLES_ADMIN, ROLE_ADMIN, SESSION_VARIABLES_ADMIN, SYSTEM_VARIABLES_ADMIN ON *.* TO 'clusterset'@'%' WITH GRANT OPTION; " +
				"GRANT INSERT, UPDATE, DELETE ON mysql.* TO 'clusterset'@'%' WITH GRANT OPTION; " +
				"GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata.* TO 'clusterset'@'%' WITH GRANT OPTION; " +
				"GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata_bkp.* TO 'clusterset'@'%' WITH GRANT OPTION; " +
				"GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata_previous.* TO 'clusterset'@'%' WITH GRANT OPTION"),
		},
		{
			cmd: mysqlCmd(fmt.Sprintf("ALTER USER 'clusterset'@'%%' IDENTIFIED BY '%s' RETAIN CURRENT PASSWORD", clusterSetPass)),
		},
		{
			cmd: mysqlCmd("FLUSH PRIVILEGES"),
		},
	}
	fc := &fakeClient{scripts: scripts}

	r := PerconaServerMySQLReconciler{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr, userSecret, internalSecret, pod).Build(),
		Scheme:    scheme,
		ClientCmd: fc,
	}

	if err := r.reconcileUsers(ctx, cr, userSecret); err != nil {
		t.Fatalf("reconcileUsers failed: %v", err)
	}

	if fc.execCount != len(scripts) {
		t.Fatalf("expected %d exec calls, got %d", len(scripts), fc.execCount)
	}
}

func TestReconcileUsersRestartsRouterOnOperatorPasswordUpdate(t *testing.T) {
	ctx := context.Background()

	const crName = "router-password-rotation"
	const ns = "some-namespace"
	const oldOperatorPass = "op-password-old"
	const newOperatorPass = "op-password-new"

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion:   "1.2.0",
			SecretsName: "some-secret",
			MySQL: apiv1.MySQLSpec{
				ClusterType: apiv1.ClusterTypeGR,
				PodSpec:     apiv1.PodSpec{Size: 1},
			},
			Proxy: apiv1.ProxySpec{
				Router: &apiv1.MySQLRouterSpec{
					Enabled: true,
					PodSpec: apiv1.PodSpec{Size: 1},
				},
			},
		},
		Status: apiv1.PerconaServerMySQLStatus{
			MySQL: apiv1.StatefulAppStatus{State: apiv1.StateReady},
			State: apiv1.StateReady,
		},
	}

	oldData := map[string][]byte{string(apiv1.UserOperator): []byte(oldOperatorPass)}
	newData := map[string][]byte{string(apiv1.UserOperator): []byte(newOperatorPass)}

	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Spec.SecretsName, Namespace: ns},
		Data:       newData,
	}
	internalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cr.InternalSecretName(), Namespace: ns},
		Data:       oldData,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mysql.PodName(cr, 0),
			Namespace: ns,
			Labels:    mysql.MatchLabels(cr),
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}},
		},
	}

	routerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      router.Name(cr),
			Namespace: ns,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{},
		},
	}

	routerPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%d", cr.Name, router.AppName, 0),
			Namespace: ns,
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}},
		},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	host := mysql.FQDN(cr, 0)
	mysqlCmdWithPass := func(pass, query string) []string {
		return []string{
			"mysql",
			"--database", "performance_schema",
			"-p" + pass,
			"-u", "operator",
			"-h", host,
			"-e", query,
		}
	}
	mysqlCmd := func(query string) []string {
		return mysqlCmdWithPass(oldOperatorPass, query)
	}

	scripts := []fakeClientScript{
		{
			cmd:    mysqlCmd("SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'"),
			stdout: []byte("host\n" + host + "\n"),
		},
		{
			cmd: mysqlCmd(fmt.Sprintf("ALTER USER 'operator'@'%%' IDENTIFIED BY '%s' RETAIN CURRENT PASSWORD", newOperatorPass)),
		},
		{
			cmd: mysqlCmd("FLUSH PRIVILEGES"),
		},
		{
			cmd:    []string{"cat", naming.CredsMountPath + "/" + string(apiv1.UserOperator)},
			stdout: []byte(newOperatorPass),
		},
		{
			cmd:    []string{"cat", router.CredsMountPath + "/" + string(apiv1.UserOperator)},
			stdout: []byte(newOperatorPass),
		},
		{
			cmd:    mysqlCmdWithPass(newOperatorPass, "SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'"),
			stdout: []byte("host\n" + host + "\n"),
		},
		{
			cmd: mysqlCmd("ALTER USER 'operator'@'%' DISCARD OLD PASSWORD"),
		},
		{
			cmd: mysqlCmd("FLUSH PRIVILEGES"),
		},
	}
	fc := &fakeClient{scripts: scripts}

	r := PerconaServerMySQLReconciler{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr, userSecret, internalSecret, pod, routerDeployment, routerPod).Build(),
		Scheme:    scheme,
		ClientCmd: fc,
	}

	if err := r.reconcileUsers(ctx, cr, userSecret); err != nil {
		t.Fatalf("reconcileUsers failed: %v", err)
	}

	if fc.execCount != len(scripts) {
		t.Fatalf("expected %d exec calls, got %d", len(scripts), fc.execCount)
	}

	updatedDeployment := new(appsv1.Deployment)
	nn := types.NamespacedName{Name: router.Name(cr), Namespace: ns}
	if err := r.Get(ctx, nn, updatedDeployment); err != nil {
		t.Fatalf("get router deployment: %v", err)
	}

	hash, err := k8s.ObjectHash(userSecret)
	if err != nil {
		t.Fatalf("calculate secret hash: %v", err)
	}

	gotHash := updatedDeployment.Spec.Template.Annotations[naming.AnnotationSecretHash.String()]
	if gotHash != hash {
		t.Fatalf("router deployment secret hash mismatch: got %q, want %q", gotHash, hash)
	}
}
