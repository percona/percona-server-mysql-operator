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

package ps

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/controller/psrestore"
	database "github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

// PerconaServerMySQLReconciler reconciles a PerconaServerMySQL object
type PerconaServerMySQLReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ServerVersion *platform.ServerVersion
	Recorder      record.EventRecorder
	ClientCmd     clientcmd.Client

	Crons cronRegistry
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqls;perconaservermysqls/status;perconaservermysqls/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps;services;secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods;pods/exec,verbs=get;list;watch;create;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=certmanager.k8s.io;cert-manager.io,resources=issuers;certificates,verbs=get;list;watch;create;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;patch
//+kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;patch
//+kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;patch

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerMySQLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.PerconaServerMySQL{}).
		Named("ps-controller").
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
	log := logf.FromContext(ctx)

	rr := ctrl.Result{RequeueAfter: 5 * time.Second}

	var cr *apiv1alpha1.PerconaServerMySQL
	var err error

	defer func() {
		if err := r.reconcileCRStatus(ctx, cr, err); err != nil {
			log.Error(err, "failed to update status")
		}
	}()

	cr, err = k8s.GetCRWithDefaults(ctx, r.Client, req.NamespacedName, r.ServerVersion)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return rr, errors.Wrap(err, "get CR")
	}

	if cr.ObjectMeta.DeletionTimestamp != nil {
		return rr, r.applyFinalizers(ctx, cr)
	}

	if err = r.doReconcile(ctx, cr); err != nil {
		return rr, errors.Wrap(err, "reconcile")
	}

	return rr, nil
}

func (r *PerconaServerMySQLReconciler) applyFinalizers(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("Finalizer")
	log.Info("Applying finalizers", "CR", cr)

	var err error

	// Sorting finalizers to make sure that delete-mysql-pods-in-order runs before
	// delete-mysql-pvc since the latter removes secrets needed for the former.
	slices.Sort(cr.GetFinalizers())

	for _, finalizer := range cr.GetFinalizers() {
		switch finalizer {
		case naming.FinalizerDeletePodsInOrder:
			err = r.deleteMySQLPods(ctx, cr)
		case naming.FinalizerDeleteSSL:
			err = r.deleteCerts(ctx, cr)
		case naming.FinalizerDeleteMySQLPvc:
			err = r.deleteMySQLPvc(ctx, cr)
		}
		if err != nil {
			return err
		}
	}

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := new(apiv1alpha1.PerconaServerMySQL)
		err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, c)
		if err != nil {
			return errors.Wrap(err, "get cr")
		}

		c.SetFinalizers([]string{})
		return r.Client.Update(ctx, c)
	})
}

func (r *PerconaServerMySQLReconciler) deleteMySQLPods(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("deleteMySQLPods")

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}
	log.Info("Deleting MySQL pods", "pods", len(pods))

	// the last pod left - we can leave it for the stateful set
	if len(pods) <= 1 {
		time.Sleep(time.Second * 3)
		log.Info("Cluster deleted")
		return nil
	}

	var firstPod corev1.Pod
	for _, p := range pods {
		if p.GetName() == mysql.PodName(cr, 0) {
			firstPod = p
			break
		}
	}

	if cr.Spec.MySQL.IsAsync() {
		orcPod, err := getReadyOrcPod(ctx, r.Client, cr)
		if err != nil {
			return nil
		}

		log.Info("Ensuring oldest mysql node is the primary")
		err = orchestrator.EnsureNodeIsPrimary(ctx, r.ClientCmd, orcPod, cr.ClusterHint(), firstPod.GetName(), mysql.DefaultPort)
		if err != nil {
			return errors.Wrap(err, "ensure node is primary")
		}
	} else {
		operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
		if err != nil {
			return errors.Wrap(err, "get operator password")
		}

		firstPodUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, mysql.PodFQDN(cr, &firstPod))

		um := database.NewReplicationManager(&firstPod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, mysql.PodFQDN(cr, &firstPod))

		mysh, err := mysqlsh.NewWithExec(r.ClientCmd, &firstPod, firstPodUri)
		if err != nil {
			return err
		}

		clusterStatus, err := mysh.ClusterStatusWithExec(ctx, cr.InnoDBClusterName())
		if err != nil {
			return errors.Wrap(err, "get cluster status")
		}

		log.Info("Got primary", "primary", clusterStatus.DefaultReplicaSet.Primary)

		if !strings.HasPrefix(clusterStatus.DefaultReplicaSet.Primary, mysql.PodFQDN(cr, &firstPod)) {
			log.Info("Primary is not pod-0", "primary", clusterStatus.DefaultReplicaSet.Primary)

			log.Info("Ensuring pod-0 is the primary")
			err := mysh.SetPrimaryInstanceWithExec(ctx, cr.InnoDBClusterName(), mysql.PodFQDN(cr, &firstPod))
			if err != nil {
				return errors.Wrap(err, "set primary instance")
			}

			return psrestore.ErrWaitingTermination
		}

		log.Info("Removing instances from GR")
		for _, pod := range pods {
			if pod.Name == firstPod.Name {
				continue
			}

			podFQDN := fmt.Sprintf("%s.%s.%s", pod.Name, mysql.ServiceName(cr), cr.Namespace)

			state, err := um.GetMemberState(ctx, podFQDN)
			if err != nil {
				return errors.Wrapf(err, "get member state of %s from performance_schema", pod.Name)
			}

			if state == database.MemberStateOffline {
				log.Info("Member is not part of GR or already removed", "member", pod.Name, "memberState", state)
				continue
			}

			podUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, podFQDN)

			log.Info("Removing member from GR", "member", pod.Name, "memberState", state)
			err = mysh.RemoveInstanceWithExec(ctx, cr.InnoDBClusterName(), podUri)
			if err != nil {
				return errors.Wrapf(err, "remove instance %s", pod.Name)
			}
			log.Info("Member removed from GR", "member", pod.Name)
		}
	}

	sts := &appsv1.StatefulSet{}
	if err := r.Client.Get(ctx, mysql.NamespacedName(cr), sts); err != nil {
		return errors.Wrap(err, "get MySQL statefulset")
	}

	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 1 {
		dscaleTo := int32(1)
		sts.Spec.Replicas = &dscaleTo
		err = r.Client.Update(ctx, sts)
		if err != nil {
			return errors.Wrap(err, "downscale StatefulSet")
		}
		log.Info("Statefulset downscaled", "sts", sts)
	}

	return psrestore.ErrWaitingTermination
}

