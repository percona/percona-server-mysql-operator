package ps

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	clientcmdmock "github.com/percona/percona-server-mysql-operator/pkg/clientcmd/mock"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestReconcileTLSReload(t *testing.T) {
	const (
		crName       = "cluster1"
		ns           = "tls-ns"
		sslSecret    = "cluster1-ssl"
		operatorPass = "operator-password"
		podCount     = 3
		reloadStmt   = "ALTER INSTANCE RELOAD TLS"
		stderrDenied = "ERROR 1227 (42000): Access denied\n"

		caPEM      = "ca-certificate"
		certPEM    = "leaf-certificate"
		keyPEM     = "leaf-key"
		oldCertPEM = "old-leaf-certificate"
		oldKeyPEM  = "old-leaf-key"
	)

	leafHash := func(cert, key string) string {
		return fmt.Sprintf("%x", md5.Sum([]byte(cert+key)))
	}

	newCR := func(crVersion string, state apiv1.StatefulAppState, paused bool) *apiv1.PerconaServerMySQL {
		return &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
			Spec: apiv1.PerconaServerMySQLSpec{
				CRVersion:     crVersion,
				SSLSecretName: sslSecret,
				Pause:         paused,
				MySQL: apiv1.MySQLSpec{
					ClusterType: apiv1.ClusterTypeGR,
					PodSpec:     apiv1.PodSpec{Size: podCount},
				},
			},
			Status: apiv1.PerconaServerMySQLStatus{State: state},
		}
	}

	newSTS := func(cr *apiv1.PerconaServerMySQL, lastReloaded string) *appsv1.StatefulSet {
		annotations := make(map[string]string)
		if lastReloaded != "" {
			annotations[naming.AnnotationLastReloadedTLS.String()] = lastReloaded
		}

		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        mysql.Name(cr),
				Namespace:   cr.Namespace,
				Annotations: annotations,
			},
			Spec: appsv1.StatefulSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: mysql.MatchLabels(cr)},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: mysql.MatchLabels(cr)},
				},
			},
		}
	}

	newTLSSecret := func(cert, key string) client.Object {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: sslSecret, Namespace: ns},
			Data: map[string][]byte{
				naming.TLSCAKey:   []byte(caPEM),
				naming.TLSCertKey: []byte(cert),
				naming.TLSKeyKey:  []byte(key),
			},
		}
	}

	newInternalSecret := func() client.Object {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "internal-" + crName, Namespace: ns},
			Data:       map[string][]byte{string(apiv1.UserOperator): []byte(operatorPass)},
		}
	}

	podName := func(idx int) string {
		return fmt.Sprintf("%s-mysql-%d", crName, idx)
	}

	newPods := func(count int) []client.Object {
		cr := newCR("", "", false)

		objs := make([]client.Object, 0, count)
		for i := range count {
			objs = append(objs, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName(i),
					Namespace: ns,
					Labels:    mysql.MatchLabels(cr),
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			})
		}
		return objs
	}

	world := func(running int) []client.Object {
		return append([]client.Object{newInternalSecret()}, newPods(running)...)
	}

	catCmd := []string{
		"cat",
		mysql.TLSMountPath + "/" + naming.TLSCertKey,
		mysql.TLSMountPath + "/" + naming.TLSKeyKey,
	}

	reloadCmd := func(cr *apiv1.PerconaServerMySQL, pod string) []string {
		return []string{
			"mysql",
			"--database", "performance_schema",
			"-p" + operatorPass,
			"-u", string(apiv1.UserOperator),
			"-h", pod + "." + mysql.ServiceName(cr) + "." + cr.Namespace,
			"-e", reloadStmt,
		}
	}

	tests := map[string]struct {
		crVersion    string // defaults to 1.3.0
		state        apiv1.StatefulAppState
		paused       bool
		secret       client.Object   // the TLS secret; nil means it does not exist
		lastReloaded string          // hash on the statefulset annotation; empty means absent
		object       []client.Object // what the API holds besides CR, statefulset and TLS secret; nil means a healthy cluster

		podCert      string // certificate the pods hold on disk; defaults to the one in the secret
		podKey       string
		expectReload bool // ALTER INSTANCE RELOAD TLS expected on every pod
		expectedErr  error
		expectedHash string // last reloaded annotation expected afterwards; empty means absent
	}{
		"version gate skips 1.2.0": {
			crVersion:    "1.2.0",
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
		"new cluster records the hash without touching mysql": {
			state:        apiv1.StateNew,
			secret:       newTLSSecret(certPEM, keyPEM),
			expectedHash: leafHash(certPEM, keyPEM),
		},
		"cluster without a recorded hash records it": {
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			expectedHash: leafHash(certPEM, keyPEM),
		},
		"missing TLS secret is ignored": {
			state:        apiv1.StateReady,
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
		"secret without a certificate is ignored": {
			state: apiv1.StateReady,
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: sslSecret, Namespace: ns},
				Data:       map[string][]byte{naming.TLSCAKey: []byte(caPEM)},
			},
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
		"unchanged certificate runs no statements": {
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(certPEM, keyPEM),
			expectedHash: leafHash(certPEM, keyPEM),
		},
		"rotated certificate is reloaded on every pod": {
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			expectReload: true,
			expectedHash: leafHash(certPEM, keyPEM),
		},
		"initializing cluster is reloaded": {
			state:        apiv1.StateInitializing,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			expectReload: true,
			expectedHash: leafHash(certPEM, keyPEM),
		},
		"paused cluster is deferred": {
			state:        apiv1.StatePaused,
			paused:       true,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			object:       world(0),
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
		"reload is deferred until every pod is running": {
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			object:       world(podCount - 1),
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
		// kubelet refreshes the mounted secret on its own schedule, and reloading
		// before it lands would leave the new certificate unused.
		"reload is deferred until the certificate reaches the pods": {
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			podCert:      oldCertPEM,
			podKey:       oldKeyPEM,
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
		"sql error is returned and the hash is not recorded": {
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			expectReload: true,
			expectedErr:  errors.New("reload TLS on pod " + podName(0)),
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
		"missing operator password is returned": {
			state:        apiv1.StateReady,
			secret:       newTLSSecret(certPEM, keyPEM),
			lastReloaded: leafHash(oldCertPEM, oldKeyPEM),
			object:       newPods(podCount), // everything but the internal secret
			expectedErr:  errors.New("get operator password"),
			expectedHash: leafHash(oldCertPEM, oldKeyPEM),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			scheme := newScheme(t)

			crVersion := tt.crVersion
			if crVersion == "" {
				crVersion = "1.3.0"
			}
			cr := newCR(crVersion, tt.state, tt.paused)
			sts := newSTS(cr, tt.lastReloaded)

			objs := []client.Object{cr, sts.DeepCopy()}
			if tt.secret != nil {
				objs = append(objs, tt.secret)
			}
			if tt.object != nil {
				objs = append(objs, tt.object...)
			} else {
				objs = append(objs, world(podCount)...)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

			podCert, podKey := tt.podCert, tt.podKey
			if podCert == "" {
				podCert, podKey = certPEM, keyPEM
			}

			cliCmd := clientcmdmock.NewClient(t)

			catPods := 0
			switch {
			case tt.podCert != "":
				catPods = 1
			case tt.expectReload || tt.expectedErr != nil:
				catPods = podCount
			}
			for i := range catPods {
				pod := podName(i)
				cliCmd.On("Exec",
					mock.Anything,
					mock.MatchedBy(func(p *corev1.Pod) bool { return p.Name == pod }),
					"mysql",
					catCmd,
					mock.Anything, // stdin
					mock.Anything, // stdout
					mock.Anything, // stderr
					false,         // tty
				).Return(nil).Once().Run(func(args mock.Arguments) {
					_, _ = args.Get(5).(io.Writer).Write([]byte(podCert + podKey))
				})
			}

			if tt.expectReload {
				pods := podCount
				if tt.expectedErr != nil {
					pods = 1
				}
				for i := range pods {
					pod := podName(i)
					call := cliCmd.On("Exec",
						mock.Anything,
						mock.MatchedBy(func(p *corev1.Pod) bool { return p.Name == pod }),
						"mysql",
						reloadCmd(cr, pod),
						mock.Anything,
						mock.Anything,
						mock.Anything,
						false,
					).Return(nil).Once()

					if tt.expectedErr != nil {
						call.Run(func(args mock.Arguments) {
							_, _ = args.Get(6).(io.Writer).Write([]byte(stderrDenied))
						})
					}
				}
			}

			r := &PerconaServerMySQLReconciler{Client: cl, Scheme: scheme, ClientCmd: cliCmd}

			err := r.reconcileTLSReload(ctx, cr, sts)
			if tt.expectedErr != nil {
				require.ErrorContains(t, err, tt.expectedErr.Error())
			} else {
				require.NoError(t, err)
			}

			updated := new(appsv1.StatefulSet)
			require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, updated))

			hash, ok := updated.Annotations[naming.AnnotationLastReloadedTLS.String()]
			if tt.expectedHash == "" {
				assert.False(t, ok, "last reloaded TLS annotation must be absent")
			} else {
				assert.Equal(t, tt.expectedHash, hash, "last reloaded TLS annotation")
			}
		})
	}
}
