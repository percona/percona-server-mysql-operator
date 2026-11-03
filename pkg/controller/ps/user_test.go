package ps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
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
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
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
		Name:      crName,
		Namespace: ns,
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
				Name:      "some-cluster",
				Namespace: ns,
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
		},
		{
			name: "with user secret",
			cr: &apiv1.PerconaServerMySQL{
				Name:      "some-cluster",
				Namespace: ns,
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
			secret: &corev1.Secret{
				Name:      secretsName,
				Namespace: ns,
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
				Name:      "some-cluster",
				Namespace: ns,
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
			secret: &corev1.Secret{
				Name:      secretsName,
				Namespace: ns,
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
				Name:      "some-cluster",
				Namespace: ns,
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
					CRVersion:   "1.2.0",
				},
			},
			secret: &corev1.Secret{
				Name:      secretsName,
				Namespace: ns,
				Data:      nil,
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

func TestEnsureClusterUserSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	newCR := func() *apiv1.PerconaServerMySQL {
		return &apiv1.PerconaServerMySQL{
			Name:      "cluster",
			Namespace: "database",
			Spec: apiv1.PerconaServerMySQLSpec{
				ClusterServiceDNSSuffix: "cluster.example",
				Proxy: apiv1.ProxySpec{
					Router:  &apiv1.MySQLRouterSpec{},
					HAProxy: &apiv1.HAProxySpec{},
				},
			},
		}
	}

	tests := []struct {
		name         string
		cr           *apiv1.PerconaServerMySQL
		objects      []client.Object
		expectedData map[string][]byte
	}{
		{
			name: "mysql endpoint",
			cr:   newCR(),
			expectedData: map[string][]byte{
				"host":     []byte("cluster-mysql-primary.database.svc.cluster.example"),
				"port":     []byte("3306"),
				"user":     []byte("root"),
				"password": []byte("p@ssword"),
				"uri":      []byte("mysql://root:p%40ssword@cluster-mysql-primary.database.svc.cluster.example:3306"),
			},
		},
		{
			name: "haproxy endpoint",
			cr: func() *apiv1.PerconaServerMySQL {
				cr := newCR()
				cr.Spec.Proxy.HAProxy.Enabled = true
				return cr
			}(),
			expectedData: map[string][]byte{
				"host":                []byte("cluster-mysql-primary.database.svc.cluster.example"),
				"port":                []byte("3306"),
				"user":                []byte("root"),
				"password":            []byte("p@ssword"),
				"uri":                 []byte("mysql://root:p%40ssword@cluster-mysql-primary.database.svc.cluster.example:3306"),
				"proxy-host":          []byte("cluster-haproxy.database.svc.cluster.example"),
				"proxy-port":          []byte("3306"),
				"proxy-uri":           []byte("mysql://root:p%40ssword@cluster-haproxy.database.svc.cluster.example:3306"),
				"proxy-readonly-host": []byte("cluster-haproxy.database.svc.cluster.example"),
				"proxy-readonly-port": []byte("3307"),
				"proxy-readonly-uri":  []byte("mysql://root:p%40ssword@cluster-haproxy.database.svc.cluster.example:3307"),
			},
		},
		{
			name: "router load balancer endpoint",
			cr: func() *apiv1.PerconaServerMySQL {
				cr := newCR()
				cr.Spec.Proxy.Router.Enabled = true
				cr.Spec.Proxy.Router.Expose.Type = corev1.ServiceTypeLoadBalancer
				return cr
			}(),
			objects: []client.Object{
				&corev1.Service{
					Name: "cluster-router", Namespace: "database",
					Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}},
					}},
				},
			},
			expectedData: map[string][]byte{
				"host":                         []byte("cluster-mysql-primary.database.svc.cluster.example"),
				"port":                         []byte("3306"),
				"user":                         []byte("root"),
				"password":                     []byte("p@ssword"),
				"uri":                          []byte("mysql://root:p%40ssword@cluster-mysql-primary.database.svc.cluster.example:3306"),
				"proxy-host":                   []byte("cluster-router.database.svc.cluster.example"),
				"proxy-port":                   []byte("6446"),
				"proxy-uri":                    []byte("mysql://root:p%40ssword@cluster-router.database.svc.cluster.example:6446"),
				"proxy-readonly-host":          []byte("cluster-router.database.svc.cluster.example"),
				"proxy-readonly-port":          []byte("6447"),
				"proxy-readonly-uri":           []byte("mysql://root:p%40ssword@cluster-router.database.svc.cluster.example:6447"),
				"proxy-external-host":          []byte("lb.example.com"),
				"proxy-external-port":          []byte("6446"),
				"proxy-external-uri":           []byte("mysql://root:p%40ssword@lb.example.com:6446"),
				"proxy-readonly-external-host": []byte("lb.example.com"),
				"proxy-readonly-external-port": []byte("6447"),
				"proxy-readonly-external-uri":  []byte("mysql://root:p%40ssword@lb.example.com:6447"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).Build()
			r := PerconaServerMySQLReconciler{Client: cl, Scheme: scheme}
			userSecret := &corev1.Secret{Data: map[string][]byte{
				string(apiv1.UserRoot): []byte("p@ssword"),
			}}

			require.NoError(t, r.ensureClusterUserSecret(t.Context(), tt.cr, userSecret))

			actual := new(corev1.Secret)
			require.NoError(t, cl.Get(t.Context(), types.NamespacedName{
				Name:      "cluster-psuser-root",
				Namespace: "database",
			}, actual))
			assert.Equal(t, tt.expectedData, actual.Data)
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
		statusType  apiv1.ClusterType
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
			// The spec asks for GR but the cluster still runs async, so the
			// password would break the running replication.
			name:        "pending switch to GR keeps the source password limit",
			clusterType: apiv1.ClusterTypeGR,
			statusType:  apiv1.ClusterTypeAsync,
			secret:      s(apiv1.ClusterTypeGR, apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength+1)),
			wantError:   "password for replication user must not exceed 32 bytes",
		},
		{
			// The reverse: still GR, but async is coming, so apply it early.
			name:        "pending switch to async applies the source password limit",
			clusterType: apiv1.ClusterTypeAsync,
			statusType:  apiv1.ClusterTypeGR,
			secret:      s(apiv1.ClusterTypeAsync, apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength+1)),
			wantError:   "password for replication user must not exceed 32 bytes",
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
				Status: apiv1.PerconaServerMySQLStatus{ClusterType: tt.statusType},
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