func (r *PerconaServerMySQLReconciler) deleteCerts(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)
	log.Info("Deleting SSL certificates")

	issuers := []string{
		cr.Name + "-ps-ca-issuer",
		cr.Name + "-ps-issuer",
	}
	for _, issuerName := range issuers {
		issuer := &cm.Issuer{}
		err := r.Client.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      issuerName,
		}, issuer)
		if err != nil {
			continue
		}
		err = r.Client.Delete(ctx, issuer, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &issuer.UID}})
		if err != nil {
			return errors.Wrapf(err, "delete issuer %s", issuerName)
		}
	}

	certs := []string{
		cr.Name + "-ssl",
		cr.Name + "-ca-cert",
	}
	for _, certName := range certs {
		cert := &cm.Certificate{}
		err := r.Client.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      certName,
		}, cert)
		if err != nil {
			continue
		}

		err = r.Client.Delete(ctx, cert,
			&client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cert.UID}})
		if err != nil {
			return errors.Wrapf(err, "delete certificate %s", certName)
		}
	}

	secretNames := []string{
		cr.Name + "-ca-cert",
		cr.Spec.SSLSecretName,
	}
	for _, secretName := range secretNames {
		secret := &corev1.Secret{}
		err := r.Client.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      secretName,
		}, secret)
		if err != nil {
			continue
		}

		err = r.Client.Delete(ctx, secret,
			&client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &secret.UID}})
		if err != nil {
			return errors.Wrapf(err, "delete secret %s", secretName)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) deleteMySQLPvc(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	log.Info(fmt.Sprintf("Applying %s finalizer", naming.FinalizerDeleteMySQLPvc))

	exposer := mysql.Exposer(*cr)

	list := corev1.PersistentVolumeClaimList{}

	err := r.Client.List(ctx,
		&list,
		&client.ListOptions{
			Namespace:     cr.Namespace,
			LabelSelector: labels.SelectorFromSet(exposer.Labels()),
		},
	)
	if err != nil {
		return errors.Wrap(err, "get PVC list")
	}

	if list.Size() == 0 {
		return nil
	}

	for _, pvc := range list.Items {
		err := r.Client.Delete(ctx, &pvc, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pvc.UID}})
		if err != nil {
			return errors.Wrapf(err, "delete PVC %s", pvc.Name)
		}
		log.Info("Removed MySQL PVC", "pvc", pvc.Name)
	}

	secretNames := []string{
		cr.Spec.SecretsName,
		"internal-" + cr.Name,
	}
	err = k8s.DeleteSecrets(ctx, r.Client, cr, secretNames)
	if err != nil {
		return errors.Wrap(err, "delete secrets")
	}
	log.Info("Removed secrets", "secrets", secretNames)

	return nil
}

