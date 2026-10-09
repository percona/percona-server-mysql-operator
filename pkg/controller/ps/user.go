package ps

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
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

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

var ErrPassNotPropagated = errors.New("password not yet propagated")

func allSystemUsers(cr *apiv1.PerconaServerMySQL) map[apiv1.SystemUser]mysql.User {
	uu := secret.SystemUsers(cr)

	users := make(map[apiv1.SystemUser]mysql.User, len(uu))
	for _, u := range uu {
		user := mysql.User{
			Username: u,
			Hosts:    []string{"%"},
		}

		switch u {
		case apiv1.UserRoot:
			user.Hosts = append(user.Hosts, "localhost")
		case apiv1.UserHeartbeat, apiv1.UserXtraBackup:
			user.Hosts = []string{"localhost"}
		}

		users[u] = user
	}

	return users
}

// ensureUserSecrets reconciles the user secrets for the given cluster.
// It returns the user secret and an error if any.
func (r *PerconaServerMySQLReconciler) ensureUserSecrets(ctx context.Context, cr *apiv1.PerconaServerMySQL) (*corev1.Secret, error) {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.SecretsName,
	}

	userSecret := new(corev1.Secret)

	if err := r.Get(ctx, nn, userSecret); client.IgnoreNotFound(err) != nil {
		return nil, errors.Wrap(err, "get user secret")
	}
	err := secret.FillPasswordsSecret(cr, userSecret)
	if err != nil {
		return nil, errors.Wrap(err, "fill passwords")
	}
	userSecret.Name = cr.Spec.SecretsName
	userSecret.Namespace = cr.Namespace
	userSecret.Labels = util.SSMapMerge(cr.GlobalLabels(), mysql.MatchLabels(cr))
	userSecret.Annotations = util.SSMapMerge(cr.GlobalAnnotations())
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, nil, userSecret, r.Scheme); err != nil {
		return nil, errors.Wrap(err, "ensure user secret")
	}

	return userSecret, nil
}

func mysqlURI(user, pass, host, port string) string {
	var u url.URL

	u.User = url.UserPassword(user, pass)
	u.Scheme = "mysql"
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	return u.String()
}

