package ps

import (
	"bytes"
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/users"
)

func (r *PerconaServerMySQLReconciler) ensureUserSecrets(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.SecretsName,
	}

	userSecret := new(corev1.Secret)

	if err := r.Get(ctx, nn, userSecret); client.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "get user secret")
	}
	err := secret.FillPasswordsSecret(userSecret)
	if err != nil {

	}
	userSecret.Name = cr.Spec.SecretsName
	userSecret.Namespace = cr.Namespace
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, nil, userSecret, r.Scheme); err != nil {
		return errors.Wrap(err, "ensure user secret")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileUsers(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileUsers")

	secret := &corev1.Secret{}
	nn := types.NamespacedName{Name: cr.Spec.SecretsName, Namespace: cr.Namespace}
	if err := r.Client.Get(ctx, nn, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "get Secret/%s", nn.Name)
	}

	internalSecret := &corev1.Secret{}
	nn.Name = cr.InternalSecretName()
	err := r.Client.Get(ctx, nn, internalSecret)
	if err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrapf(err, "get Secret/%s", nn.Name)
	}

	// Internal secret is not found
	if k8serrors.IsNotFound(err) {
		secret.DeepCopyInto(internalSecret)
		internalSecret.ObjectMeta = metav1.ObjectMeta{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
		}

		if err = r.Client.Create(ctx, internalSecret); err != nil {
			return errors.Wrapf(err, "create secret %s", internalSecret.Name)
		}

		return nil
	}

	hash, err := k8s.ObjectHash(secret)
	if err != nil {
		return errors.Wrapf(err, "get secret/%s hash", secret.Name)
	}

	internalHash, err := k8s.ObjectHash(internalSecret)
	if err != nil {
		return errors.Wrapf(err, "get secret/%s hash", internalSecret.Name)
	}

	if hash == internalHash {
		log.V(1).Info("Secret data is up to date")
		return nil
	}

	if cr.Status.MySQL.State != apiv1alpha1.StateReady {
		log.Info("MySQL is not ready")
		return nil
	}

	var (
		restartMySQL        bool
		restartReplication  bool
		restartOrchestrator bool
	)
	updatedUsers := make([]mysql.User, 0)
	for user, pass := range secret.Data {
		if bytes.Equal(pass, internalSecret.Data[user]) {
			log.V(1).Info("User password is up to date", "user", user)
			continue
		}

		mysqlUser := mysql.User{
			Username: apiv1alpha1.SystemUser(user),
			Password: string(pass),
			Hosts:    []string{"%"},
		}

		switch mysqlUser.Username {
		case apiv1alpha1.UserMonitor:
			restartMySQL = cr.PMMEnabled(internalSecret)
		case apiv1alpha1.UserPMMServerKey:
			restartMySQL = cr.PMMEnabled(internalSecret)
			continue // PMM server user credentials are not stored in db
		case apiv1alpha1.UserReplication:
			restartReplication = true
		case apiv1alpha1.UserOrchestrator:
			restartOrchestrator = true && cr.Spec.MySQL.IsAsync()
		case apiv1alpha1.UserRoot:
			mysqlUser.Hosts = append(mysqlUser.Hosts, "localhost")
		case apiv1alpha1.UserHeartbeat, apiv1alpha1.UserXtraBackup:
			mysqlUser.Hosts = []string{"localhost"}
		}

		log.V(1).Info("User password changed", "user", user)

		updatedUsers = append(updatedUsers, mysqlUser)
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	primaryHost, err := r.getPrimaryHost(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "get primary host")
	}
	log.V(1).Info("Got primary host", "primary", primaryHost)

	idx, err := getPodIndexFromHostname(primaryHost)
	if err != nil {
		return err
	}
	primPod, err := getMySQLPod(ctx, r.Client, cr, idx)
	if err != nil {
		return err
	}

	um, err := users.NewManagerExec(primPod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, primaryHost)
	if err != nil {
		return errors.Wrap(err, "init user manager")
	}
	defer um.Close()

	var asyncPrimary *orchestrator.Instance

	if restartReplication {
		if cr.Spec.MySQL.IsAsync() {
			asyncPrimary, err = r.getPrimaryFromOrchestrator(ctx, cr)
			if err != nil {
				return errors.Wrap(err, "get cluster primary")
			}
			if err := r.stopAsyncReplication(ctx, cr, asyncPrimary); err != nil {
				return errors.Wrap(err, "stop async replication")
			}
		}
	}

	if err := um.UpdateUserPasswords(updatedUsers); err != nil {
		return errors.Wrapf(err, "update passwords")
	}

	if restartReplication {
		var updatedReplicaPass string
		for _, user := range updatedUsers {
			if user.Username == apiv1alpha1.UserReplication {
				updatedReplicaPass = user.Password
				break
			}
		}

		if cr.Spec.MySQL.IsAsync() {
			if err := r.startAsyncReplication(ctx, cr, updatedReplicaPass, asyncPrimary); err != nil {
				return errors.Wrap(err, "start async replication")
			}
		}
	}

	if cr.OrchestratorEnabled() && restartOrchestrator {
		log.Info("Orchestrator password updated. Restarting orchestrator.")

		sts := &appsv1.StatefulSet{}
		if err := r.Client.Get(ctx, orchestrator.NamespacedName(cr), sts); err != nil {
			return errors.Wrap(err, "get Orchestrator statefulset")
		}
		if err := k8s.RolloutRestart(ctx, r.Client, sts, apiv1alpha1.AnnotationSecretHash, hash); err != nil {
			return errors.Wrap(err, "restart orchestrator")
		}
	}

	if restartMySQL {
		log.Info("Monitor user password updated. Restarting MySQL.")

		sts := &appsv1.StatefulSet{}
		if err := r.Client.Get(ctx, mysql.NamespacedName(cr), sts); err != nil {
			return errors.Wrap(err, "get MySQL statefulset")
		}
		if err := k8s.RolloutRestart(ctx, r.Client, sts, apiv1alpha1.AnnotationSecretHash, hash); err != nil {
			return errors.Wrap(err, "restart MySQL")
		}
	}

	if cr.Status.State != apiv1alpha1.StateReady {
		log.Info("Waiting cluster to be ready")
		return nil
	}

	internalSecret.Data = secret.DeepCopy().Data
	if err := r.Client.Update(ctx, internalSecret); err != nil {
		return errors.Wrapf(err, "update Secret/%s", internalSecret.Name)
	}

	log.Info("Updated internal secret", "secretName", cr.InternalSecretName())

	// TODO: Wait for pass propagation

	primaryHost, err = r.getPrimaryHost(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "get primary host")
	}
	log.V(1).Info("Got primary host", "primary", primaryHost)

	// TODO: handle this differently
	// Because how currently we implement manager.DiscardOldPasswords,
	// (as well ass manger.UpdateUserPasswords), we need updated operator user pass
	// to perform DiscardOldPasswords properly.
	updatedOperatorPass := operatorPass
	for _, user := range updatedUsers {
		if user.Username == apiv1alpha1.UserOperator {
			updatedOperatorPass = user.Password
			break
		}
	}

	um, err = users.NewManagerExec(primPod, r.ClientCmd, apiv1alpha1.UserOperator, updatedOperatorPass, primaryHost)
	if err != nil {
		return errors.Wrap(err, "init user manager")
	}
	defer um.Close()

	if err := um.DiscardOldPasswords(updatedUsers); err != nil {
		return errors.Wrap(err, "discard old passwords")
	}

	log.Info("Discarded old user passwords")

	return nil
}