func (r *PerconaServerMySQLReconciler) doReconcile(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	log := logf.FromContext(ctx).WithName("doReconcile")

	if err := r.reconcileFullClusterCrash(ctx, cr); err != nil {
		return errors.Wrap(err, "failed to check full cluster crash")
	}
	if err := r.reconcileVersions(ctx, cr); err != nil {
		log.Error(err, "failed to reconcile versions")
	}
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
	if err := r.reconcileHAProxy(ctx, cr); err != nil {
		return errors.Wrap(err, "HAProxy")
	}
	if err := r.reconcileMySQLRouter(ctx, cr); err != nil {
		return errors.Wrap(err, "MySQL router")
	}
	if err := r.reconcileBinlogServer(ctx, cr); err != nil {
		return errors.Wrap(err, "binlog server")
	}
	if err := r.reconcileScheduledBackup(ctx, cr); err != nil {
		return errors.Wrap(err, "scheduled backup")
	}
	if err := r.cleanupOutdated(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup outdated")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileDatabase(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileDatabase")

	configurable := mysql.Configurable(*cr)
	configHash, err := r.reconcileCustomConfiguration(ctx, cr, &configurable)
	if err != nil {
		return errors.Wrap(err, "reconcile MySQL config")
	}

	tlsHash, err := getTLSHash(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "failed to get tls hash")
	}

	if err = r.reconcileMySQLAutoConfig(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile MySQL auto-config")
	}

	initImage, err := k8s.InitImage(ctx, r.Client, cr, &cr.Spec.MySQL.PodSpec)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	internalSecret := new(corev1.Secret)
	nn := types.NamespacedName{Name: cr.InternalSecretName(), Namespace: cr.Namespace}
	err = r.Client.Get(ctx, nn, internalSecret)
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrapf(err, "get Secret/%s", nn.Name)
	}

	sts := mysql.StatefulSet(cr, initImage, configHash, tlsHash, internalSecret)

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, sts, r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile sts")
	}

	if pmm := cr.Spec.PMM; pmm != nil && pmm.Enabled && !pmm.HasSecret(internalSecret) {
		log.Info(fmt.Sprintf(`Can't enable PMM: either "%s" key doesn't exist in the secrets, or secrets and internal secrets are out of sync`,
			apiv1alpha1.UserPMMServerKey), "secrets", cr.Spec.SecretsName, "internalSecrets", cr.InternalSecretName())
	}

	if cr.Spec.UpdateStrategy == apiv1alpha1.SmartUpdateStatefulSetStrategyType {
		return r.smartUpdate(ctx, sts, cr)
	}

	return nil
}

type Exposer interface {
	Exposed() bool
	Name(index string) string
	Size() int32
	Labels() map[string]string
	Service(name string) *corev1.Service
	SaveOldMeta() bool
}

