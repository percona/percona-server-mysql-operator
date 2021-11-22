package k8s

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv2 "github.com/percona/percona-server-mysql-operator/api/v2"
)

// SecretKeySelector is a k8s helper to create SecretKeySelector object
func SecretKeySelector(name, key string) *corev1.SecretKeySelector {
	return &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
	}
}

func UserPassword(ctx context.Context, get APIGetter, cr *apiv2.PerconaServerForMySQL, username apiv2.SystemUser) (string, error) {
	nn := types.NamespacedName{
		Name:      cr.Spec.SecretsName,
		Namespace: cr.Namespace,
	}

	secret := &corev1.Secret{}
	if err := get.Get(ctx, nn, secret); err != nil {
		return "", errors.Wrapf(err, "get secret %s", cr.Spec.SecretsName)
	}

	pass, ok := secret.Data[string(username)]
	if !ok {
		return "", errors.Errorf("no password for %s in secret %s", username, cr.Spec.SecretsName)
	}

	return string(pass), nil
}