// rolloutRecordingClient records the writes in order, and the internal secret each restart found.
type rolloutRecordingClient struct {
	client.Client
	internalSecretNN types.NamespacedName
	writes           []string
	secretAtRestart  map[string]*corev1.Secret
}

func writeTarget(obj client.Object) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", obj), "*v1.") + "/" + obj.GetName()
}

func (c *rolloutRecordingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.writes = append(c.writes, "update "+writeTarget(obj))
	return c.Client.Update(ctx, obj, opts...)
}

func (c *rolloutRecordingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	secret := new(corev1.Secret)
	if err := c.Get(ctx, c.internalSecretNN, secret); err != nil {
		return err
	}
	if c.secretAtRestart == nil {
		c.secretAtRestart = make(map[string]*corev1.Secret)
	}
	c.secretAtRestart[writeTarget(obj)] = secret
	c.writes = append(c.writes, "patch "+writeTarget(obj))

	return c.Client.Patch(ctx, obj, patch, opts...)
}

// answerSQL answers the statements a password change runs on a GR cluster whose primary is primary.
func answerSQL(primary string) func(stmt string) (string, bool) {
	return func(stmt string) (string, bool) {
		switch {
		case strings.HasPrefix(stmt, "SELECT MEMBER_HOST"):
			return "host\n" + primary + "\n", true
		case strings.HasPrefix(stmt, "SELECT CONCAT(User, '@', Host)"):
			return "", true
		case strings.HasPrefix(stmt, "ALTER USER"):
			return "", true
		}
		return "", false
	}
}