func (r *PerconaServerMySQLReconciler) reconcileServicePerPod(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, exposer Exposer) error {
	_ = logf.FromContext(ctx).WithName("reconcileServicePerPod")

	if !exposer.Exposed() {
		return nil
	}

	size := int(exposer.Size())
	svcNames := make(map[string]struct{}, size)
	for i := 0; i < size; i++ {
		svcName := exposer.Name(strconv.Itoa(i))
		svc := exposer.Service(svcName)
		svcNames[svc.Name] = struct{}{}

		if err := k8s.EnsureService(ctx, r.Client, cr, svc, r.Scheme, exposer.SaveOldMeta()); err != nil {
			return errors.Wrapf(err, "reconcile svc for pod %s", svc.Name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileMySQLServices(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	_ = logf.FromContext(ctx).WithName("reconcileMySQLServices")

	if err := k8s.EnsureService(ctx, r.Client, cr, mysql.UnreadyService(cr), r.Scheme, true); err != nil {
		return errors.Wrap(err, "reconcile unready svc")
	}

	if err := k8s.EnsureService(ctx, r.Client, cr, mysql.HeadlessService(cr), r.Scheme, true); err != nil {
		return errors.Wrap(err, "reconcile headless svc")
	}

	if err := k8s.EnsureService(ctx, r.Client, cr, mysql.ProxyService(cr), r.Scheme, true); err != nil {
		return errors.Wrap(err, "reconcile proxy svc")
	}

	exposer := mysql.Exposer(*cr)
	if err := r.reconcileServicePerPod(ctx, cr, &exposer); err != nil {
		return errors.Wrap(err, "reconcile service per pod")
	}

	return nil
}

// reconcileMySQLAutoConfig reconciles the ConfigMap for MySQL auto-tuning parameters and
// sets read_only=0 for single-node clusters without Orchestrator
func (r *PerconaServerMySQLReconciler) reconcileMySQLAutoConfig(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileMySQLAutoConfig")
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
		return errors.Wrapf(err, "get ConfigMap/%s", nn.Name)
	}

	setWriteMode := cr.MySQLSpec().Size == 1 && !cr.Spec.Orchestrator.Enabled

	if memory == nil && !setWriteMode {
		exists := true
		if k8serrors.IsNotFound(err) {
			exists = false
		}

		if !exists || !metav1.IsControlledBy(currentConfigMap, cr) {
			return nil
		}

		if err := r.Client.Delete(ctx, currentConfigMap); err != nil {
			return errors.Wrapf(err, "delete ConfigMaps/%s", currentConfigMap.Name)
		}

		log.Info("ConfigMap deleted", "name", currentConfigMap.Name)

		return nil
	}

	config := ""

	// for single-node clusters, we need to set read_only=0 if orchestrator is disabled
	if setWriteMode {
		config = "\nsuper_read_only=0\nread_only=0"
	}

	if memory != nil {
		autotuneParams, err := mysql.GetAutoTuneParams(cr, memory)
		if err != nil {
			return err
		}
		config += autotuneParams
	}

	configMap := k8s.ConfigMap(mysql.AutoConfigMapName(cr), cr.Namespace, mysql.CustomConfigKey, config)
	if !reflect.DeepEqual(currentConfigMap.Data, configMap.Data) {
		if err := k8s.EnsureObject(ctx, r.Client, cr, configMap, r.Scheme); err != nil {
			return errors.Wrapf(err, "ensure ConfigMap/%s", configMap.Name)
		}
		log.Info("ConfigMap updated", "name", configMap.Name, "data", configMap.Data)
	}
	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileOrchestrator(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileOrchestrator")

	if cr.Spec.MySQL.ClusterType == apiv1alpha1.ClusterTypeGR || !cr.OrchestratorEnabled() {
		return nil
	}

	role, binding, sa := orchestrator.RBAC(cr)
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, role, r.Scheme); err != nil {
		return errors.Wrap(err, "create role")
	}
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, sa, r.Scheme); err != nil {
		return errors.Wrap(err, "create service account")
	}
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, binding, r.Scheme); err != nil {
		return errors.Wrap(err, "create role binding")
	}

	cmap := &corev1.ConfigMap{}
	err := r.Client.Get(ctx, orchestrator.NamespacedName(cr), cmap)
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "get config map")
	}

	existingNodes := make([]string, 0)
	if !k8serrors.IsNotFound(err) {
		cfg, ok := cmap.Data[orchestrator.ConfigFileName]
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

	initImage, err := k8s.InitImage(ctx, r.Client, cr, &cr.Spec.Orchestrator.PodSpec)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	tlsHash, err := getTLSHash(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "failed to get tls hash")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.StatefulSet(cr, initImage, tlsHash), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}

	raftNodes := orchestrator.RaftNodes(cr)
	if len(existingNodes) == 0 || len(existingNodes) == len(raftNodes) {
		return nil
	}

	orcPod, err := getReadyOrcPod(ctx, r.Client, cr)
	if err != nil {
		return nil
	}
	g, gCtx := errgroup.WithContext(context.Background())

	if len(raftNodes) > len(existingNodes) {
		newPeers := util.Difference(raftNodes, existingNodes)

		for _, peer := range newPeers {
			p := peer
			g.Go(func() error {
				return orchestrator.AddPeer(gCtx, r.ClientCmd, orcPod, p)
			})
		}

		log.Error(g.Wait(), "Orchestrator add peers", "peers", newPeers)
	} else {
		oldPeers := util.Difference(existingNodes, raftNodes)

		for _, peer := range oldPeers {
			p := peer
			g.Go(func() error {
				return orchestrator.RemovePeer(gCtx, r.ClientCmd, orcPod, p)
			})
		}

		log.Error(g.Wait(), "Orchestrator remove peers", "peers", oldPeers)
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileOrchestratorServices(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if err := k8s.EnsureService(ctx, r.Client, cr, orchestrator.Service(cr), r.Scheme, true); err != nil {
		return errors.Wrap(err, "reconcile Service")
	}

	exposer := orchestrator.Exposer(*cr)
	if err := r.reconcileServicePerPod(ctx, cr, &exposer); err != nil {
		return errors.Wrap(err, "reconcile service per pod")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileHAProxy(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileHAProxy")

	if !cr.HAProxyEnabled() {
		return nil
	}

	configurable := haproxy.Configurable(*cr)
	configHash, err := r.reconcileCustomConfiguration(ctx, cr, &configurable)
	if err != nil {
		return errors.Wrap(err, "reconcile HAProxy config")
	}

	tlsHash, err := getTLSHash(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "failed to get tls hash")
	}

	nn := types.NamespacedName{Namespace: cr.Namespace, Name: mysql.PodName(cr, 0)}
	firstMySQLPodReady, err := k8s.IsPodWithNameReady(ctx, r.Client, nn)
	if err != nil {
		return errors.Wrapf(err, "check if pod %s ready", nn.String())
	}

	if !firstMySQLPodReady {
		log.V(1).Info("Waiting for pod to be ready", "pod", nn.Name)
		return nil
	}

	initImage, err := k8s.InitImage(ctx, r.Client, cr, &cr.Spec.Proxy.HAProxy.PodSpec)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	internalSecret := new(corev1.Secret)
	nn = types.NamespacedName{Name: cr.InternalSecretName(), Namespace: cr.Namespace}
	err = r.Client.Get(ctx, nn, internalSecret)
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrapf(err, "get Secret/%s", nn.Name)
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, haproxy.StatefulSet(cr, initImage, configHash, tlsHash, internalSecret), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileServices(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if err := r.reconcileMySQLServices(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile MySQL services")
	}

	if cr.OrchestratorEnabled() {
		if err := r.reconcileOrchestratorServices(ctx, cr); err != nil {
			return errors.Wrap(err, "reconcile Orchestrator services")
		}
	}

	if cr.HAProxyEnabled() {
		internalSecret := new(corev1.Secret)
		nn := types.NamespacedName{Name: cr.InternalSecretName(), Namespace: cr.Namespace}
		err := r.Client.Get(ctx, nn, internalSecret)
		if client.IgnoreNotFound(err) != nil {
			return errors.Wrapf(err, "get Secret/%s", nn.Name)
		}

		expose := cr.Spec.Proxy.HAProxy.Expose
		if err := k8s.EnsureService(ctx, r.Client, cr, haproxy.Service(cr, internalSecret), r.Scheme, expose.SaveOldMeta()); err != nil {
			return errors.Wrap(err, "reconcile HAProxy svc")
		}
	}

	if cr.RouterEnabled() {
		expose := cr.Spec.Proxy.Router.Expose
		if err := k8s.EnsureService(ctx, r.Client, cr, router.Service(cr), r.Scheme, expose.SaveOldMeta()); err != nil {
			return errors.Wrap(err, "reconcile router svc")
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileReplication(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileReplication")

	if err := r.reconcileGroupReplication(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile group replication")
	}

	if cr.Spec.MySQL.ClusterType == apiv1alpha1.ClusterTypeGR || !cr.OrchestratorEnabled() || cr.Spec.Orchestrator.Size <= 0 {
		return nil
	}

	sts := &appsv1.StatefulSet{}
	// no need to set init image since we're just getting obj from API
	if err := r.Get(ctx, client.ObjectKeyFromObject(orchestrator.StatefulSet(cr, "", "")), sts); err != nil {
		return client.IgnoreNotFound(err)
	}

	if sts.Status.ReadyReplicas == 0 {
		log.Info("orchestrator is not ready. skip", "ready", sts.Status.ReadyReplicas)
		return nil
	}

	pod, err := getReadyOrcPod(ctx, r.Client, cr)
	if err != nil {
		return nil
	}

	if err := orchestrator.Discover(ctx, r.ClientCmd, pod, mysql.ServiceName(cr), mysql.DefaultPort); err != nil {
		switch {
		case errors.Is(err, orchestrator.ErrUnauthorized):
			log.Info("mysql is not ready, unauthorized orchestrator discover response. skip")
			return nil
		case errors.Is(err, orchestrator.ErrEmptyResponse):
			log.Info("mysql is not ready, empty orchestrator discover response. skip")
			return nil
		case errors.Is(err, orchestrator.ErrBadConn):
			log.Info("mysql is not ready, bad connection. skip")
			return nil
		case errors.Is(err, orchestrator.ErrNoSuchHost):
			log.Info("mysql is not ready, host not found. skip")
			return nil
		}
		return errors.Wrap(err, "failed to discover cluster")
	}

	primary, err := orchestrator.ClusterPrimary(ctx, r.ClientCmd, pod, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}
	if primary.Alias == "" {
		log.Info("mysql is not ready, orchestrator cluster primary alias is empty. skip")
		return nil
	}

	// orchestrator doesn't attempt to recover from NonWriteableMaster if there's only 1 MySQL pod
	if cr.MySQLSpec().Size == 1 && primary.ReadOnly {
		if err := orchestrator.SetWriteable(ctx, r.ClientCmd, pod, primary.Key.Hostname, int(primary.Key.Port)); err != nil {
			return errors.Wrapf(err, "set %s writeable", primary.Key.Hostname)
		}
	}

	clusterInstances, err := orchestrator.Cluster(ctx, r.ClientCmd, pod, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster instances")
	}

	// In the case of a cluster downscale, we need to forget replicas that are not part of the cluster
	if len(clusterInstances) > int(cr.MySQLSpec().Size) {
		mysqlPods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
		if err != nil {
			return errors.Wrap(err, "get mysql pods")
		}

		podSet := make(map[string]struct{}, len(mysqlPods))
		for _, p := range mysqlPods {
			podSet[p.Name] = struct{}{}
		}
		for _, instance := range clusterInstances {
			if _, ok := podSet[instance.Alias]; ok {
				continue
			}

			log.Info("Forgeting replica", "replica", instance.Alias)
			err := orchestrator.ForgetInstance(ctx, r.ClientCmd, pod, instance.Alias, int(instance.Key.Port))
			if err != nil {
				return errors.Wrapf(err, "forget replica %s", instance.Alias)
			}
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileGroupReplication(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileGroupReplication")

	if cr.Spec.MySQL.ClusterType != apiv1alpha1.ClusterTypeGR {
		return nil
	}

	if cr.Status.MySQL.Ready == 0 || cr.Status.MySQL.Ready != cr.Spec.MySQL.Size {
		log.V(1).Info("Waiting for MySQL pods to be ready")
		return nil
	}

	pod, err := getReadyMySQLPod(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "get ready mysql pod")
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	db := database.NewReplicationManager(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, mysql.ServiceName(cr))
	cond := meta.FindStatusCondition(cr.Status.Conditions, apiv1alpha1.ConditionInnoDBClusterBootstrapped)
	if cond == nil || cond.Status == metav1.ConditionFalse {
		if exists, err := db.CheckIfDatabaseExists(ctx, "mysql_innodb_cluster_metadata"); err != nil || !exists {
			log.V(1).Info("InnoDB cluster is not created yet")
			return nil
		}

		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               apiv1alpha1.ConditionInnoDBClusterBootstrapped,
			Status:             metav1.ConditionTrue,
			Reason:             apiv1alpha1.ConditionInnoDBClusterBootstrapped,
			Message:            fmt.Sprintf("InnoDB cluster successfully bootstrapped with %d nodes", cr.MySQLSpec().Size),
			LastTransitionTime: metav1.Now(),
		})

		return nil
	}

	if exists, err := db.CheckIfDatabaseExists(ctx, "mysql_innodb_cluster_metadata"); err != nil || !exists {
		return errors.Wrap(err, "InnoDB cluster is already bootstrapped, but failed to check its status")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOutdatedServices(ctx context.Context, exposer Exposer, ns string) error {
	log := logf.FromContext(ctx).WithName("cleanupOutdatedServices")
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
	svcLabels[naming.LabelExposed] = "true"
	services, err := k8s.ServicesByLabels(ctx, r.Client, svcLabels, ns)
	if err != nil {
		return errors.Wrap(err, "get exposed services")
	}

	for _, svc := range services {
		if _, ok := svcNames[svc.Name]; ok {
			continue
		}

		log.Info("Deleting outdated service", "service", svc.Name)
		if err := r.Client.Delete(ctx, &svc); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "delete Service/%s", svc.Name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupMysql(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if !cr.Spec.Pause {
		mysqlExposer := mysql.Exposer(*cr)
		if err := r.cleanupOutdatedServices(ctx, &mysqlExposer, cr.Namespace); err != nil {
			return errors.Wrap(err, "cleanup MySQL services")
		}
	}
	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOrchestrator(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	orcExposer := orchestrator.Exposer(*cr)

	if !cr.OrchestratorEnabled() {
		if err := r.Delete(ctx, orchestrator.StatefulSet(cr, "", "")); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete orchestrator statefulset")
		}

		if err := r.cleanupOutdatedServices(ctx, &orcExposer, cr.Namespace); err != nil {
			return errors.Wrap(err, "cleanup Orchestrator services")
		}

		return nil
	}

	if cr.Spec.Pause {
		return nil
	}

	svcLabels := orcExposer.Labels()
	svcLabels[naming.LabelExposed] = "true"
	services, err := k8s.ServicesByLabels(ctx, r.Client, svcLabels, cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get exposed services")
	}

	if len(services) == int(orcExposer.Size()) {
		return nil
	}

	outdatedSvcs := make(map[string]struct{}, len(services))
	for i := len(services) - 1; i >= int(orcExposer.Size()); i-- {
		outdatedSvcs[orchestrator.PodName(cr, i)] = struct{}{}
	}

	for _, svc := range services {
		if _, ok := outdatedSvcs[svc.Name]; !ok {
			continue
		}

		logf.FromContext(ctx).Info("Deleting outdated orchestrator service", "service", svc.Name)
		if err := r.Client.Delete(ctx, &svc); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "delete Service/%s", svc.Name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupProxies(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if !cr.RouterEnabled() {
		if err := r.Delete(ctx, router.Deployment(cr, "", "", "")); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete router deployment")
		}

		if err := r.Delete(ctx, router.Service(cr)); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete router service")
		}
	}

	if !cr.HAProxyEnabled() {
		if err := r.Delete(ctx, haproxy.StatefulSet(cr, "", "", "", nil)); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete haproxy statefulset")
		}

		if err := r.Delete(ctx, haproxy.Service(cr, nil)); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete haproxy service")
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileMySQLRouter(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileMySQLRouter")

	if !cr.RouterEnabled() {
		return nil
	}

	configurable := router.Configurable(*cr)
	configHash, err := r.reconcileCustomConfiguration(ctx, cr, &configurable)
	if err != nil {
		return errors.Wrap(err, "reconcile Router config")
	}

	tlsHash, err := getTLSHash(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "failed to get tls hash")
	}

	if cr.Spec.Proxy.Router.Size > 0 {
		if cr.Status.MySQL.Ready != cr.Spec.MySQL.Size {
			log.V(1).Info("Waiting for MySQL pods to be ready")
			return nil
		}

		pod, err := getReadyMySQLPod(ctx, r.Client, cr)
		if err != nil {
			return errors.Wrap(err, "get ready mysql pod")
		}

		operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
		if err != nil {
			return errors.Wrap(err, "get operator password")
		}

		db := database.NewReplicationManager(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, mysql.PodFQDN(cr, pod))
		if exist, err := db.CheckIfDatabaseExists(ctx, "mysql_innodb_cluster_metadata"); err != nil || !exist {
			log.V(1).Info("Waiting for InnoDB Cluster", "cluster", cr.Name)
			return nil
		}
	}

	initImage, err := k8s.InitImage(ctx, r.Client, cr, &cr.Spec.Proxy.Router.PodSpec)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, router.Deployment(cr, initImage, configHash, tlsHash), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile Deployment")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileBinlogServer(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if !cr.Spec.Backup.PiTR.Enabled {
		return nil
	}

	logger := logf.FromContext(ctx)

	if len(cr.Status.Host) == 0 {
		logger.V(1).Info("Waiting for .status.host to be populated")
		return nil
	}

	s3 := cr.Spec.Backup.PiTR.BinlogServer.Storage.S3

	if s3 != nil && len(s3.CredentialsSecret) == 0 {
		logger.Info("setting spec.backup.pitr.binlogServer.s3.credentialsSecret is required to upload binlogs to s3")
		return nil
	}

	s3Secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Spec.Backup.PiTR.BinlogServer.Storage.S3.CredentialsSecret,
			Namespace: cr.Namespace,
		},
	}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&s3Secret), &s3Secret); err != nil {
		return errors.Wrap(err, "get s3 credentials secret")
	}

	accessKey := s3Secret.Data[secret.CredentialsAWSAccessKey]
	secretKey := s3Secret.Data[secret.CredentialsAWSSecretKey]

	s3Uri := fmt.Sprintf("s3://%s:%s@%s.%s", accessKey, secretKey, s3.Bucket, s3.Region)
	if len(s3.Prefix) > 0 {
		s3Uri += fmt.Sprintf("/%s", s3.Prefix)
	}

	replPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserReplication)
	if err != nil {
		return errors.Wrap(err, "get replication password")
	}

	configSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      binlogserver.ConfigSecretName(cr),
			Namespace: cr.Namespace,
		},
	}
	configSecret.Data = make(map[string][]byte)

	config := binlogserver.Configuration{
		Logger: binlogserver.Logger{
			Level: "debug",
			File:  "/dev/stdout",
		},
		Connection: binlogserver.Connection{
			Host:           mysql.FQDN(cr, 0),
			Port:           3306,
			User:           string(apiv1alpha1.UserReplication),
			Password:       replPass,
			ConnectTimeout: cr.Spec.Backup.PiTR.BinlogServer.ConnectTimeout,
			WriteTimeout:   cr.Spec.Backup.PiTR.BinlogServer.WriteTimeout,
			ReadTimeout:    cr.Spec.Backup.PiTR.BinlogServer.ReadTimeout,
		},
		Replication: binlogserver.Replication{
			ServerID: cr.Spec.Backup.PiTR.BinlogServer.ServerID,
			IdleTime: cr.Spec.Backup.PiTR.BinlogServer.IdleTime,
		},
		Storage: binlogserver.Storage{
			URI: s3Uri,
		},
	}

	configBytes, err := json.Marshal(config)
	if err != nil {
		return errors.Wrap(err, "marshal binlog server config")
	}

	configSecret.Data[binlogserver.ConfigKey] = configBytes
	if err := k8s.EnsureObject(ctx, r.Client, cr, &configSecret, r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile secret")
	}

	initImage, err := k8s.InitImage(ctx, r.Client, cr, &cr.Spec.Backup.PiTR.BinlogServer.PodSpec)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	err = k8s.EnsureObjectWithHash(ctx, r.Client, cr, binlogserver.StatefulSet(cr, initImage, fmt.Sprintf("%x", md5.Sum(configBytes))), r.Scheme)
	if err != nil {
		return errors.Wrap(err, "reconcile statefulset")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOutdated(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if err := r.cleanupMysql(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup mysql")
	}

	if err := r.cleanupOrchestrator(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup orchestrator")
	}

	if err := r.cleanupProxies(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup proxies")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) getPrimaryFromOrchestrator(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (*orchestrator.Instance, error) {
	pod, err := getReadyOrcPod(ctx, r.Client, cr)
	if err != nil {
		return nil, err
	}
	primary, err := orchestrator.ClusterPrimary(ctx, r.ClientCmd, pod, cr.ClusterHint())
	if err != nil {
		return nil, errors.Wrap(err, "get cluster primary")
	}

	if primary.Key.Hostname == "" {
		primary.Key.Hostname = fmt.Sprintf("%s.%s.%s", primary.Alias, mysql.ServiceName(cr), cr.Namespace)
	}

	return primary, nil
}

func (r *PerconaServerMySQLReconciler) getPrimaryFromGR(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (string, error) {
	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return "", errors.Wrap(err, "get operator password")
	}

	pod, err := getReadyMySQLPod(ctx, r.Client, cr)
	if err != nil {
		return "", errors.Wrap(err, "get ready mysql pod")
	}

	um := database.NewReplicationManager(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, mysql.PodFQDN(cr, pod))

	return um.GetGroupReplicationPrimary(ctx)
}

func (r *PerconaServerMySQLReconciler) getPrimaryHost(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (string, error) {
	log := logf.FromContext(ctx).WithName("getPrimaryHost")

	if cr.Spec.MySQL.IsGR() {
		return r.getPrimaryFromGR(ctx, cr)
	}

	primary, err := r.getPrimaryFromOrchestrator(ctx, cr)
	if err != nil {
		return "", errors.Wrap(err, "get cluster primary")
	}
	log.V(1).Info("Cluster primary from orchestrator", "primary", primary)

	return primary.Key.Hostname, nil
}

func (r *PerconaServerMySQLReconciler) stopAsyncReplication(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, primary *orchestrator.Instance) error {
	log := logf.FromContext(ctx).WithName("stopAsyncReplication")

	orcPod, err := getReadyOrcPod(ctx, r.Client, cr)
	if err != nil {
		return err
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	g, gCtx := errgroup.WithContext(context.Background())
	for _, replica := range primary.Replicas {
		hostname := replica.Hostname
		port := replica.Port
		g.Go(func() error {
			idx, err := getPodIndexFromHostname(hostname)
			if err != nil {
				return err
			}

			pod, err := getMySQLPod(ctx, r.Client, cr, idx)
			if err != nil {
				return err
			}
			repDb := database.NewReplicationManager(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, hostname)

			if err := orchestrator.StopReplication(gCtx, r.ClientCmd, orcPod, hostname, port); err != nil {
				return errors.Wrapf(err, "stop replica %s", hostname)
			}

			status, _, err := repDb.ReplicationStatus(ctx)
			if err != nil {
				return errors.Wrapf(err, "get replication status of %s", hostname)
			}

			for status == database.ReplicationStatusActive {
				time.Sleep(250 * time.Millisecond)
				status, _, err = repDb.ReplicationStatus(ctx)
				if err != nil {
					return errors.Wrapf(err, "get replication status of %s", hostname)
				}
			}

			log.V(1).Info("Stopped replication on replica", "hostname", hostname, "port", port)

			return nil
		})
	}

	return errors.Wrap(g.Wait(), "stop replication on replicas")
}

func (r *PerconaServerMySQLReconciler) startAsyncReplication(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, replicaPass string, primary *orchestrator.Instance) error {
	log := logf.FromContext(ctx).WithName("startAsyncReplication")

	orcPod, err := getReadyOrcPod(ctx, r.Client, cr)
	if err != nil {
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	g, gCtx := errgroup.WithContext(context.Background())
	for _, replica := range primary.Replicas {
		hostname := replica.Hostname
		port := replica.Port
		g.Go(func() error {
			idx, err := getPodIndexFromHostname(hostname)
			if err != nil {
				return err
			}
			pod, err := getMySQLPod(ctx, r.Client, cr, idx)
			if err != nil {
				return err
			}
			um := database.NewReplicationManager(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, hostname)

			log.V(1).Info("Change replication source", "primary", primary.Key.Hostname, "replica", hostname)
			if err := um.ChangeReplicationSource(ctx, primary.Key.Hostname, replicaPass, primary.Key.Port); err != nil {
				return errors.Wrapf(err, "change replication source on %s", hostname)
			}

			if err := orchestrator.StartReplication(gCtx, r.ClientCmd, orcPod, hostname, port); err != nil {
				return errors.Wrapf(err, "start replication on %s", hostname)
			}

			log.V(1).Info("Started replication on replica", "hostname", hostname, "port", port)

			return nil
		})
	}

	return errors.Wrap(g.Wait(), "start replication on replicas")
}

func getReadyMySQLPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL) (*corev1.Pod, error) {
	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return nil, errors.Wrap(err, "get pods")
	}

	for i, pod := range pods {
		if k8s.IsPodReady(pod) {
			return &pods[i], nil
		}
	}
	return nil, errors.New("no ready pods")
}

func getMySQLPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL, idx int) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	nn := types.NamespacedName{Namespace: cr.Namespace, Name: mysql.PodName(cr, idx)}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}

func getReadyOrcPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL) (*corev1.Pod, error) {
	pods, err := k8s.PodsByLabels(ctx, cl, orchestrator.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return nil, errors.Wrap(err, "get pods")
	}

	for i, pod := range pods {
		if k8s.IsPodReady(pod) {
			return &pods[i], nil
		}
	}
	return nil, errors.New("no ready pods")
}

func getPodIndexFromHostname(hostname string) (int, error) {
	hh := strings.Split(hostname, ".")

	if len(hh) == 0 {
		return 0, fmt.Errorf("can't get pod index from hostname: %s", hh)
	}

	ii := strings.Split(hh[0], "-")
	if len(ii) == 0 {
		return 0, fmt.Errorf("can't get pod index from pod name: %s", hh[0])
	}

	idx, err := strconv.Atoi(ii[len(ii)-1])
	if err != nil {
		return 0, err
	}

	return idx, nil
}
