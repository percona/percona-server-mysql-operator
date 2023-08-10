package ps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/users"
)

const (
	annotationPasswordsUpdated string = "percona.com/passwords-updated"
)

var ErrPassNotPropagated = errors.New("password not yet propagated")

func allSystemUsers() map[apiv1alpha1.SystemUser]mysql.User {
	uu := [...]apiv1alpha1.SystemUser{
		apiv1alpha1.UserHeartbeat,
		apiv1alpha1.UserMonitor,
		apiv1alpha1.UserOperator,
		apiv1alpha1.UserOrchestrator,
		apiv1alpha1.UserReplication,
		apiv1alpha1.UserRoot,
		apiv1alpha1.UserXtraBackup,
	}

	users := make(map[apiv1alpha1.SystemUser]mysql.User, len(uu))
	for _, u := range uu {
		user := mysql.User{
			Username: u,
			Hosts:    []string{"%"},
		}

		switch u {
		case apiv1alpha1.UserRoot:
			user.Hosts = append(user.Hosts, "localhost")
		case apiv1alpha1.UserHeartbeat, apiv1alpha1.UserXtraBackup:
			user.Hosts = []string{"localhost"}
		}

		users[u] = user
	}

	return users
}

func (r *PerconaServerMySQLReconciler) ensureUserSecrets(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.SecretsName,
	}

	userSecret := new(corev1.Secret)

	if err := r.Get(ctx, nn, userSecret); client.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "get user secret")
	}
	err := secret.FillPasswordsSecret(cr, userSecret)
	if err != nil {
		return errors.Wrap(err, "fill passwords")
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

	allUsers := allSystemUsers()
	if cr.MySQLSpec().IsGR() {
		delete(allUsers, apiv1alpha1.UserReplication)
	}

	if hash == internalHash {
		if v, ok := internalSecret.Annotations[annotationPasswordsUpdated]; ok && v == "false" {
			operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
			if err != nil {
				return errors.Wrap(err, "get operator password")
			}

			// At this point we don't know exact updated users and we pass all system users.
			// Discarding old password is idempotent so it is safe to pass not updated user.
			// We can improve this by maybe storing updated users in a annotation and reading it here.
			users := make([]mysql.User, 0)
			for _, u := range allUsers {
				users = append(users, u)
			}
			return r.discardOldPasswordsAfterNewPropagated(ctx, cr, internalSecret, users, operatorPass)
		}

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

		mysqlUser := allUsers[apiv1alpha1.SystemUser(user)]
		mysqlUser.Password = string(pass)

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

	k8s.AddAnnotation(internalSecret, string(annotationPasswordsUpdated), "false")
	err = r.Client.Update(ctx, internalSecret)
	if err != nil {
		return errors.Wrap(err, "update internal sys users secret annotation")
	}

	return r.discardOldPasswordsAfterNewPropagated(ctx, cr, internalSecret, updatedUsers, operatorPass)
}

func (r *PerconaServerMySQLReconciler) discardOldPasswordsAfterNewPropagated(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
	secrets *corev1.Secret,
	updatedUsers []mysql.User,
	operatorPass string) error {

	log := logf.FromContext(ctx)

	err := r.passwordsPropagated(ctx, cr, secrets)
	if err != nil && err == ErrPassNotPropagated {
		if err == ErrPassNotPropagated {
			log.Info("Waiting for passwords to be propagated")
			return nil
		}
		return err
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

	if err := um.DiscardOldPasswords(updatedUsers); err != nil {
		return errors.Wrap(err, "discard old passwords")
	}

	log.Info("Discarded old user passwords")

	k8s.AddAnnotation(secrets, annotationPasswordsUpdated, "true")
	err = r.Client.Update(ctx, secrets)
	if err != nil {
		return errors.Wrap(err, "update internal sys users secret annotation")
	}
	return nil
}

func (r *PerconaServerMySQLReconciler) passwordsPropagated(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, secrets *corev1.Secret) error {
	log := logf.FromContext(ctx)

	type component struct {
		name      string
		size      int
		credsPath string
	}
	components := []component{
		{
			name:      mysql.ComponentName,
			size:      int(cr.MySQLSpec().Size),
			credsPath: mysql.CredsMountPath,
		},
	}

	if cr.MySQLSpec().IsAsync() {
		components = append(components, component{
			name:      orchestrator.ComponentName,
			size:      int(cr.Spec.Orchestrator.Size),
			credsPath: orchestrator.CredsMountPath,
		})
	}

	if cr.HAProxyEnabled() {
		components = append(components, component{
			name:      haproxy.ComponentName,
			size:      int(cr.Spec.Proxy.HAProxy.Size),
			credsPath: haproxy.CredsMountPath,
		})
	}

	if cr.RouterEnabled() {
		components = append(components, component{
			name:      router.ComponentName,
			size:      int(cr.Spec.Proxy.HAProxy.Size),
			credsPath: router.CredsMountPath,
		})
	}

	eg := new(errgroup.Group)

	for _, component := range components {
		comp := component

		log.Info("Checking if password is propagated for component", "component", comp.name)

		eg.Go(func() error {
			for i := 0; int32(i) < int32(comp.size); i++ {
				pod := corev1.Pod{}
				err := r.Client.Get(ctx,
					types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      fmt.Sprintf("%s-%s-%d", cr.Name, comp.name, i),
					},
					&pod,
				)
				if err != nil && k8serrors.IsNotFound(err) {
					return err
				} else if err != nil {
					return errors.Wrapf(err, "get %s pod", comp.name)
				}

				// TODO: Improve this by sending single cmd request insted for each user separately
				for user, pass := range secrets.Data {
					cmd := []string{"cat", fmt.Sprintf("%s/%s", comp.credsPath, user)}
					var errb, outb bytes.Buffer
					err = r.ClientCmd.Exec(ctx, &pod, comp.name, cmd, nil, &outb, &errb, false)
					if err != nil {
						if strings.Contains(errb.String(), "No such file or directory") {
							return nil
						}
						return errors.Errorf("exec cat on %s-%d: %v / %s / %s", comp.name, i, err, outb.String(), errb.String())
					}
					if len(errb.Bytes()) > 0 {
						return errors.Errorf("cat on %s-%s-%d: %s", cr.Name, comp.name, i, errb.String())
					}

					if outb.String() != string(pass) {
						return ErrPassNotPropagated
					}
				}
			}

			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	log.Info("Updated password propagated")
	return nil
}