// The internal secret must hold the new passwords before any pod restarts for them:
// a pod started earlier mounts the old ones, and Orchestrator's subPath mount never sees an update.
func TestReconcileUsersRecordsChangeBeforeRestarts(t *testing.T) {
	const ns = "some-namespace"

	tests := map[string]struct {
		change     map[string]string
		oldToken   string
		wantUsers  string
		wantWrites []string
	}{
		"operator and monitor passwords": {
			change: map[string]string{
				string(apiv1.UserOperator): "operator-password-new",
				string(apiv1.UserMonitor):  "monitor-password-new",
			},
			oldToken:  "token",
			wantUsers: "monitor,operator",
			wantWrites: []string{
				"update Secret/internal-gr",
				"patch StatefulSet/gr-mysql",
				"patch Deployment/gr-router",
			},
		},
		"a changed PMM server token": {
			change:    map[string]string{string(apiv1.UserPMMServerToken): "token-new"},
			oldToken:  "token",
			wantUsers: string(apiv1.UserPMMServerToken),
			wantWrites: []string{
				"update Secret/internal-gr",
				"patch StatefulSet/gr-mysql",
			},
		},
		"a first PMM server token": {
			change:     map[string]string{string(apiv1.UserPMMServerToken): "token"},
			wantWrites: []string{"update Secret/internal-gr"},
		},
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr, oldData := passwordChangeTestCluster(ns, apiv1.StateReady)
			cr.Spec.Proxy.Router = &apiv1.MySQLRouterSpec{Enabled: true, Size: 1}
			cr.Spec.PMM = &apiv1.PMMSpec{Enabled: true}
			oldData[string(apiv1.UserPMMServerToken)] = []byte(tt.oldToken)
			newData := maps.Clone(oldData)
			for user, pass := range tt.change {
				newData[user] = []byte(pass)
			}

			userSecret := &corev1.Secret{Name: cr.Spec.SecretsName, Namespace: ns, Data: newData}
			internalSecret := &corev1.Secret{Name: cr.InternalSecretName(), Namespace: ns, Data: oldData}
			hash, err := k8s.ObjectHash(userSecret)
			require.NoError(t, err)

			replicas := int32(1)
			sts := &appsv1.StatefulSet{
				Name: mysql.Name(cr), Namespace: ns,
				Spec:   appsv1.StatefulSetSpec{Replicas: &replicas},
				Status: appsv1.StatefulSetStatus{UpdateRevision: "rev-1"},
			}
			pod := credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(cr), readyMySQLPod("rev-1"))
			deployment := &appsv1.Deployment{Name: router.Name(cr), Namespace: ns}

			cl := &rolloutRecordingClient{
				Client: fake.NewClientBuilder().WithScheme(scheme).
					WithObjects(cr, userSecret, internalSecret, sts, pod, deployment).Build(),
				internalSecretNN: client.ObjectKeyFromObject(internalSecret),
			}
			r := PerconaServerMySQLReconciler{
				Client:    cl,
				Scheme:    scheme,
				ClientCmd: &credsExecClient{sql: answerSQL("gr-mysql-0.gr-mysql." + ns)},
			}

			require.NoError(t, r.reconcileUsers(t.Context(), cr, userSecret))
			assert.Equal(t, tt.wantWrites, cl.writes)

			for target, secret := range cl.secretAtRestart {
				assert.Equal(t, newData, secret.Data, "%s restarted before the internal secret held the new passwords", target)
				assert.Equal(t, "false", secret.Annotations[naming.AnnotationPasswordsUpdated.String()], target)
				assert.Equal(t, tt.wantUsers, secret.Annotations[naming.AnnotationPasswordsUpdatedUsers.String()], target)
			}

			updated := new(corev1.Secret)
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(internalSecret), updated))
			assert.Equal(t, newData, updated.Data)
			assert.Equal(t, "false", updated.Annotations[naming.AnnotationPasswordsUpdated.String()])
			assert.Equal(t, tt.wantUsers, updated.Annotations[naming.AnnotationPasswordsUpdatedUsers.String()])

			restarted := new(appsv1.Deployment)
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(deployment), restarted))
			if slices.Contains(tt.wantWrites, "patch Deployment/gr-router") {
				assert.Equal(t, hash, restarted.Spec.Template.Annotations[naming.AnnotationSecretHash.String()])
			}
		})
	}
}

