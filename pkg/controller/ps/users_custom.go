package ps

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strconv"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	secretutil "github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/users"
)

const defaultCustomUserSecretKey = "password"

func (r *PerconaServerMySQLReconciler) reconcileCustomUsers(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	if cr.Status.State != apiv1.StateReady {
		return nil
	}

	um, err := r.newUserManager(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "create users manager")
	}
	defer um.Close() //nolint:errcheck

	for _, user := range cr.Spec.Users {
		if err := r.reconcileCustomUser(ctx, um, cr, &user); err != nil {
			return errors.Wrapf(err, "reconcile user %s", user.Name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileCustomUser(ctx context.Context, um users.Manager, cr *apiv1.PerconaServerMySQL, user *apiv1.User) error {
	log := logf.FromContext(ctx)
	sysUsers := allSystemUsers(cr)

	if _, ok := sysUsers[apiv1.SystemUser(user.Name)]; ok {
		log.Info("Skipping system user", "user", user.Name, "reason", "User name is reserved for system users")
		return nil
	}

	user = user.DeepCopy()
	if len(user.Hosts) == 0 {
		user.Hosts = []string{"%"}
	}

	userPass, err := getUserPassword(ctx, r.Client, cr, user)
	if err != nil {
		return errors.Wrap(err, "get user password")
	}

	userHash, err := getUserObjectHash(user)
	if err != nil {
		return errors.Wrap(err, "get user object hash")
	}

	observedHash, err := getObservedUserHash(ctx, r.Client, cr, user)
	if err != nil {
		return errors.Wrap(err, "get observed user hash")
	}

	if userHash != observedHash {
		log.Info("User config changed, upserting user", "user", user.Name)
		if err := upsertCustomUser(ctx, r.Client, um, cr, user, string(userPass), userHash); err != nil {
			return errors.Wrap(err, "upsert user")
		}
	}

	observedPass, err := getObservedUserPassword(ctx, r.Client, cr, user)
	if err != nil {
		return errors.Wrap(err, "get observed user password")
	}

	if !bytes.Equal(userPass, observedPass) {
		log.Info("User password changed, updating user password", "user", user.Name)
		if err := updateUserPassword(ctx, r.Client, um, cr, user, string(userPass)); err != nil {
			return errors.Wrap(err, "update user password")
		}
	}
	return nil
}

func upsertCustomUser(ctx context.Context, cl client.Client, um users.Manager, cr *apiv1.PerconaServerMySQL, user *apiv1.User, pass string, hash string) error {
	log := logf.FromContext(ctx)
	log.Info("Update user config", "user", user.Name)

	if err := um.UpsertUser(ctx, user, pass); err != nil {
		return errors.Wrap(err, "upsert user")
	}

	if err := patchInternalUserSecret(ctx, cl, cr, func(data map[string][]byte) {
		data[user.Name+".md5"] = []byte(hash)
	}); err != nil {
		return errors.Wrap(err, "patch internal user secret")
	}

	return nil
}

func patchInternalUserSecret(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
	mutate func(data map[string][]byte),
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		internalSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cr.InternalCustomUserSecretName(),
				Namespace: cr.GetNamespace(),
			},
		}

		if err := cl.Get(ctx, client.ObjectKeyFromObject(internalSecret), internalSecret); err != nil {
			return err
		}

		orig := internalSecret.DeepCopy()
		if internalSecret.Data == nil {
			internalSecret.Data = make(map[string][]byte)
		}
		mutate(internalSecret.Data)

		return cl.Patch(ctx, internalSecret, client.MergeFrom(orig))
	})
}

func updateUserPassword(ctx context.Context, cl client.Client, um users.Manager, cr *apiv1.PerconaServerMySQL, user *apiv1.User, pass string) error {
	log := logf.FromContext(ctx)
	log.Info("Updating user password", "user", user.Name)

	if err := um.AlterUser(ctx, user, pass); err != nil {
		return errors.Wrap(err, "alter user")
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		internalSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cr.InternalCustomUserSecretName(),
				Namespace: cr.GetNamespace(),
			},
		}

		if err := cl.Get(ctx, client.ObjectKeyFromObject(internalSecret), internalSecret); err != nil {
			return err
		}

		orig := internalSecret.DeepCopy()
		if internalSecret.Data == nil {
			internalSecret.Data = make(map[string][]byte)
		}
		internalSecret.Data[user.Name] = []byte(pass)

		return cl.Patch(ctx, internalSecret, client.MergeFrom(orig))
	}); err != nil {
		return errors.Wrap(err, "update user password in k8s secret")
	}
	return nil
}

