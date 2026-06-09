package ps

import (
	"bytes"
	"context"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
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

			for _, user := range secret.SecretUsers {
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
	s := func(user apiv1.SystemUser, password []byte) *corev1.Secret {
		secret := &corev1.Secret{
			Data: make(map[string][]byte),
		}
		for systemUser := range allSystemUsers() {
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
				secret := s(apiv1.UserRoot, []byte("password"))
				delete(secret.Data, string(apiv1.UserMonitor))
				return secret
			}(),
			wantError: "missing password for monitor user",
		},
		{
			name:        "unknown user",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.SystemUser("unknown"), []byte("password")),
			wantError:   "unknown user unknown is specified in the secret",
		},
		{
			name:        "empty password",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.UserRoot, nil),
			wantError:   "password is empty for root user",
		},
		{
			name:        "NUL byte",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.UserRoot, []byte{'a', 0, 'b'}),
			wantError:   "password for root user must not contain NUL bytes",
		},
		{
			name:        "maximum MySQL password length",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.UserRoot, bytes.Repeat([]byte{'a'}, mySQLPasswordMaxLength)),
		},
		{
			name:        "MySQL password too long",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.UserRoot, bytes.Repeat([]byte{'a'}, mySQLPasswordMaxLength+1)),
			wantError:   "password for root user must not exceed 256 bytes",
		},
		{
			name:        "maximum async replication password length",
			clusterType: apiv1.ClusterTypeAsync,
			secret:      s(apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength)),
		},
		{
			name:        "async replication password too long",
			clusterType: apiv1.ClusterTypeAsync,
			secret:      s(apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength+1)),
			wantError:   "password for replication user must not exceed 32 bytes",
		},
		{
			name:        "async replication limit counts bytes",
			clusterType: apiv1.ClusterTypeAsync,
			secret:      s(apiv1.UserReplication, []byte(strings.Repeat("ї", 17))),
			wantError:   "password for replication user must not exceed 32 bytes",
		},
		{
			name:        "group replication does not use source password limit",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.UserReplication, bytes.Repeat([]byte{'a'}, mySQLReplicationSourcePasswordMaxLength+1)),
		},
		{
			name:        "PMM server token is not a MySQL password",
			clusterType: apiv1.ClusterTypeGR,
			secret:      s(apiv1.UserPMMServerToken, nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{
				Spec: apiv1.PerconaServerMySQLSpec{
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