func TestBackfillInternalSecret(t *testing.T) {
	const ns = "backfill-ns"

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	userSecret := func() *corev1.Secret {
		return &corev1.Secret{
			Name: "user-secret", Namespace: ns,
			Data: map[string][]byte{
				"operator":     []byte("op-pass"),
				"configurator": []byte("cfg-pass"),
			},
		}
	}

	tests := map[string]struct {
		internal *corev1.Secret
		want     map[string][]byte
		wantSave bool
	}{
		"adds the user missing after an upgrade": {
			internal: &corev1.Secret{
				Name: "internal-secret", Namespace: ns,
				Data: map[string][]byte{"operator": []byte("op-pass")},
			},
			want: map[string][]byte{
				"operator":     []byte("op-pass"),
				"configurator": []byte("cfg-pass"),
			},
			wantSave: true,
		},
		"keeps a password that differs from the user secret": {
			internal: &corev1.Secret{
				Name: "internal-secret", Namespace: ns,
				Data: map[string][]byte{"operator": []byte("old-pass")},
			},
			want: map[string][]byte{
				"operator":     []byte("old-pass"),
				"configurator": []byte("cfg-pass"),
			},
			wantSave: true,
		},
		"writes nothing when no user is missing": {
			internal: &corev1.Secret{
				Name: "internal-secret", Namespace: ns,
				Data: map[string][]byte{
					"operator":     []byte("op-pass"),
					"configurator": []byte("cfg-pass"),
				},
			},
			want: map[string][]byte{
				"operator":     []byte("op-pass"),
				"configurator": []byte("cfg-pass"),
			},
		},
		"populates a nil map": {
			internal: &corev1.Secret{
				Name: "internal-secret", Namespace: ns,
			},
			want: map[string][]byte{
				"operator":     []byte("op-pass"),
				"configurator": []byte("cfg-pass"),
			},
			wantSave: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			user := userSecret()
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.internal.DeepCopy(), user).Build()
			r := &PerconaServerMySQLReconciler{Client: cl, Scheme: scheme}

			internal := tc.internal.DeepCopy()
			require.NoError(t, r.backfillInternalSecret(t.Context(), internal, user))
			assert.Equal(t, tc.want, internal.Data)

			stored := new(corev1.Secret)
			require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Name: internal.Name, Namespace: ns}, stored))
			if tc.wantSave {
				assert.Equal(t, tc.want, stored.Data)
			} else {
				assert.Equal(t, tc.internal.Data, stored.Data)
			}

			user.Data["configurator"][0] = 'X'
			assert.Equal(t, byte('c'), internal.Data["configurator"][0])
		})
	}
}

func TestBackfillInternalSecretUpdateError(t *testing.T) {
	const ns = "backfill-err-ns"

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	internal := &corev1.Secret{
		Name: "internal-secret", Namespace: ns,
		Data: map[string][]byte{"operator": []byte("op-pass")},
	}
	user := &corev1.Secret{
		Name: "user-secret", Namespace: ns,
		Data: map[string][]byte{"configurator": []byte("cfg-pass")},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(user).Build()
	r := &PerconaServerMySQLReconciler{Client: cl, Scheme: scheme}

	err := r.backfillInternalSecret(t.Context(), internal, user)
	require.EqualError(t, err, `update Secret/internal-secret: secrets "internal-secret" not found`)
}

// credsExecClient answers `cat <file>` with the file mounted in the pod, SQL with sql, and fails any other command,
// so a test fails if the operator runs SQL it must not run.
type credsExecClient struct {
	mu         sync.Mutex
	files      map[string]map[string]string
	sql        func(stmt string) (string, bool)
	commands   []string
	statements []string
}

func (c *credsExecClient) Exec(_ context.Context, pod *corev1.Pod, _ string, command []string, _ io.Reader, stdout, stderr io.Writer, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.commands = append(c.commands, pod.Name+": "+strings.Join(command, " "))
	if len(command) > 0 && command[0] == "mysql" && c.sql != nil {
		stmt := command[len(command)-1]
		c.statements = append(c.statements, stmt)
		out, ok := c.sql(stmt)
		if !ok {
			return fmt.Errorf("unexpected statement in pod %s: %s", pod.Name, stmt)
		}
		_, err := io.WriteString(stdout, out)
		return err
	}
	if len(command) != 2 || command[0] != "cat" {
		return fmt.Errorf("unexpected command in pod %s: %v", pod.Name, command)
	}
	files, running := c.files[pod.Name]
	if !running {
		_, _ = fmt.Fprintf(stderr, "container not found (%q)", pod.Name)
		return fmt.Errorf("unable to upgrade connection")
	}
	content, ok := files[command[1]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "cat: %s: No such file or directory", command[1])
		return fmt.Errorf("command terminated with exit code 1")
	}
	_, err := io.WriteString(stdout, content)
	return err
}

func (c *credsExecClient) REST() restclient.Interface {
	return nil
}

func (c *credsExecClient) Config() *restclient.Config {
	return nil
}

func mountedCreds(dir string, data map[string][]byte) map[string]string {
	files := make(map[string]string, len(data))
	for user, pass := range data {
		files[dir+"/"+user] = string(pass)
	}
	return files
}

// credsTestPod returns a running pod whose container is named after the component, as the operator names it.
func credsTestPod(name, ns string, labels map[string]string, opts ...func(*corev1.Pod)) *corev1.Pod {
	pod := &corev1.Pod{
		Name: name, Namespace: ns, Labels: labels,
		Spec: corev1.PodSpec{NodeName: "node-0"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  labels[naming.LabelName],
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	for _, opt := range opts {
		opt(pod)
	}
	return pod
}

func crashLooping(pod *corev1.Pod) {
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
	}
}

// readyMySQLPod makes the pod a ready member of a rolled out StatefulSet revision.
func readyMySQLPod(revision string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		pod.Labels[appsv1.StatefulSetRevisionLabel] = revision
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}}
	}
}