func getObservedUserHash(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
	user *apiv1.User,
) (string, error) {
	internalSecret, err := getInternalCustomUserSecret(ctx, cl, cr)
	if err != nil {
		return "", errors.Wrap(err, "get internal custom user secret")
	}

	hash := internalSecret.Data[user.Name+".md5"]
	return string(hash), nil
}

func getObservedUserPassword(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
	user *apiv1.User,
) ([]byte, error) {
	internalSecret, err := getInternalCustomUserSecret(ctx, cl, cr)
	if err != nil {
		return nil, errors.Wrap(err, "get internal custom user secret")
	}

	pass := internalSecret.Data[user.Name]
	return pass, nil
}

func getUserPassword(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
	user *apiv1.User,
) ([]byte, error) {
	secret, err := getCustomUserSecret(ctx, cl, cr, user)
	if err != nil {
		return nil, errors.Wrap(err, "get user secret")
	}

	key := defaultCustomUserSecretKey
	if user.PasswordSecretRef != nil && user.PasswordSecretRef.Key != "" {
		key = user.PasswordSecretRef.Key
	}
	return secret.Data[key], nil
}

func getInternalCustomUserSecret(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
) (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.InternalCustomUserSecretName(),
			Namespace: cr.GetNamespace(),
		},
	}

	if err := cl.Get(ctx, client.ObjectKeyFromObject(secret), secret); err == nil {
		return secret, nil
	} else if !k8serrors.IsNotFound(err) {
		return nil, errors.Wrap(err, "get internal custom user secret")
	}

	if err := controllerutil.SetControllerReference(cr, secret, cl.Scheme()); err != nil {
		return nil, errors.Wrap(err, "set controller reference for internal custom user secret")
	}

	if err := cl.Create(ctx, secret); err != nil {
		return nil, errors.Wrap(err, "create internal custom user secret")
	}
	return secret, nil
}

func getCustomUserSecret(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
	user *apiv1.User,
) (*corev1.Secret, error) {
	secretName := cr.DefaultCustomUserSecretName(*user)
	secretKey := defaultCustomUserSecretKey
	if user.PasswordSecretRef != nil {
		secretName = user.PasswordSecretRef.Name
		if user.PasswordSecretRef.Key != "" {
			secretKey = user.PasswordSecretRef.Key
		}
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cr.GetNamespace(),
		},
	}

	if err := cl.Get(ctx, client.ObjectKeyFromObject(secret), secret); err == nil {
		return secret, nil
	} else if !k8serrors.IsNotFound(err) {
		return nil, errors.Wrap(err, "get user secret")
	}

	pass, err := secretutil.GeneratePass()
	if err != nil {
		return nil, errors.Wrap(err, "generate password")
	}

	secret.Data = map[string][]byte{
		secretKey: pass,
	}

	if err := cl.Create(ctx, secret); err != nil {
		return nil, errors.Wrap(err, "create user secret")
	}

	return secret, nil
}

func getUserObjectHash(user *apiv1.User) (string, error) {
	userCpy := user.DeepCopy()
	slices.Sort(userCpy.Hosts)
	slices.Sort(userCpy.DBs)
	slices.Sort(userCpy.Grants)

	data, err := json.Marshal(userCpy)
	if err != nil {
		return "", err
	}

	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:]), nil
}

func (r *PerconaServerMySQLReconciler) newUserManager(ctx context.Context, cr *apiv1.PerconaServerMySQL) (users.Manager, error) {
	opPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return nil, errors.Wrap(err, "get operator password")
	}

	host, err := appHost(ctx, r.Client, cr)
	if err != nil {
		return nil, errors.Wrap(err, "get app host")
	}

	addr := net.JoinHostPort(host, strconv.Itoa(mysql.DefaultPort))

	um, err := users.NewManager(addr, string(apiv1.UserOperator), opPass, cr.Spec.MySQL.ReadinessProbe.TimeoutSeconds)
	if err != nil {
		return nil, errors.Wrap(err, "create users manager")
	}

	return um, nil
}
