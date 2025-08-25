package k8s

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

// SecretKeySelector is a k8s helper to create SecretKeySelector object
func SecretKeySelector(name, key string) *corev1.SecretKeySelector {
	return &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
	}
}

func UserPassword(ctx context.Context, cl client.Reader, cr *apiv1.PerconaServerMySQL, username apiv1.SystemUser) (string, error) {
	nn := types.NamespacedName{
		Name:      cr.InternalSecretName(),
		Namespace: cr.Namespace,
	}

	secret := &corev1.Secret{}
	if err := cl.Get(ctx, nn, secret); err != nil {
		return "", errors.Wrapf(err, "get secret/%s", nn.Name)
	}

	pass, ok := secret.Data[string(username)]
	if !ok {
		return "", errors.Errorf("no password for %s in secret %s", username, nn.Name)
	}

	return string(pass), nil
}