func TestPasswordsPropagated(t *testing.T) {
	const ns = "some-namespace"

	oldData := map[string][]byte{
		string(apiv1.UserMonitor):      []byte("monitor-old"),
		string(apiv1.UserOperator):     []byte("operator-old"),
		string(apiv1.UserOrchestrator): []byte("orchestrator-old"),
	}
	newData := map[string][]byte{
		string(apiv1.UserMonitor):      []byte("monitor-new"),
		string(apiv1.UserOperator):     []byte("operator-new"),
		string(apiv1.UserOrchestrator): []byte("orchestrator-new"),
	}
	orchestratorOnly := func(data map[string][]byte) map[string][]byte {
		return map[string][]byte{string(apiv1.UserOrchestrator): data[string(apiv1.UserOrchestrator)]}
	}

	gr := &apiv1.PerconaServerMySQL{
		Name: "gr", Namespace: ns,
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion: "1.2.0",
			MySQL:     apiv1.MySQLSpec{ClusterType: apiv1.ClusterTypeGR, Size: 1},
			Proxy:     apiv1.ProxySpec{Router: &apiv1.MySQLRouterSpec{Enabled: true, Size: 1}},
		},
	}
	async := &apiv1.PerconaServerMySQL{
		Name: "async", Namespace: ns,
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion:    "1.2.0",
			MySQL:        apiv1.MySQLSpec{ClusterType: apiv1.ClusterTypeAsync, Size: 1},
			Orchestrator: apiv1.OrchestratorSpec{Enabled: true, Size: 1},
			Proxy:        apiv1.ProxySpec{HAProxy: &apiv1.HAProxySpec{Enabled: true, Size: 1}},
		},
	}

	// Router runs as a Deployment, so its pods carry a generated suffix rather than an ordinal.
	const routerPod = "gr-router-6d4cf56db6-x2lq7"

	terminating := func(pod *corev1.Pod) {
		now := metav1.Now()
		pod.DeletionTimestamp = &now
		pod.Finalizers = []string{"test"}
	}
	unscheduled := func(pod *corev1.Pod) {
		pod.Spec.NodeName = ""
		pod.Status.Phase = corev1.PodPending
	}
	evicted := func(pod *corev1.Pod) {
		pod.Status.Phase = corev1.PodFailed
		pod.Status.Reason = "Evicted"
	}

	tests := map[string]struct {
		cr      *apiv1.PerconaServerMySQL
		pods    []*corev1.Pod
		files   map[string]map[string]string
		wantErr bool
		notRead []string
	}{
		"router pod still mounts the old passwords": {
			cr: gr,
			pods: []*corev1.Pod{
				credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(gr)),
				credsTestPod(routerPod, ns, router.MatchLabels(gr)),
			},
			files: map[string]map[string]string{
				"gr-mysql-0": mountedCreds(naming.CredsMountPath, newData),
				routerPod:    mountedCreds(router.CredsMountPath, oldData),
			},
			wantErr: true,
		},
		"every pod mounts the new passwords": {
			cr: gr,
			pods: []*corev1.Pod{
				credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(gr)),
				credsTestPod(routerPod, ns, router.MatchLabels(gr)),
			},
			files: map[string]map[string]string{
				"gr-mysql-0": mountedCreds(naming.CredsMountPath, newData),
				routerPod:    mountedCreds(router.CredsMountPath, newData),
			},
		},
		"orchestrator pod still mounts its old password": {
			cr: async,
			pods: []*corev1.Pod{
				credsTestPod("async-mysql-0", ns, mysql.MatchLabels(async)),
				credsTestPod("async-haproxy-0", ns, haproxy.MatchLabels(async)),
				credsTestPod("async-orc-0", ns, orchestrator.MatchLabels(async)),
			},
			files: map[string]map[string]string{
				"async-mysql-0":   mountedCreds(naming.CredsMountPath, newData),
				"async-haproxy-0": mountedCreds(haproxy.CredsMountPath, newData),
				"async-orc-0":     mountedCreds(orchestrator.CredsMountPath, orchestratorOnly(oldData)),
			},
			wantErr: true,
		},
		// Orchestrator mounts only its own password, so the other users have no file there.
		"orchestrator pod mounts its new password": {
			cr: async,
			pods: []*corev1.Pod{
				credsTestPod("async-mysql-0", ns, mysql.MatchLabels(async)),
				credsTestPod("async-haproxy-0", ns, haproxy.MatchLabels(async)),
				credsTestPod("async-orc-0", ns, orchestrator.MatchLabels(async)),
			},
			files: map[string]map[string]string{
				"async-mysql-0":   mountedCreds(naming.CredsMountPath, newData),
				"async-haproxy-0": mountedCreds(haproxy.CredsMountPath, newData),
				"async-orc-0":     mountedCreds(orchestrator.CredsMountPath, orchestratorOnly(newData)),
			},
		},
		"a running container that cannot be read": {
			cr: gr,
			pods: []*corev1.Pod{
				credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(gr)),
				credsTestPod(routerPod, ns, router.MatchLabels(gr)),
			},
			files: map[string]map[string]string{
				"gr-mysql-0": mountedCreds(naming.CredsMountPath, newData),
			},
			wantErr: true,
		},
		"a crash-looping container is not read": {
			cr: gr,
			pods: []*corev1.Pod{
				credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(gr)),
				credsTestPod(routerPod, ns, router.MatchLabels(gr), crashLooping),
			},
			files: map[string]map[string]string{
				"gr-mysql-0": mountedCreds(naming.CredsMountPath, newData),
			},
			notRead: []string{routerPod},
		},
		"pods that are not scheduled, terminating or evicted are not checked": {
			cr: gr,
			pods: []*corev1.Pod{
				credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(gr)),
				credsTestPod(routerPod, ns, router.MatchLabels(gr), terminating),
				credsTestPod("gr-router-6d4cf56db6-pending", ns, router.MatchLabels(gr), unscheduled),
				credsTestPod("gr-router-6d4cf56db6-evicted", ns, router.MatchLabels(gr), evicted),
			},
			files: map[string]map[string]string{
				"gr-mysql-0": mountedCreds(naming.CredsMountPath, newData),
			},
		},
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{tt.cr}
			for _, pod := range tt.pods {
				objs = append(objs, pod)
			}
			execCli := &credsExecClient{files: tt.files}
			r := PerconaServerMySQLReconciler{
				Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
				Scheme:    scheme,
				ClientCmd: execCli,
			}

			users := make([]mysql.User, 0, len(newData))
			for user := range newData {
				users = append(users, mysql.User{Username: apiv1.SystemUser(user)})
			}

			err := r.passwordsPropagated(t.Context(), tt.cr, &corev1.Secret{Data: newData}, users)
			for _, pod := range tt.notRead {
				for _, cmd := range execCli.commands {
					assert.NotContains(t, cmd, pod+": ")
				}
			}
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrPassNotPropagated)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// A changed password must not be discarded while any Router pod still mounts it.
func TestDiscardOldPasswordsWaitsForEveryRouterPod(t *testing.T) {
	const ns = "some-namespace"
	const routerPod = "gr-router-6d4cf56db6-x2lq7"

	cr := &apiv1.PerconaServerMySQL{
		Name: "gr", Namespace: ns,
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion: "1.2.0",
			MySQL:     apiv1.MySQLSpec{ClusterType: apiv1.ClusterTypeGR, Size: 1},
			Proxy:     apiv1.ProxySpec{Router: &apiv1.MySQLRouterSpec{Enabled: true, Size: 1}},
		},
	}
	newData := map[string][]byte{string(apiv1.UserMonitor): []byte("monitor-new")}
	oldData := map[string][]byte{string(apiv1.UserMonitor): []byte("monitor-old")}
	internalSecret := &corev1.Secret{
		Name: cr.InternalSecretName(), Namespace: ns,
		Annotations: map[string]string{naming.AnnotationPasswordsUpdated.String(): "false"},
		Data:        newData,
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	execCli := &credsExecClient{files: map[string]map[string]string{
		"gr-mysql-0": mountedCreds(naming.CredsMountPath, newData),
		routerPod:    mountedCreds(router.CredsMountPath, oldData),
	}}
	r := PerconaServerMySQLReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			cr,
			internalSecret,
			credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(cr)),
			credsTestPod(routerPod, ns, router.MatchLabels(cr)),
		).Build(),
		Scheme:    scheme,
		ClientCmd: execCli,
	}

	users := []mysql.User{{Username: apiv1.UserMonitor, Hosts: []string{"%"}, Password: "monitor-new"}}
	require.NoError(t, r.discardOldPasswordsAfterNewPropagated(t.Context(), cr, internalSecret, users, "operator-pass"))

	for _, cmd := range execCli.commands {
		assert.Contains(t, cmd, ": cat ", "the old passwords were discarded before every pod mounted the new ones")
	}
	updated := new(corev1.Secret)
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(internalSecret), updated))
	assert.Equal(t, "false", updated.Annotations[naming.AnnotationPasswordsUpdated.String()])
}