func (r *PerconaServerMySQLReconciler) ensureClusterUserSecret(ctx context.Context, cr *apiv1.PerconaServerMySQL, userSecret *corev1.Secret) error {
	clusterUser := string(apiv1.UserRoot)
	clusterPass := userSecret.Data[clusterUser]

	secret := &corev1.Secret{
		Name:      cr.Name + "-psuser-" + clusterUser,
		Namespace: cr.Namespace,
	}

	mysqlHost := mysqlPrimaryHost(ctx, cr, true)
	mysqlPort := strconv.Itoa(mysql.DefaultPort)
	mysqlURIStr := mysqlURI(clusterUser, string(clusterPass), mysqlHost, mysqlPort)

	secret.Data = map[string][]byte{
		"host":     []byte(mysqlHost),
		"port":     []byte(mysqlPort),
		"user":     []byte(clusterUser),
		"password": clusterPass,
		"uri":      []byte(mysqlURIStr),
	}

	proxyHost := proxyHost(ctx, cr, true)
	proxyPort := proxyServicePort(cr)
	proxyReadOnlyPort := proxyServicePortReadOnly(cr)
	if proxyHost != "" {
		proxyURI := mysqlURI(clusterUser, string(clusterPass), proxyHost, proxyPort)

		secret.Data["proxy-host"] = []byte(proxyHost)
		secret.Data["proxy-port"] = []byte(proxyPort)
		secret.Data["proxy-uri"] = []byte(proxyURI)

		proxyReadOnlyURI := mysqlURI(clusterUser, string(clusterPass), proxyHost, proxyReadOnlyPort)
		secret.Data["proxy-readonly-host"] = []byte(proxyHost)
		secret.Data["proxy-readonly-port"] = []byte(proxyReadOnlyPort)
		secret.Data["proxy-readonly-uri"] = []byte(proxyReadOnlyURI)
	}

	lbHost, err := loadBalancerHost(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "load balancer host")
	}
	if lbHost != "" {
		lbURI := mysqlURI(clusterUser, string(clusterPass), lbHost, proxyPort)
		secret.Data["proxy-external-host"] = []byte(lbHost)
		secret.Data["proxy-external-port"] = []byte(proxyPort)
		secret.Data["proxy-external-uri"] = []byte(lbURI)

		lbReadOnlyURI := mysqlURI(clusterUser, string(clusterPass), lbHost, proxyReadOnlyPort)
		secret.Data["proxy-readonly-external-host"] = []byte(lbHost)
		secret.Data["proxy-readonly-external-port"] = []byte(proxyReadOnlyPort)
		secret.Data["proxy-readonly-external-uri"] = []byte(lbReadOnlyURI)
	}

	secret.Labels = util.SSMapMerge(cr.GlobalLabels(), mysql.MatchLabels(cr))
	secret.Annotations = util.SSMapMerge(cr.GlobalAnnotations())
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, nil, secret, r.Scheme); err != nil {
		return errors.Wrap(err, "ensure root user secret")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileUsers(ctx context.Context, cr *apiv1.PerconaServerMySQL, secret *corev1.Secret) error {
	log := logf.FromContext(ctx).WithName("reconcileUsers")

	if err := validateUserSecret(cr, secret); err != nil {
		log.Error(err, "User secret is invalid. Passwords and internal secret won't be updated until it becomes valid")
		return nil
	}

	internalSecret := &corev1.Secret{}
	nn := types.NamespacedName{Name: cr.InternalSecretName(), Namespace: cr.GetNamespace()}
	err := r.Client.Get(ctx, nn, internalSecret)
	if err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrapf(err, "get Secret/%s", nn.Name)
	}

	internalMeta := metav1.ObjectMeta{
		Name:        cr.InternalSecretName(),
		Namespace:   cr.Namespace,
		Labels:      util.SSMapMerge(cr.GlobalLabels(), mysql.MatchLabels(cr)),
		Annotations: cr.GlobalAnnotations(),
	}

	// Internal secret is not found
	if k8serrors.IsNotFound(err) {
		secret.DeepCopyInto(internalSecret)
		internalSecret.ObjectMeta = internalMeta

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

	allUsers := allSystemUsers(cr)

	// The old passwords of the previous change must be discarded before another
	// change starts, otherwise the passwords retained for it would be overwritten.
	if v, ok := internalSecret.Annotations[naming.AnnotationPasswordsUpdated.String()]; ok && v == "false" {
		return r.completePasswordChange(ctx, cr, internalSecret, internalHash)
	}

	if hash == internalHash {
		if !k8s.EqualMetadata(internalMeta, internalSecret.ObjectMeta) {
			internalSecret.ObjectMeta = internalMeta
			if err := r.Update(ctx, internalSecret); err != nil {
				return errors.Wrap(err, "update internal secret metadata")
			}
		}

		return nil
	}

	if cr.Status.MySQL.State != apiv1.StateReady {
		log.Info("MySQL is not ready")
		return nil
	}

	appliedCRVersion, err := mysql.GetAppliedCRVersion(ctx, r.Client, cr)
	if err != nil {
		if errors.Is(err, mysql.ErrRolloutInProgress) {
			log.Info("Waiting for mysql pods rollout to complete")
			return nil
		}
		return errors.Wrap(err, "get applied CR version")
	}

	// Wait for the pods to be re-created so that any new users may be created at container startup.
	if cr.CompareVersion("1.3.0") >= 0 && (appliedCRVersion == "" || appliedCRVersion != cr.Spec.CRVersion) {
		log.Info("Waiting for smart update to finish")
		return nil
	}

	// Passwords are changed only when the whole cluster is ready, so the
	// internal secret is updated in the same pass as the passwords.
	if cr.Status.State != apiv1.StateReady {
		if err := r.backfillInternalSecret(ctx, internalSecret, secret); err != nil {
			return err
		}

		log.Info("Waiting cluster to be ready")
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
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
	primPod, err := mysql.GetPod(ctx, r.Client, cr, idx)
	if err != nil {
		return err
	}

	um := db.NewUserManager(primPod, r.ClientCmd, apiv1.UserOperator, operatorPass, primaryHost)

	var restartReplication bool
	updatedUsers := make([]mysql.User, 0)
	changedUsers := make([]apiv1.SystemUser, 0)
	for user, pass := range secret.Data {
		if bytes.Equal(pass, internalSecret.Data[user]) {
			log.V(1).Info("User password is up to date", "user", user)
			continue
		}

		mysqlUser := allUsers[apiv1.SystemUser(user)]
		mysqlUser.Password = string(pass)

		switch apiv1.SystemUser(user) {
		case apiv1.UserPMMServerToken:
			// A restart is needed only when PMM already runs with an older token.
			if cr.PMMEnabled(internalSecret) {
				changedUsers = append(changedUsers, apiv1.UserPMMServerToken)
			}
			continue // PMM server user credentials are not stored in db
		case apiv1.UserReplication:
			restartReplication = true
		}

		log.V(1).Info("User password changed", "user", user)

		updatedUsers = append(updatedUsers, mysqlUser)
		changedUsers = append(changedUsers, apiv1.SystemUser(user))
	}

	var asyncPrimary *orchestrator.Instance

	if restartReplication {
		if cr.AppliedIsAsync() {
			asyncPrimary, err = r.getPrimaryFromOrchestrator(ctx, cr)
			if err != nil {
				return errors.Wrap(err, "get cluster primary")
			}
			if err := r.stopAsyncReplication(ctx, cr, asyncPrimary); err != nil {
				return errors.Wrap(err, "stop async replication")
			}
		}
	}

	if err := um.UpdateUserPasswords(ctx, updatedUsers); err != nil {
		return errors.Wrapf(err, "update passwords")
	}

	if restartReplication {
		var updatedReplicaPass string
		for _, user := range updatedUsers {
			if user.Username == apiv1.UserReplication {
				updatedReplicaPass = user.Password
				break
			}
		}

		if cr.AppliedIsAsync() {
			if err := r.startAsyncReplication(ctx, cr, updatedReplicaPass, asyncPrimary); err != nil {
				return errors.Wrap(err, "start async replication")
			}
		}
	}

	// Restarted pods must mount the new passwords: Orchestrator's subPath mount never sees a later update.
	if err := r.recordPasswordChange(ctx, cr, internalSecret, secret, changedUsers); err != nil {
		return err
	}

	return r.restartForPasswordChange(ctx, cr, internalSecret, changedUsers, hash)
}

// completePasswordChange requests the recorded change's restarts again and discards its old passwords once every pod has the new ones.
func (r *PerconaServerMySQLReconciler) completePasswordChange(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	internalSecret *corev1.Secret,
	hash string,
) error {
	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	allUsers := allSystemUsers(cr)

	v, ok := internalSecret.Annotations[naming.AnnotationPasswordsUpdatedUsers.String()]
	if !ok {
		// Recorded by an operator version that did not store which users changed.
		users := make([]mysql.User, 0, len(allUsers))
		for _, u := range allUsers {
			users = append(users, u)
		}
		return r.discardOldPasswordsAfterNewPropagated(ctx, cr, internalSecret, users, operatorPass)
	}

	changedUsers := splitUsers(v)
	if err := r.restartForPasswordChange(ctx, cr, internalSecret, changedUsers, hash); err != nil {
		return err
	}

	users := make([]mysql.User, 0, len(changedUsers))
	for _, user := range changedUsers {
		if u, ok := allUsers[user]; ok {
			users = append(users, u)
		}
	}
	return r.discardOldPasswordsAfterNewPropagated(ctx, cr, internalSecret, users, operatorPass)
}

func (r *PerconaServerMySQLReconciler) recordPasswordChange(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	internalSecret *corev1.Secret,
	secret *corev1.Secret,
	changedUsers []apiv1.SystemUser,
) error {
	log := logf.FromContext(ctx).WithName("reconcileUsers")

	internalSecret.Data = secret.DeepCopy().Data
	k8s.AddAnnotation(internalSecret, naming.AnnotationPasswordsUpdated.String(), "false")
	k8s.AddAnnotation(internalSecret, naming.AnnotationPasswordsUpdatedUsers.String(), joinUsers(changedUsers))
	if err := r.Update(ctx, internalSecret); err != nil {
		return errors.Wrapf(err, "update Secret/%s", internalSecret.Name)
	}

	log.Info("Updated internal secret", "secretName", cr.InternalSecretName(), "users", joinUsers(changedUsers))

	return nil
}

// restartForPasswordChange restarts, once per change, the components that read the changed passwords only at startup.
func (r *PerconaServerMySQLReconciler) restartForPasswordChange(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	internalSecret *corev1.Secret,
	changedUsers []apiv1.SystemUser,
	hash string,
) error {
	log := logf.FromContext(ctx).WithName("reconcileUsers")

	pmmEnabled := cr.PMMEnabled(internalSecret)

	var restartMySQL, restartHAProxy, restartOrchestrator, restartRouter bool
	for _, user := range changedUsers {
		switch user {
		case apiv1.UserMonitor:
			restartMySQL = restartMySQL || pmmEnabled
		case apiv1.UserOperator:
			restartRouter = cr.RouterEnabled()
		case apiv1.UserPMMServerToken:
			restartMySQL = restartMySQL || pmmEnabled
			restartHAProxy = pmmEnabled && cr.HAProxyEnabled()
		case apiv1.UserOrchestrator:
			restartOrchestrator = cr.OrchestratorEnabled() && cr.AppliedIsAsync()
		}
	}

	workloads := []struct {
		restart bool
		name    string
		key     types.NamespacedName
		obj     client.Object
	}{
		{restartOrchestrator, "Orchestrator", orchestrator.NamespacedName(cr), new(appsv1.StatefulSet)},
		{restartMySQL, "MySQL", mysql.NamespacedName(cr), new(appsv1.StatefulSet)},
		{restartHAProxy, "HAProxy", haproxy.NamespacedName(cr), new(appsv1.StatefulSet)},
		{restartRouter, "Router", types.NamespacedName{Name: router.Name(cr), Namespace: cr.Namespace}, new(appsv1.Deployment)},
	}
	for _, w := range workloads {
		if !w.restart {
			continue
		}

		if err := r.Get(ctx, w.key, w.obj); err != nil {
			return errors.Wrapf(err, "get %s", w.name)
		}
		if podTemplateSecretHash(w.obj) == hash {
			continue
		}

		log.Info("Restarting to load the changed passwords", "component", w.name, "users", joinUsers(changedUsers))
		if err := k8s.RolloutRestart(ctx, r.Client, w.obj, naming.AnnotationSecretHash, hash); err != nil {
			return errors.Wrapf(err, "restart %s", w.name)
		}
	}

	return nil
}

func podTemplateSecretHash(obj client.Object) string {
	switch obj := obj.(type) {
	case *appsv1.StatefulSet:
		return obj.Spec.Template.Annotations[naming.AnnotationSecretHash.String()]
	case *appsv1.Deployment:
		return obj.Spec.Template.Annotations[naming.AnnotationSecretHash.String()]
	}
	return ""
}

func joinUsers(users []apiv1.SystemUser) string {
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, string(u))
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func splitUsers(v string) []apiv1.SystemUser {
	users := make([]apiv1.SystemUser, 0)
	for name := range strings.SplitSeq(v, ",") {
		if name != "" {
			users = append(users, apiv1.SystemUser(name))
		}
	}
	return users
}

func (r *PerconaServerMySQLReconciler) backfillInternalSecret(
	ctx context.Context,
	internalSecret *corev1.Secret,
	secret *corev1.Secret,
) error {
	log := logf.FromContext(ctx).WithName("reconcileUsers")

	added := make([]string, 0, len(secret.Data))
	for user, pass := range secret.Data {
		if _, ok := internalSecret.Data[user]; ok {
			continue
		}

		if internalSecret.Data == nil {
			internalSecret.Data = make(map[string][]byte, len(secret.Data))
		}
		internalSecret.Data[user] = bytes.Clone(pass)
		added = append(added, user)
	}

	if len(added) == 0 {
		return nil
	}

	if err := r.Update(ctx, internalSecret); err != nil {
		return errors.Wrapf(err, "update Secret/%s", internalSecret.Name)
	}

	sort.Strings(added)
	log.Info("Added missing users to the internal secret", "users", added)

	return nil
}

const (
	mySQLPasswordMaxLength = 256

	// > The password used for a replication user account in a CHANGE REPLICATION SOURCE TO statement is limited to 32 characters in length
	// Source: https://dev.mysql.com/doc/refman/8.0/en/change-replication-source-to.html#crs-opt-source_password
	mySQLReplicationSourcePasswordMaxLength = 32
)

func validateUserSecret(cr *apiv1.PerconaServerMySQL, secret *corev1.Secret) error {
	if secret == nil || len(secret.Data) == 0 {
		return errors.New("user secret is empty")
	}

	systemUsers := allSystemUsers(cr)
	var errs []error

	for user := range systemUsers {
		if _, ok := secret.Data[string(user)]; ok {
			continue
		}
		errs = append(errs, fmt.Errorf("missing password for %s user", user))
		continue
	}

	for user, pass := range secret.Data {
		if _, ok := systemUsers[apiv1.SystemUser(user)]; !ok && user != string(apiv1.UserPMMServerToken) {
			errs = append(errs, fmt.Errorf("unknown user %s is specified in the secret", string(user)))
			continue
		}
		if user == string(apiv1.UserPMMServerToken) {
			continue
		}
		if len(pass) == 0 {
			errs = append(errs, fmt.Errorf("password is empty for %s user", string(user)))
			continue
		}
		if bytes.IndexByte(pass, 0) >= 0 {
			errs = append(errs, fmt.Errorf("password for %s user must not contain NUL bytes", user))
			continue
		}
		maxLen := mySQLPasswordMaxLength
		if user == string(apiv1.UserReplication) && (cr.Spec.MySQL.IsAsync() || cr.AppliedIsAsync()) {
			maxLen = mySQLReplicationSourcePasswordMaxLength
		}

		// MySQL counts bytes, not characters
		if len(pass) > maxLen {
			errs = append(errs, fmt.Errorf("password for %s user must not exceed %d bytes", user, maxLen))
			continue
		}
	}

	return stderrors.Join(errs...)
}

func (r *PerconaServerMySQLReconciler) discardOldPasswordsAfterNewPropagated(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	secrets *corev1.Secret,
	updatedUsers []mysql.User,
	operatorPass string,
) error {
	log := logf.FromContext(ctx)

	if err := r.passwordsPropagated(ctx, cr, secrets, updatedUsers); err != nil {
		if errors.Is(err, ErrPassNotPropagated) {
			log.Info("Waiting for passwords to be propagated", "reason", err.Error())
			return nil
		}
		return errors.Wrap(err, "check if passwords are propagated")
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
	primPod, err := mysql.GetPod(ctx, r.Client, cr, idx)
	if err != nil {
		return err
	}

	um := db.NewUserManager(primPod, r.ClientCmd, apiv1.UserOperator, operatorPass, primaryHost)

	if err := um.DiscardOldPasswords(ctx, updatedUsers); err != nil {
		return errors.Wrap(err, "discard old passwords")
	}

	log.Info("Discarded old user passwords")

	k8s.AddAnnotation(secrets, naming.AnnotationPasswordsUpdated.String(), "true")
	delete(secrets.Annotations, naming.AnnotationPasswordsUpdatedUsers.String())
	err = r.Client.Update(ctx, secrets)
	if err != nil {
		return errors.Wrap(err, "update internal sys users secret annotation")
	}
	return nil
}

func (r *PerconaServerMySQLReconciler) passwordsPropagated(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	secrets *corev1.Secret,
	updatedUsers []mysql.User,
) error {
	log := logf.FromContext(ctx)

	type component struct {
		name      string
		labels    map[string]string
		credsPath string
	}
	components := []component{
		{
			name:      mysql.AppName,
			labels:    mysql.MatchLabels(cr),
			credsPath: naming.CredsMountPath,
		},
	}

	if cr.OrchestratorEnabled() {
		components = append(components, component{
			name:      orchestrator.AppName,
			labels:    orchestrator.MatchLabels(cr),
			credsPath: orchestrator.CredsMountPath,
		})
	}

	if cr.HAProxyEnabled() {
		components = append(components, component{
			name:      haproxy.AppName,
			labels:    haproxy.MatchLabels(cr),
			credsPath: haproxy.CredsMountPath,
		})
	}

	if cr.RouterEnabled() {
		components = append(components, component{
			name:      router.AppName,
			labels:    router.MatchLabels(cr),
			credsPath: router.CredsMountPath,
		})
	}

	users := make([]string, 0, len(updatedUsers))
	for _, u := range updatedUsers {
		users = append(users, string(u.Username))
	}
	slices.Sort(users)

	eg := new(errgroup.Group)

	for _, component := range components {
		comp := component

		log.Info("Checking if password is propagated for component", "component", comp.name)

		eg.Go(func() error {
			// Pods are listed by labels: Router pods belong to a Deployment and
			// Orchestrator pods are named after "orc", not after the component.
			pods, err := k8s.PodsByLabels(ctx, r.Client, comp.labels, cr.Namespace)
			if err != nil {
				return errors.Wrapf(err, "list %s pods", comp.name)
			}

			for i := range pods {
				pod := &pods[i]

				// A pod that is not scheduled has no volumes yet and will mount the current secret.
				// A pod that is being deleted or has finished does not run with its mounted credentials.
				if pod.Spec.NodeName == "" || pod.DeletionTimestamp != nil ||
					pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
					continue
				}

				// A container that is not running, e.g. in CrashLoopBackOff, holds no password and reads the updated secret when it starts.
				if !containerRunning(pod, comp.name) {
					continue
				}

				// TODO: Improve this by sending single cmd request insted for each user separately
				for _, user := range users {
					cmd := []string{"cat", fmt.Sprintf("%s/%s", comp.credsPath, user)}
					var errb, outb bytes.Buffer
					err := r.ClientCmd.Exec(ctx, pod, comp.name, cmd, nil, &outb, &errb, false)
					if err != nil {
						// The pod holds no password for this user, e.g. Orchestrator mounts only its own.
						if strings.Contains(errb.String(), "No such file or directory") {
							continue
						}
						return errors.Wrapf(ErrPassNotPropagated, "read %s password in pod %s: %v: %s", user, pod.Name, err, strings.TrimSpace(errb.String()))
					}

					if outb.String() != string(secrets.Data[user]) {
						return errors.Wrapf(ErrPassNotPropagated, "%s password in pod %s", user, pod.Name)
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

func containerRunning(pod *corev1.Pod, name string) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == name {
			return status.State.Running != nil
		}
	}

	return false
}

func getMySQLURI(user apiv1.SystemUser, password, host string) string {
	return fmt.Sprintf("%s:%s@%s", user, url.QueryEscape(password), host)
}
