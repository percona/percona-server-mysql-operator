package k8s

import (
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestUserPassword(t *testing.T) {
	const (
		crName = "cluster1"
		ns     = "secrets-ns"
	)

	cr := &apiv1.PerconaServerMySQL{
		Name: crName, Namespace: ns,
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := map[string]struct {
		objects    []client.Object
		user       apiv1.SystemUser
		want       string
		wantErr    error
		wantErrMsg string
	}{
		"returns the password": {
			objects: []client.Object{&corev1.Secret{
				Name: cr.InternalSecretName(), Namespace: ns,
				Data: map[string][]byte{string(apiv1.UserConfigurator): []byte("cfg-pass")},
			}},
			user: apiv1.UserConfigurator,
			want: "cfg-pass",
		},
		"user absent from the secret": {
			objects: []client.Object{&corev1.Secret{
				Name: cr.InternalSecretName(), Namespace: ns,
				Data: map[string][]byte{string(apiv1.UserOperator): []byte("op-pass")},
			}},
			user:       apiv1.UserConfigurator,
			wantErr:    ErrPasswordNotFound,
			wantErrMsg: "no password for configurator in secret internal-cluster1: password not found in secret",
		},
		"secret missing entirely": {
			user:       apiv1.UserConfigurator,
			wantErrMsg: `get secret/internal-cluster1: secrets "internal-cluster1" not found`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objects...).Build()

			got, err := UserPassword(t.Context(), cl, cr, tc.user)
			if tc.wantErrMsg != "" {
				require.EqualError(t, err, tc.wantErrMsg)
				assert.Empty(t, got)
				assert.Equal(t, tc.wantErr != nil, stderrors.Is(err, ErrPasswordNotFound))
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
