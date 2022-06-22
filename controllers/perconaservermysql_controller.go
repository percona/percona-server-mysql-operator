/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/users"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

// PerconaServerMySQLReconciler reconciles a PerconaServerMySQL object
type PerconaServerMySQLReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ServerVersion *platform.ServerVersion
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqls;perconaservermysqls/status;perconaservermysqls/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods;configmaps;services;secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerMySQLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.PerconaServerMySQL{}).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PerconaServerMySQL object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *PerconaServerMySQLReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithName("PerconaServerMySQL")

	rr := ctrl.Result{RequeueAfter: 5 * time.Second}

	cr, err := r.getCRWithDefaults(ctx, req.NamespacedName)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return rr, errors.Wrap(err, "get CR")
	}

	defer func() {
		if err := r.reconcileCRStatus(ctx, cr); err != nil {
			l.Error(err, "failed to update status")
		}
	}()

	if err := r.doReconcile(ctx, cr); err != nil {
		return rr, errors.Wrap(err, "reconcile")
	}

	return rr, nil
}

func (r *PerconaServerMySQLReconciler) doReconcile(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	if err := r.ensureUserSecrets(ctx, cr); err != nil {
		return errors.Wrap(err, "users secret")
	}
	if err := r.reconcileUsers(ctx, cr); err != nil {
		return errors.Wrap(err, "users")
	}
	if err := r.ensureTLSSecret(ctx, cr); err != nil {
		return errors.Wrap(err, "TLS secret")
	}
	if err := r.reconcileServices(ctx, cr); err != nil {
		return errors.Wrap(err, "services")
	}
	if err := r.reconcileDatabase(ctx, cr); err != nil {
		return errors.Wrap(err, "database")
	}
	if err := r.reconcileOrchestrator(ctx, cr); err != nil {
		return errors.Wrap(err, "orchestrator")
	}
	if err := r.reconcileReplication(ctx, cr); err != nil {
		return errors.Wrap(err, "replication")
	}
	if err := r.cleanupOutdated(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup outdated")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) getCRWithDefaults(
	ctx context.Context,
	nn types.NamespacedName,
) (*apiv1alpha1.PerconaServerMySQL, error) {
	cr := &apiv1alpha1.PerconaServerMySQL{}
	if err := r.Client.Get(ctx, nn, cr); err != nil {
		return nil, errors.Wrapf(err, "get %v", nn.String())
	}
	if err := cr.CheckNSetDefaults(r.ServerVersion); err != nil {
		return nil, errors.Wrapf(err, "check and set defaults for %v", nn.String())
	}

	return cr, nil
}

func (r *PerconaServerMySQLReconciler) ensureUserSecrets(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.SecretsName,
	}

	exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
	if err != nil {
		return errors.Wrap(err, "check if secret exists")
	} else if exists {
		return nil
	}

	secret, err := secret.GeneratePasswordsSecret(cr.Spec.SecretsName, cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "generate passwords")
	}

	if err := k8s.EnsureObject(ctx, r.Client, cr, secret, r.Scheme); err != nil {
		return errors.Wrapf(err, "create secret %s", cr.Spec.SecretsName)
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileUsers(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	l := log.FromContext(ctx).WithName("reconcileUsers")

	secret := &corev1.Secret{}
	nn := types.NamespacedName{Name: cr.Spec.SecretsName, Namespace: cr.Namespace}
	if err := r.Client.Get(ctx, nn, secret); err != nil {
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

		if err := r.Client.Create(ctx, internalSecret); err != nil {
			return errors.Wrapf(err, "create Secret/%s", internalSecret.Name)
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
		l.V(1).Info("Secret data is up to date")
		return nil
	}

	if cr.Status.MySQL.State != apiv1alpha1.StateReady {
		l.Info("MySQL is not ready")
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
			l.V(1).Info("User password is up to date", "user", user)
			continue
		}

		mysqlUser := mysql.User{
			Username: apiv1alpha1.SystemUser(user),
			Password: string(pass),
			Hosts:    []string{"%"},
		}

		switch mysqlUser.Username {
		case apiv1alpha1.UserMonitor:
			restartMySQL = cr.PMMEnabled()
		case apiv1alpha1.UserPMMServer:
			restartMySQL = cr.PMMEnabled()
			continue // PMM server user credentials are not stored in db
		case apiv1alpha1.UserReplication:
			restartReplication = true
		case apiv1alpha1.UserOrchestrator:
			restartOrchestrator = true
		case apiv1alpha1.UserRoot:
			mysqlUser.Hosts = append(mysqlUser.Hosts, "localhost")
		case apiv1alpha1.UserClusterCheck, apiv1alpha1.UserXtraBackup:
			mysqlUser.Hosts = []string{"localhost"}
		}

		l.V(1).Info("User password changed", "user", user)

		updatedUsers = append(updatedUsers, mysqlUser)
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	orcHost := orchestrator.APIHost(cr)
	primary, err := orchestrator.ClusterPrimary(ctx, orcHost, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}
	l.V(1).Info("Got cluster primary", "primary", primary)
	primaryHost := getPrimaryHostname(primary, cr)

	um, err := users.NewManager(apiv1alpha1.UserOperator, operatorPass, primaryHost, mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrap(err, "init user manager")
	}
	defer um.Close()

	if restartReplication {
		db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, primaryHost, mysql.DefaultAdminPort)
		if err != nil {
			return errors.Wrap(err, "open connection to primary")
		}

		// We're disabling semi-sync replication on primary to avoid LockedSemiSyncMaster.
		if err := db.SetSemiSyncSource(false); err != nil {
			return errors.Wrap(err, "set semi_sync wait count")
		}

		g, gCtx := errgroup.WithContext(context.Background())
		for _, replica := range primary.Replicas {
			hostname := replica.Hostname
			port := replica.Port
			g.Go(func() error {
				repDb, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, hostname, port)
				if err != nil {
					return errors.Wrapf(err, "connect to replica %s", hostname)
				}

				if err := orchestrator.StopReplication(gCtx, orcHost, hostname, port); err != nil {
					return errors.Wrapf(err, "stop replica %s", hostname)
				}

				status, _, err := repDb.ReplicationStatus()
				if err != nil {
					return errors.Wrapf(err, "get replication status of %s", hostname)
				}

				for status == replicator.ReplicationStatusActive {
					time.Sleep(250 * time.Millisecond)
					status, _, err = repDb.ReplicationStatus()
					if err != nil {
						return errors.Wrapf(err, "get replication status of %s", hostname)
					}
				}

				l.V(1).Info("Stopped replication on replica", "hostname", hostname, "port", port)

				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return errors.Wrap(err, "stop replication on replicas")
		}
	}

	if err := um.UpdateUserPasswords(updatedUsers); err != nil {
		return errors.Wrapf(err, "update passwords")
	}

	if restartReplication {
		var replicationPass string
		for _, user := range updatedUsers {
			if user.Username == apiv1alpha1.UserReplication {
				replicationPass = user.Password
				break
			}
		}
		g, gCtx := errgroup.WithContext(context.Background())
		for _, replica := range primary.Replicas {
			hostname := replica.Hostname
			port := replica.Port
			g.Go(func() error {
				db, err := replicator.NewReplicator(
					apiv1alpha1.UserOperator,
					operatorPass,
					hostname,
					mysql.DefaultAdminPort,
				)
				if err != nil {
					return errors.Wrapf(err, "get db connection to %s", hostname)
				}
				defer db.Close()

				pHost := getPrimaryHostname(primary, cr)
				l.V(1).Info("Change replication source", "primary", pHost, "replica", hostname)
				if err := db.ChangeReplicationSource(pHost, replicationPass, primary.Key.Port); err != nil {
					return errors.Wrapf(err, "change replication source on %s", hostname)
				}

				if err := orchestrator.StartReplication(gCtx, orcHost, hostname, port); err != nil {
					return errors.Wrapf(err, "start replication on %s", hostname)
				}

				l.V(1).Info("Started replication on replica", "hostname", hostname, "port", port)

				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return errors.Wrap(err, "start replication on replicas")
		}
	}

	if restartOrchestrator {
		l.Info("Orchestrator password updated. Restarting orchestrator.")

		sts := &appsv1.StatefulSet{}
		if err := r.Client.Get(ctx, orchestrator.NamespacedName(cr), sts); err != nil {
			return errors.Wrap(err, "get Orchestrator statefulset")
		}
		if err := k8s.RolloutRestart(ctx, r.Client, sts, apiv1alpha1.AnnotationSecretHash, hash); err != nil {
			return errors.Wrap(err, "restart orchestrator")
		}
	}

	if restartMySQL {
		l.Info("Monitor user password updated. Restarting MySQL.")

		sts := &appsv1.StatefulSet{}
		if err := r.Client.Get(ctx, mysql.NamespacedName(cr), sts); err != nil {
			return errors.Wrap(err, "get MySQL statefulset")
		}
		if err := k8s.RolloutRestart(ctx, r.Client, sts, apiv1alpha1.AnnotationSecretHash, hash); err != nil {
			return errors.Wrap(err, "restart MySQL")
		}
	}

	if cr.Status.State != apiv1alpha1.StateReady {
		l.Info("Waiting cluster to be ready")
		return nil
	}

	if err := um.DiscardOldPasswords(updatedUsers); err != nil {
		return errors.Wrap(err, "discard old passwords")
	}

	l.Info("Discarded old user passwords")

	internalSecret.Data = secret.Data
	if err := r.Client.Update(ctx, internalSecret); err != nil {
		return errors.Wrapf(err, "update Secret/%s", internalSecret.Name)
	}

	l.Info("Updated internal secret", "secretName", cr.InternalSecretName())

	return nil
}

func (r *PerconaServerMySQLReconciler) ensureTLSSecret(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	nn := types.NamespacedName{
		Name:      cr.Spec.SSLSecretName,
		Namespace: cr.Namespace,
	}

	if ok, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{}); err != nil {
		return errors.Wrap(err, "check existence")
	} else if ok {
		return nil
	}

	secret, err := secret.GenerateCertsSecret(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "create SSL manually")
	}

	if err := k8s.EnsureObject(ctx, r.Client, cr, secret, r.Scheme); err != nil {
		return errors.Wrap(err, "create secret")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileDatabase(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	configHash, err := r.reconcileMySQLConfiguration(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "reconcile MySQL config")
	}

	autoConfigHash, err := r.reconcileMySQLAutoConfig(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "reconcile MySQL auto-config")
	}

	initImage, err := k8s.InitImage(ctx, r.Client)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.StatefulSet(cr, initImage, configHash, autoConfigHash), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile sts")
	}

	return nil
}

type Exposer interface {
	Exposed() bool
	Name(index string) string
	Size() int32
	Labels() map[string]string
	Service(name string) *corev1.Service
}

func (r *PerconaServerMySQLReconciler) reconcileServicePerPod(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, exposer Exposer) error {
	_ = log.FromContext(ctx).WithName("reconcileServicePerPod")

	if !exposer.Exposed() {
		return nil
	}

	size := int(exposer.Size())
	svcNames := make(map[string]struct{}, size)
	for i := 0; i < size; i++ {
		svcName := exposer.Name(strconv.Itoa(i))
		svc := exposer.Service(svcName)
		svcNames[svc.Name] = struct{}{}

		if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, svc, r.Scheme); err != nil {
			return errors.Wrapf(err, "reconcile svc for pod %s", svc.Name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileMySQLServices(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	_ = log.FromContext(ctx).WithName("reconcileMySQLServices")

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.PrimaryService(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile primary svc")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.UnreadyService(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile unready svc")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.HeadlessService(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile headless svc")
	}

	exposer := mysql.Exposer(*cr)
	if err := r.reconcileServicePerPod(ctx, cr, &exposer); err != nil {
		return errors.Wrap(err, "reconcile service per pod")
	}

	return nil
}
func (r *PerconaServerMySQLReconciler) reconcileMySQLAutoConfig(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (string, error) {
	l := log.FromContext(ctx).WithName("reconcileMySQLAutoConfig")
	var memory *resource.Quantity
	var err error

	if res := cr.Spec.MySQL.Resources; res.Size() > 0 {
		if _, ok := res.Requests[corev1.ResourceMemory]; ok {
			memory = res.Requests.Memory()
		}
		if _, ok := res.Limits[corev1.ResourceMemory]; ok {
			memory = res.Limits.Memory()
		}
	}

	nn := types.NamespacedName{
		Name:      mysql.AutoConfigMapName(cr),
		Namespace: cr.Namespace,
	}
	currentConfigMap := new(corev1.ConfigMap)
	if err = r.Client.Get(ctx, nn, currentConfigMap); client.IgnoreNotFound(err) != nil {
		return "", errors.Wrapf(err, "get ConfigMap/%s", nn.Name)
	}
	if memory == nil {
		exists := true
		if k8serrors.IsNotFound(err) {
			exists = false
		}

		if !exists || !metav1.IsControlledBy(currentConfigMap, cr) {
			return "", nil
		}

		if err := r.Client.Delete(ctx, currentConfigMap); err != nil {
			return "", errors.Wrapf(err, "delete ConfigMaps/%s", currentConfigMap.Name)
		}

		l.Info("ConfigMap deleted", "name", currentConfigMap.Name)

		return "", nil
	}
	autotuneParams, err := getAutoTuneParams(cr, memory)
	if err != nil {
		return "", err
	}
	configMap := k8s.ConfigMap(mysql.AutoConfigMapName(cr), cr.Namespace, mysql.CustomConfigKey, autotuneParams)
	if !reflect.DeepEqual(currentConfigMap.Data, configMap.Data) {
		if err := k8s.EnsureObject(ctx, r.Client, cr, configMap, r.Scheme); err != nil {
			return "", errors.Wrapf(err, "ensure ConfigMap/%s", configMap.Name)
		}
		l.Info("ConfigMap updated", "name", configMap.Name, "data", configMap.Data)
	}
	d := struct{ Data map[string]string }{Data: configMap.Data}
	data, err := json.Marshal(d)
	if err != nil {
		return "", errors.Wrap(err, "marshal configmap data to json")
	}

	return fmt.Sprintf("%x", md5.Sum(data)), nil
}

func (r *PerconaServerMySQLReconciler) reconcileMySQLConfiguration(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) (string, error) {
	l := log.FromContext(ctx).WithName("reconcileMySQLConfiguration")

	cmName := mysql.ConfigMapName(cr)
	nn := types.NamespacedName{Name: cmName, Namespace: cr.Namespace}

	currCm := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, nn, currCm); err != nil && !k8serrors.IsNotFound(err) {
		return "", errors.Wrapf(err, "get ConfigMap/%s", cmName)
	}

	// Cleanup if user removed the configuration from CR
	if cr.Spec.MySQL.Configuration == "" {
		exists, err := k8s.ObjectExists(ctx, r.Client, nn, currCm)
		if err != nil {
			return "", errors.Wrapf(err, "check if ConfigMap/%s exists", cmName)
		}

		if !exists || !metav1.IsControlledBy(currCm, cr) {
			return "", nil
		}

		if err := r.Client.Delete(ctx, currCm); err != nil {
			return "", errors.Wrapf(err, "delete ConfigMaps/%s", cmName)
		}

		l.Info("ConfigMap deleted", "name", cmName)

		return "", nil
	}

	cm := k8s.ConfigMap(cmName, cr.Namespace, mysql.CustomConfigKey, cr.Spec.MySQL.Configuration)
	if !reflect.DeepEqual(currCm.Data, cm.Data) {
		if err := k8s.EnsureObject(ctx, r.Client, cr, cm, r.Scheme); err != nil {
			return "", errors.Wrapf(err, "ensure ConfigMap/%s", cmName)
		}

		l.Info("ConfigMap updated", "name", cmName, "data", cm.Data)
	}

	d := struct{ Data map[string]string }{Data: cm.Data}
	data, err := json.Marshal(d)
	if err != nil {
		return "", errors.Wrap(err, "marshal configmap data to json")
	}

	return fmt.Sprintf("%x", md5.Sum(data)), nil
}

func getAutoTuneParams(cr *apiv1alpha1.PerconaServerMySQL, q *resource.Quantity) (string, error) {
	autotuneParams := ""

	poolSize := q.Value() * int64(75) / int64(100)
	if q.Value()-poolSize < int64(1000000000) {
		poolSize = q.Value() * int64(50) / int64(100)
	}
	instances := int64(1)                 // default value
	chunkSize := int64(1024 * 1024 * 128) // default value

	// Adjust innodb_buffer_pool_chunk_size
	// If innodb_buffer_pool_size is bigger than 1Gi, innodb_buffer_pool_instances is set to 8.
	// By default, innodb_buffer_pool_chunk_size is 128Mi and innodb_buffer_pool_size needs to be
	// multiple of innodb_buffer_pool_chunk_size * innodb_buffer_pool_instances.
	// More info: https://dev.mysql.com/doc/refman/8.0/en/innodb-buffer-pool-resize.html
	if poolSize > int64(1073741824) {
		instances = 8
		chunkSize = poolSize / instances
		// innodb_buffer_pool_chunk_size can be increased or decreased in units of 1Mi (1048576 bytes).
		// That's why we should strip redundant bytes
		chunkSize -= chunkSize % (1048576)
	}

	// Buffer pool size must always
	// be equal to or a multiple of innodb_buffer_pool_chunk_size * innodb_buffer_pool_instances.
	// If not, this value will be adjusted
	if poolSize%(instances*chunkSize) != 0 {
		poolSize += (instances * chunkSize) - poolSize%(instances*chunkSize)
	}
	conf := cr.Spec.MySQL.Configuration
	if !strings.Contains(conf, "innodb_buffer_pool_size") {
		poolSizeVal := strconv.FormatInt(poolSize, 10)
		autotuneParams += "\ninnodb_buffer_pool_size=" + poolSizeVal

		if !strings.Contains(conf, "innodb_buffer_pool_chunk_size") {
			chunkSizeVal := strconv.FormatInt(chunkSize, 10)
			autotuneParams += "\ninnodb_buffer_pool_chunk_size=" + chunkSizeVal
		}
	}

	if !strings.Contains(conf, "max_connections") {
		divider := int64(12582880)
		if q.Value() < divider {
			return "", errors.New("Not enough memory set in requests. Must be >= 12Mi.")
		}
		maxConnSize := q.Value() / divider
		maxConnSizeVal := strconv.FormatInt(maxConnSize, 10)
		autotuneParams += "\nmax_connections=" + maxConnSizeVal
	}

	return autotuneParams, nil
}

func (r *PerconaServerMySQLReconciler) reconcileOrchestrator(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	l := log.FromContext(ctx).WithName("reconcileOrchestrator")

	cm := &corev1.ConfigMap{}
	err := r.Client.Get(ctx, orchestrator.NamespacedName(cr), cm)
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "get config map")
	}

	existingNodes := make([]string, 0)
	if !k8serrors.IsNotFound(err) {
		cfg, ok := cm.Data[orchestrator.ConfigFileName]
		if !ok {
			return errors.Errorf("key %s not found in ConfigMap", orchestrator.ConfigFileName)
		}

		config := make(map[string]interface{}, 0)
		if err := json.Unmarshal([]byte(cfg), &config); err != nil {
			return errors.Wrap(err, "unmarshal ConfigMap data to json")
		}

		nodes, ok := config["RaftNodes"].([]interface{})
		if !ok {
			return errors.New("key RaftNodes not found in ConfigMap")
		}

		for _, v := range nodes {
			existingNodes = append(existingNodes, v.(string))
		}
	}

	cmData, err := orchestrator.ConfigMapData(cr)
	if err != nil {
		return errors.Wrap(err, "get ConfigMap data")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.ConfigMap(cr, cmData), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile ConfigMap")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.StatefulSet(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}

	raftNodes := orchestrator.RaftNodes(cr)
	if len(existingNodes) == 0 || len(existingNodes) == len(raftNodes) {
		return nil
	}

	orcHost := orchestrator.APIHost(cr)
	g, gCtx := errgroup.WithContext(context.Background())

	if len(raftNodes) > len(existingNodes) {
		newPeers := util.Difference(raftNodes, existingNodes)

		for _, peer := range newPeers {
			p := peer
			g.Go(func() error {
				return orchestrator.AddPeer(gCtx, orcHost, p)
			})
		}

		l.Error(g.Wait(), "Orchestrator add peers", "peers", newPeers)
	} else {
		oldPeers := util.Difference(existingNodes, raftNodes)

		for _, peer := range oldPeers {
			p := peer
			g.Go(func() error {
				return orchestrator.RemovePeer(gCtx, orcHost, p)
			})
		}

		l.Error(g.Wait(), "Orchestrator remove peers", "peers", oldPeers)
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileOrchestratorServices(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.Service(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile Service")
	}

	exposer := orchestrator.Exposer(*cr)
	if err := r.reconcileServicePerPod(ctx, cr, &exposer); err != nil {
		return errors.Wrap(err, "reconcile service per pod")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileServices(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if err := r.reconcileMySQLServices(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile MySQL services")
	}

	if err := r.reconcileOrchestratorServices(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile Orchestrator services")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileReplication(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	l := log.FromContext(ctx).WithName("reconcileReplication")

	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(orchestrator.StatefulSet(cr)), sts); err != nil {
		return client.IgnoreNotFound(err)
	}

	if sts.Status.ReadyReplicas == 0 {
		l.Info("orchestrator is not ready. skip", "ready", sts.Status.ReadyReplicas)
		return nil
	}

	if err := reconcileReplicationPrimaryPod(ctx, r.Client, cr); err != nil {
		return errors.Wrap(err, "reconcile primary pod")
	}
	if err := reconcileReplicationSemiSync(ctx, r.Client, cr); err != nil {
		return errors.Wrap(err, "reconcile semi-sync")
	}

	return nil
}

func reconcileReplicationPrimaryPod(
	ctx context.Context,
	cl client.Client,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	l := log.FromContext(ctx).WithName("reconcileReplicationPrimaryPod")

	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get MySQL pod list")
	}
	l.V(1).Info(fmt.Sprintf("got %v pods", len(pods)))

	host := orchestrator.APIHost(cr)
	primary, err := orchestrator.ClusterPrimary(ctx, host, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}
	primaryAlias := primary.Alias
	l.V(1).Info(fmt.Sprintf("got cluster primary alias: %v", primaryAlias), "data", primary)

	for i := range pods {
		pod := pods[i].DeepCopy()
		if pod.GetLabels()[apiv1alpha1.MySQLPrimaryLabel] == "true" {
			if pod.Name == primaryAlias {
				l.Info(fmt.Sprintf("primary %v is not changed. skip", primaryAlias))
				return nil
			}

			k8s.RemoveLabel(pod, apiv1alpha1.MySQLPrimaryLabel)
			if err := cl.Patch(ctx, pod, client.StrategicMergeFrom(&pods[i])); err != nil {
				return errors.Wrapf(err, "remove label from old primary pod: %v/%v",
					pod.GetNamespace(), pod.GetName())
			}

			l.Info(fmt.Sprintf("removed label from old primary pod: %v/%v",
				pod.GetNamespace(), pod.GetName()))
			break
		}
	}

	for i := range pods {
		pod := pods[i].DeepCopy()
		if pod.Name == primaryAlias {
			k8s.AddLabel(pod, apiv1alpha1.MySQLPrimaryLabel, "true")
			if err := cl.Patch(ctx, pod, client.StrategicMergeFrom(&pods[i])); err != nil {
				return errors.Wrapf(err, "add label to new primary pod %v/%v",
					pod.GetNamespace(), pod.GetName())
			}

			l.Info(fmt.Sprintf("added label to new primary pod: %v/%v",
				pod.GetNamespace(), pod.GetName()))

			break
		}
	}

	return nil
}

func reconcileReplicationSemiSync(
	ctx context.Context,
	cl client.Reader,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	l := log.FromContext(ctx).WithName("reconcileReplicationSemiSync")

	host := orchestrator.APIHost(cr)
	primary, err := orchestrator.ClusterPrimary(ctx, host, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}

	primaryHost := getPrimaryHostname(primary, cr)

	l.V(1).Info(fmt.Sprintf("use primary host: %v", primaryHost), "clusterPrimary", primary)

	operatorPass, err := k8s.UserPassword(ctx, cl, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator,
		operatorPass,
		primaryHost,
		mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrapf(err, "connect to %v", primaryHost)
	}
	defer db.Close()

	if err := db.SetSemiSyncSource(cr.MySQLSpec().SizeSemiSync.IntValue() > 0); err != nil {
		return errors.Wrapf(err, "set semi-sync source on %#v", primaryHost)
	}
	l.Info(fmt.Sprintf("set semi-sync source on %v", primaryHost))

	if err := db.SetSemiSyncSize(cr.MySQLSpec().SizeSemiSync.IntValue()); err != nil {
		return errors.Wrapf(err, "set semi-sync size on %v", primaryHost)
	}
	l.Info(fmt.Sprintf("set semi-sync size on %v", primaryHost))

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOutdatedServices(ctx context.Context, exposer Exposer) error {
	l := log.FromContext(ctx).WithName("cleanupOutdatedServices")

	size := int(exposer.Size())
	svcNames := make(map[string]struct{}, size)
	for i := 0; i < size; i++ {
		svcName := exposer.Name(strconv.Itoa(i))
		svc := exposer.Service(svcName)
		svcNames[svc.Name] = struct{}{}

		if !exposer.Exposed() {
			if err := r.Client.Delete(ctx, svc); err != nil && !k8serrors.IsNotFound(err) {
				return errors.Wrapf(err, "delete svc for pod %s", svc.Name)
			}
		}
	}

	svcLabels := exposer.Labels()
	svcLabels[apiv1alpha1.ExposedLabel] = "true"
	services, err := k8s.ServicesByLabels(ctx, r.Client, svcLabels)
	if err != nil {
		return errors.Wrap(err, "get exposed services")
	}

	for _, svc := range services {
		if _, ok := svcNames[svc.Name]; ok {
			continue
		}

		l.Info("Deleting outdated service", "service", svc.Name)
		if err := r.Client.Delete(ctx, &svc); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "delete Service/%s", svc.Name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOutdated(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	mysqlExposer := mysql.Exposer(*cr)
	if err := r.cleanupOutdatedServices(ctx, &mysqlExposer); err != nil {
		return errors.Wrap(err, "cleanup MySQL services")
	}

	orcExposer := orchestrator.Exposer(*cr)
	if err := r.cleanupOutdatedServices(ctx, &orcExposer); err != nil {
		return errors.Wrap(err, "cleanup Orchestrator services")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileCRStatus(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	l := log.FromContext(ctx).WithName("reconcileCRStatus")

	mysqlStatus, err := appStatus(ctx, r.Client, cr.MySQLSpec().Size, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get MySQL status")
	}
	cr.Status.MySQL = mysqlStatus

	orcStatus, err := appStatus(ctx, r.Client, cr.OrchestratorSpec().Size, orchestrator.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get Orchestrator status")
	}
	cr.Status.Orchestrator = orcStatus

	if cr.Status.MySQL.State == cr.Status.Orchestrator.State {
		cr.Status.State = cr.Status.MySQL.State
	} else {
		cr.Status.State = apiv1alpha1.StateInitializing
	}

	cr.Status.Host, err = appHost(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "get app host")
	}

	l.V(1).Info("Writing CR status", "state", cr.Status.State, "orchestrator", cr.Status.Orchestrator, "mysql", cr.Status.MySQL, "host", cr.Status.Host)

	nn := types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}
	return writeStatus(ctx, r.Client, nn, cr.Status)
}

func appHost(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL) (string, error) {
	serviceName := mysql.PrimaryServiceName(cr)
	if cr.MySQLSpec().PrimaryServiceType != corev1.ServiceTypeLoadBalancer {
		return serviceName + "." + cr.GetNamespace(), nil
	}

	svc := &corev1.Service{}
	err := cl.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: serviceName}, svc)
	if err != nil {
		return "", errors.Wrapf(err, "get %s service", serviceName)
	}

	var host string
	for _, i := range svc.Status.LoadBalancer.Ingress {
		host = i.IP
		if len(i.Hostname) > 0 {
			host = i.Hostname
		}
	}

	return host, nil
}

func appStatus(
	ctx context.Context,
	cl client.Reader,
	size int32,
	labels map[string]string,
) (apiv1alpha1.StatefulAppStatus, error) {
	status := apiv1alpha1.StatefulAppStatus{
		Size:  size,
		State: apiv1alpha1.StateInitializing,
	}

	pods, err := k8s.PodsByLabels(ctx, cl, labels)
	if err != nil {
		return status, errors.Wrap(err, "get pod list")
	}

	for i := range pods {
		if k8s.IsPodReady(pods[i]) {
			status.Ready++
		}
	}

	if status.Ready == status.Size {
		status.State = apiv1alpha1.StateReady
	}

	return status, nil
}

func writeStatus(
	ctx context.Context,
	cl client.Client,
	nn types.NamespacedName,
	status apiv1alpha1.PerconaServerMySQLStatus,
) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		cr := &apiv1alpha1.PerconaServerMySQL{}
		if err := cl.Get(ctx, nn, cr); err != nil {
			return errors.Wrapf(err, "get %v", nn.String())
		}

		cr.Status = status
		if err := cl.Status().Update(ctx, cr); err != nil {
			return errors.Wrapf(err, "update %v", nn.String())
		}

		return nil
	})
}

func getPrimaryHostname(primary *orchestrator.Instance, cr *apiv1alpha1.PerconaServerMySQL) string {
	if primary.Key.Hostname != "" {
		return primary.Key.Hostname
	}

	return fmt.Sprintf("%s.%s.%s", primary.Alias, mysql.ServiceName(cr), cr.Namespace)
}
