package cluster

import (
	"context"
	"crypto/rand"
	"math/big"
	mrand "math/rand"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v2 "github.com/percona/percona-mysql/pkg/api/v2"
	"github.com/percona/percona-mysql/pkg/k8s"
)

const (
	passwordMaxLen = 20
	passwordMinLen = 16
	passSymbols    = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"abcdefghijklmnopqrstuvwxyz" +
		"0123456789"
)

// generatePass generates a random password
func generatePass() ([]byte, error) {
	mrand.Seed(time.Now().UnixNano())
	ln := mrand.Intn(passwordMaxLen-passwordMinLen) + passwordMinLen
	b := make([]byte, ln)
	for i := 0; i < ln; i++ {
		randInt, err := rand.Int(rand.Reader, big.NewInt(int64(len(passSymbols))))
		if err != nil {
			return nil, errors.Wrap(err, "get rand int")
		}
		b[i] = passSymbols[randInt.Int64()]
	}

	return b, nil
}

func (r *MySQLReconciler) reconcileUsersSecret(cr *v2.PerconaServerForMySQL) error {
	secretObj := corev1.Secret{}
	err := r.Client.Get(context.TODO(),
		types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.Spec.SecretsName,
		},
		&secretObj,
	)
	if err == nil {
		return nil
	} else if !k8serror.IsNotFound(err) {
		return errors.Wrap(err, "get secret")
	}

	users := []string{
		v2.USERS_SECRET_KEY_ROOT,
		v2.USERS_SECRET_KEY_XTRABACKUP,
		v2.USERS_SECRET_KEY_MONITOR,
		v2.USERS_SECRET_KEY_CLUSTERCHECK,
		v2.USERS_SECRET_KEY_PROXYADMIN,
		v2.USERS_SECRET_KEY_OPERATOR,
		v2.USERS_SECRET_KEY_REPLICATION,
		v2.USERS_SECRET_KEY_ORCHESTRATOR,
	}
	data := make(map[string][]byte)
	for _, user := range users {
		data[user], err = generatePass()
		if err != nil {
			return errors.Wrapf(err, "create %s user password", user)
		}
	}

	secretObj = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Spec.SecretsName,
			Namespace: cr.Namespace,
		},
		Data: data,
		Type: corev1.SecretTypeOpaque,
	}

	if err := k8s.SetControllerReference(cr, &secretObj, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", secretObj.Kind, secretObj.Name)
	}

	err = r.Client.Create(context.TODO(), &secretObj)
	return errors.Wrapf(err, "create users secret '%s'", cr.Spec.SecretsName)
}