func passwordChangeTestCluster(ns string, state apiv1.StatefulAppState) (*apiv1.PerconaServerMySQL, map[string][]byte) {
	cr := &apiv1.PerconaServerMySQL{
		Name: "gr", Namespace: ns,
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion:   "1.2.0",
			SecretsName: "gr-secrets",
			MySQL:       apiv1.MySQLSpec{ClusterType: apiv1.ClusterTypeGR, Size: 1},
		},
		Status: apiv1.PerconaServerMySQLStatus{
			MySQL: apiv1.StatefulAppStatus{State: apiv1.StateReady},
			State: state,
		},
	}
	data := make(map[string][]byte)
	for _, user := range secret.SystemUsers(cr) {
		data[string(user)] = []byte(string(user) + "-password")
	}
	return cr, data
}

// Passwords applied before the cluster is ready would be applied again on every
// reconcile until it is, and a second RETAIN CURRENT PASSWORD drops the password
// that pods and the operator itself still use.
func TestReconcileUsersWaitsForReadyClusterBeforeChangingPasswords(t *testing.T) {
	const ns = "some-namespace"

	cr, oldData := passwordChangeTestCluster(ns, apiv1.StateInitializing)
	newData := maps.Clone(oldData)
	newData[string(apiv1.UserOperator)] = []byte("operator-password-new")

	userSecret := &corev1.Secret{Name: cr.Spec.SecretsName, Namespace: ns, Data: newData}
	internalSecret := &corev1.Secret{Name: cr.InternalSecretName(), Namespace: ns, Data: oldData}

	// A rolled out StatefulSet, so the pass is not held back by a smart update.
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		Name: mysql.Name(cr), Namespace: ns,
		Spec:   appsv1.StatefulSetSpec{Replicas: &replicas},
		Status: appsv1.StatefulSetStatus{UpdateRevision: "rev-1"},
	}
	pod := credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(cr), readyMySQLPod("rev-1"))

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	fc := &fakeClient{}
	r := PerconaServerMySQLReconciler{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr, userSecret, internalSecret, sts, pod).Build(),
		Scheme:    scheme,
		ClientCmd: fc,
	}

	require.NoError(t, r.reconcileUsers(t.Context(), cr, userSecret))
	assert.Equal(t, 0, fc.execCount, "passwords were changed on a cluster that is not ready")

	updated := new(corev1.Secret)
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(internalSecret), updated))
	assert.Equal(t, oldData, updated.Data)
}

