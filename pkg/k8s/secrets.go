package k8s

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

func UserPassword(cl client.Client, cr *v2.PerconaServerForMySQL, username string) (string, error) {
	secret := &corev1.Secret{}
	err := cl.Get(context.TODO(), types.NamespacedName{Name: cr.Spec.SecretsName, Namespace: cr.Namespace}, secret)
	if err != nil {
		return "", errors.Wrapf(err, "get secret %s", cr.Spec.SecretsName)
	}

	pass, ok := secret.Data[username]
	if !ok {
		return "", errors.Errorf("no password for %s in secret %s", username, cr.Spec.SecretsName)
	}

	return string(pass), nil
}
