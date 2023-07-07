package ps

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
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
		cr     *apiv1alpha1.PerconaServerMySQL
		secret *corev1.Secret
	}{
		{
			name: "without user secret",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
				},
			},
		},
		{
			name: "with user secret",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretsName,
					Namespace: ns,
				},
				Data: map[string][]byte{
					string(apiv1alpha1.UserHeartbeat):    []byte("hb-password"),
					string(apiv1alpha1.UserMonitor):      []byte("m-password"),
					string(apiv1alpha1.UserOperator):     []byte("op-password"),
					string(apiv1alpha1.UserOrchestrator): []byte("orc-password"),
					string(apiv1alpha1.UserReplication):  []byte("repl-password"),
					string(apiv1alpha1.UserRoot):         []byte("root-password"),
					string(apiv1alpha1.UserXtraBackup):   []byte("backup-password"),
				},
			},
		},
		{
			name: "with partially filled secret",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					SecretsName: secretsName,
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretsName,
					Namespace: ns,
				},
				Data: map[string][]byte{
					string(apiv1alpha1.UserHeartbeat):   []byte("hb-password"),
					string(apiv1alpha1.UserMonitor):     []byte("m-password"),
					string(apiv1alpha1.UserReplication): []byte("repl-password"),
					string(apiv1alpha1.UserXtraBackup):  []byte("backup-password"),
				},
			},
		},
		{
			name: "with existing empty secret",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: ns,
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
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
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
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
			if err := r.ensureUserSecrets(ctx, tt.cr); err != nil {
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