// A new change waits until the old passwords of the previous one are discarded,
// so the passwords retained for the previous change are never overwritten.
func TestReconcileUsersFinishesPreviousChangeFirst(t *testing.T) {
	const ns = "some-namespace"

	cr, firstData := passwordChangeTestCluster(ns, apiv1.StateReady)
	secondData := maps.Clone(firstData)
	secondData[string(apiv1.UserOperator)] = []byte("operator-password-second")
	staleData := maps.Clone(firstData)
	staleData[string(apiv1.UserOperator)] = []byte("operator-password-before-first")

	userSecret := &corev1.Secret{Name: cr.Spec.SecretsName, Namespace: ns, Data: secondData}
	internalSecret := &corev1.Secret{
		Name: cr.InternalSecretName(), Namespace: ns,
		Annotations: map[string]string{naming.AnnotationPasswordsUpdated.String(): "false"},
		Data:        firstData,
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	execCli := &credsExecClient{files: map[string]map[string]string{
		"gr-mysql-0": mountedCreds(naming.CredsMountPath, staleData),
	}}
	r := PerconaServerMySQLReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			cr, userSecret, internalSecret, credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(cr)),
		).Build(),
		Scheme:    scheme,
		ClientCmd: execCli,
	}

	require.NoError(t, r.reconcileUsers(t.Context(), cr, userSecret))
	for _, cmd := range execCli.commands {
		assert.Contains(t, cmd, ": cat ", "a new password change started before the previous one finished")
	}

	updated := new(corev1.Secret)
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(internalSecret), updated))
	assert.Equal(t, firstData, updated.Data)
}

// A recorded change is driven to its end by later passes: the restarts it needs are requested
// again, without restarting anything twice, and only its users' old passwords are discarded.
func TestReconcileUsersCompletesRecordedChange(t *testing.T) {
	const ns = "some-namespace"
	const routerPod = "gr-router-6d4cf56db6-x2lq7"

	cr, oldData := passwordChangeTestCluster(ns, apiv1.StateReady)
	cr.Spec.Proxy.Router = &apiv1.MySQLRouterSpec{Enabled: true, Size: 1}
	newData := maps.Clone(oldData)
	newData[string(apiv1.UserOperator)] = []byte("operator-password-new")
	newData[string(apiv1.UserRoot)] = []byte("root-password-new")

	hash, err := k8s.ObjectHash(&corev1.Secret{Data: newData})
	require.NoError(t, err)

	discards := func(users ...apiv1.SystemUser) []string {
		stmts := make([]string, 0)
		for _, user := range users {
			for _, host := range allSystemUsers(cr)[user].Hosts {
				stmts = append(stmts, fmt.Sprintf("ALTER USER '%s'@'%s' DISCARD OLD PASSWORD", user, host))
			}
		}
		return stmts
	}
	recorded := map[string]string{
		naming.AnnotationPasswordsUpdated.String():      "false",
		naming.AnnotationPasswordsUpdatedUsers.String(): "operator,root",
	}

	tests := map[string]struct {
		annotations     map[string]string
		routerHash      string
		routerData      map[string][]byte
		wantWrites      []string
		wantDiscards    []string
		wantAnnotations map[string]string
		wantRouterHash  string
	}{
		"restarts a component the change has not restarted yet": {
			annotations:     recorded,
			routerData:      oldData,
			wantWrites:      []string{"patch Deployment/gr-router"},
			wantAnnotations: recorded,
			wantRouterHash:  hash,
		},
		"discards the old passwords of the changed users only": {
			annotations:  recorded,
			routerHash:   hash,
			routerData:   newData,
			wantWrites:   []string{"update Secret/internal-gr"},
			wantDiscards: discards(apiv1.UserOperator, apiv1.UserRoot),
			wantAnnotations: map[string]string{
				naming.AnnotationPasswordsUpdated.String(): "true",
			},
			wantRouterHash: hash,
		},
		"a change recorded without its users discards every system user's": {
			annotations: map[string]string{
				naming.AnnotationPasswordsUpdated.String(): "false",
			},
			routerData:   newData,
			wantWrites:   []string{"update Secret/internal-gr"},
			wantDiscards: discards(secret.SystemUsers(cr)...),
			wantAnnotations: map[string]string{
				naming.AnnotationPasswordsUpdated.String(): "true",
			},
		},
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			userSecret := &corev1.Secret{Name: cr.Spec.SecretsName, Namespace: ns, Data: newData}
			internalSecret := &corev1.Secret{
				Name: cr.InternalSecretName(), Namespace: ns,
				Annotations: maps.Clone(tt.annotations),
				Data:        newData,
			}
			deployment := &appsv1.Deployment{Name: router.Name(cr), Namespace: ns}
			if tt.routerHash != "" {
				deployment.Spec.Template.Annotations = map[string]string{naming.AnnotationSecretHash.String(): tt.routerHash}
			}

			cl := &rolloutRecordingClient{
				Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
					cr, userSecret, internalSecret, deployment,
					credsTestPod("gr-mysql-0", ns, mysql.MatchLabels(cr), readyMySQLPod("rev-1")),
					credsTestPod(routerPod, ns, router.MatchLabels(cr)),
				).Build(),
				internalSecretNN: client.ObjectKeyFromObject(internalSecret),
			}
			execCli := &credsExecClient{
				files: map[string]map[string]string{
					"gr-mysql-0": mountedCreds(naming.CredsMountPath, newData),
					routerPod:    mountedCreds(router.CredsMountPath, tt.routerData),
				},
				sql: answerSQL("gr-mysql-0.gr-mysql." + ns),
			}
			r := PerconaServerMySQLReconciler{Client: cl, Scheme: scheme, ClientCmd: execCli}

			require.NoError(t, r.reconcileUsers(t.Context(), cr, userSecret))
			assert.Equal(t, tt.wantWrites, cl.writes)

			var discarded []string
			for _, stmt := range execCli.statements {
				if strings.Contains(stmt, "DISCARD OLD PASSWORD") {
					discarded = append(discarded, stmt)
				}
			}
			assert.ElementsMatch(t, tt.wantDiscards, discarded)

			updated := new(corev1.Secret)
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(internalSecret), updated))
			assert.Equal(t, tt.wantAnnotations, updated.Annotations)

			restarted := new(appsv1.Deployment)
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(deployment), restarted))
			assert.Equal(t, tt.wantRouterHash, restarted.Spec.Template.Annotations[naming.AnnotationSecretHash.String()])
		})
	}
}
