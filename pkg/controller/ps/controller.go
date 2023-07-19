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
	"encoding/json"
	"fmt"
	"reflect"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/controller/psrestore"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

// PerconaServerMySQLReconciler reconciles a PerconaServerMySQL object
type PerconaServerMySQLReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ServerVersion *platform.ServerVersion
	Recorder      record.EventRecorder
	ClientCmd     clientcmd.Client
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqls;perconaservermysqls/status;perconaservermysqls/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods;pods/exec;configmaps;services;secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=certmanager.k8s.io;cert-manager.io,resources=issuers;certificates,verbs=get;list;watch;create;update;patch;delete;deletecollection

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
	defer func() {
		if err := r.reconcileCRStatus(ctx, cr); err != nil {
			log.Error(err, "failed to update status")
		}
	}()

	cr, err := k8s.GetCRWithDefaults(ctx, r.Client, req.NamespacedName, r.ServerVersion)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return rr, errors.Wrap(err, "get CR")
	}

	if cr.ObjectMeta.DeletionTimestamp != nil {
		return rr, r.applyFinalizers(ctx, cr)
	}

	if err := r.doReconcile(ctx, cr); err != nil {
		return rr, errors.Wrap(err, "reconcile")
	}

	return rr, nil
}

func (r *PerconaServerMySQLReconciler) applyFinalizers(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("Finalizer")
	log.Info("Applying finalizers", "CR", cr)

	var err error

	finalizers := []string{}
	for _, f := range cr.GetFinalizers() {
		switch f {
		case "delete-mysql-pods-in-order":
			err = r.deleteMySQLPods(ctx, cr)
		case "delete-ssl":
			err = r.deleteCerts(ctx, cr)
		}

		if err != nil {
			switch err {
			case psrestore.ErrWaitingTermination:
				log.Info("waiting for pods to be deleted", "finalizer", f)
			default:
				log.Error(err, "failed to run finalizer", "finalizer", f)
			}
			finalizers = append(finalizers, f)
		}
	}

	cr.SetFinalizers(finalizers)

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		err = r.Client.Update(ctx, cr)
		if err != nil {
			log.Error(err, "Client.Update failed")
		}
		return err
	})
}

func (r *PerconaServerMySQLReconciler) deleteMySQLPods(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
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
		orcPod, err := getOrcPod(ctx, r.Client, cr, 0)
		if err != nil {
			return nil
		}

		log.Info("Ensuring oldest mysql node is the primary")
		err = orchestrator.EnsureNodeIsPrimaryExec(ctx, r.ClientCmd, orcPod, cr.ClusterHint(), firstPod.GetName(), mysql.DefaultPort)
		if err != nil {
			return errors.Wrap(err, "ensure node is primary")
		}
	} else {
		operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
		if err != nil {
			return errors.Wrap(err, "get operator password")
		}

		firstPodFQDN := fmt.Sprintf("%s.%s.%s", firstPod.Name, mysql.ServiceName(cr), cr.Namespace)
		firstPodUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, firstPodFQDN)

		db, err := replicator.NewReplicatorExec(&firstPod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, firstPodFQDN)
		if err != nil {
			return errors.Wrapf(err, "connect to %s", firstPod.Name)
		}
		defer db.Close()

		mysh, err := mysqlsh.NewWithExec(r.ClientCmd, &firstPod, firstPodUri)
		if err != nil {
			return err
		}

		log.Info("Removing instances from GR")
		for _, pod := range pods {
			if pod.Name == firstPod.Name {
				continue
			}

			podFQDN := fmt.Sprintf("%s.%s.%s", pod.Name, mysql.ServiceName(cr), cr.Namespace)

			state, err := db.GetMemberState(ctx, podFQDN)
			if err != nil {
				return errors.Wrapf(err, "get member state of %s from performance_schema", pod.Name)
			}

			if state == replicator.MemberStateOffline {
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
	log.V(1).Info("Got statefulset", "sts", sts, "spec", sts.Spec)

	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 1 {
		dscaleTo := int32(1)
		sts.Spec.Replicas = &dscaleTo
		err = r.Client.Update(ctx, sts)
		if err != nil {
			return errors.Wrap(err, "downscale StatefulSet")
		}
		log.Info("sts replicaset downscaled", "sts", sts)
	}

	return psrestore.ErrWaitingTermination
}

func (r *PerconaServerMySQLReconciler) deleteCerts(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)
	log.Info("Deleting SSL certificates")

	issuers := []string{
		cr.Name + "-pso-ca-issuer",
		cr.Name + "-pso-issuer",
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
	if err := r.cleanupOutdated(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup outdated")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileDatabase(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQL,
) error {
	log := logf.FromContext(ctx).WithName("reconcileDatabase")

	configurable := mysql.Configurable(*cr)
	configHash, err := r.reconcileCustomConfiguration(ctx, cr, &configurable)
	if err != nil {
		return errors.Wrap(err, "reconcile MySQL config")
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

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.StatefulSet(cr, initImage, configHash, internalSecret), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile sts")
	}

	if pmm := cr.Spec.PMM; pmm != nil && pmm.Enabled && !pmm.HasSecret(internalSecret) {
		log.Info(fmt.Sprintf(`Can't enable PMM: either "%s" key doesn't exist in the secrets, or secrets and internal secrets are out of sync`,
			apiv1alpha1.UserPMMServerKey), "secrets", cr.Spec.SecretsName, "internalSecrets", cr.InternalSecretName())
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

	exposer := mysql.Exposer(*cr)
	if err := r.reconcileServicePerPod(ctx, cr, &exposer); err != nil {
		return errors.Wrap(err, "reconcile service per pod")
	}

	return nil
}

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
	if memory == nil {
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
	autotuneParams, err := mysql.GetAutoTuneParams(cr, memory)
	if err != nil {
		return err
	}
	configMap := k8s.ConfigMap(mysql.AutoConfigMapName(cr), cr.Namespace, mysql.CustomConfigKey, autotuneParams)
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

	initImage, err := k8s.InitImage(ctx, r.Client, cr, &cr.Spec.Orchestrator.PodSpec)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.StatefulSet(cr, initImage), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}

	raftNodes := orchestrator.RaftNodes(cr)
	if len(existingNodes) == 0 || len(existingNodes) == len(raftNodes) {
		return nil
	}

	orcPod, err := getOrcPod(ctx, r.Client, cr, 0)
	if err != nil {
		return nil
	}
	g, gCtx := errgroup.WithContext(context.Background())

	if len(raftNodes) > len(existingNodes) {
		newPeers := util.Difference(raftNodes, existingNodes)

		for _, peer := range newPeers {
			p := peer
			g.Go(func() error {
				return orchestrator.AddPeerExec(gCtx, r.ClientCmd, orcPod, p)
			})
		}

		log.Error(g.Wait(), "Orchestrator add peers", "peers", newPeers)
	} else {
		oldPeers := util.Difference(existingNodes, raftNodes)

		for _, peer := range oldPeers {
			p := peer
			g.Go(func() error {
				return orchestrator.RemovePeerExec(gCtx, r.ClientCmd, orcPod, p)
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

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, haproxy.StatefulSet(cr, initImage, configHash, internalSecret), r.Scheme); err != nil {
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
	if err := r.Get(ctx, client.ObjectKeyFromObject(orchestrator.StatefulSet(cr, "")), sts); err != nil {
		return client.IgnoreNotFound(err)
	}

	if sts.Status.ReadyReplicas == 0 {
		log.Info("orchestrator is not ready. skip", "ready", sts.Status.ReadyReplicas)
		return nil
	}

	pod, err := getOrcPod(ctx, r.Client, cr, 0)
	if err != nil {
		return nil
	}

	if err := orchestrator.DiscoverExec(ctx, r.ClientCmd, pod, mysql.ServiceName(cr), mysql.DefaultPort); err != nil {
		switch err.Error() {
		case "Unauthorized":
			log.Info("mysql is not ready, unauthorized orchestrator discover response. skip")
			return nil
		case orchestrator.ErrEmptyResponse.Error():
			log.Info("mysql is not ready, empty orchestrator discover response. skip")
			return nil
		}
		return errors.Wrap(err, "failed to discover cluster")
	}

	primary, err := orchestrator.ClusterPrimaryExec(ctx, r.ClientCmd, pod, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}
	if primary.Alias == "" {
		log.Info("mysql is not ready, orchestrator cluster primary alias is empty. skip")
		return nil
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

	firstPod, err := getMySQLPod(ctx, r.Client, cr, 0)
	if err != nil {
		return err
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	uri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, mysql.ServiceName(cr))

	mysh, err := mysqlsh.NewWithExec(r.ClientCmd, firstPod, uri)
	if err != nil {
		return err
	}

	cond := meta.FindStatusCondition(cr.Status.Conditions, apiv1alpha1.ConditionInnoDBClusterBootstrapped)
	if cond == nil || cond.Status == metav1.ConditionFalse {
		if !mysh.DoesClusterExistWithExec(ctx, cr.InnoDBClusterName()) {
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

	if !mysh.DoesClusterExistWithExec(ctx, cr.InnoDBClusterName()) {
		return errors.New("InnoDB cluster is already bootstrapped, but failed to check its status")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOutdatedServices(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, exposer Exposer) error {
	log := logf.FromContext(ctx).WithName("cleanupOutdatedServices")

	if !cr.HAProxyEnabled() || cr.Spec.Proxy.HAProxy.Size == 0 {
		svc := haproxy.Service(cr, nil)
		if err := r.Client.Delete(ctx, svc); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "delete HAProxy svc %s", svc.Name)
		}
	}

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

		log.Info("Deleting outdated service", "service", svc.Name)
		if err := r.Client.Delete(ctx, &svc); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "delete Service/%s", svc.Name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOutdatedStatefulSets(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if !cr.OrchestratorEnabled() {
		if err := r.Delete(ctx, orchestrator.StatefulSet(cr, "")); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete orchestrator statefulset")
		}
	}
	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupProxies(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if !cr.RouterEnabled() {
		if err := r.Delete(ctx, router.Deployment(cr, "", "")); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete router deployment")
		}

		if err := r.Delete(ctx, router.Service(cr)); err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete router service")
		}
	}

	if !cr.HAProxyEnabled() {
		if err := r.Delete(ctx, haproxy.StatefulSet(cr, "", "", nil)); err != nil && !k8serrors.IsNotFound(err) {
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

	if cr.Spec.Proxy.Router.Size > 0 {
		if cr.Status.MySQL.Ready != cr.Spec.MySQL.Size {
			log.V(1).Info("Waiting for MySQL pods to be ready")
			return nil
		}

		firstPod, err := getMySQLPod(ctx, r.Client, cr, 0)
		if err != nil {
			return err
		}

		operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
		if err != nil {
			return errors.Wrap(err, "get operator password")
		}

		firstPodUri := mysql.PodName(cr, 0) + "." + mysql.ServiceName(cr) + "." + cr.Namespace
		uri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, firstPodUri)
		mysh, err := mysqlsh.NewWithExec(r.ClientCmd, firstPod, uri)
		if err != nil {
			return err
		}

		if !mysh.DoesClusterExistWithExec(ctx, cr.InnoDBClusterName()) {
			log.V(1).Info("Waiting for InnoDB Cluster", "cluster", cr.Name)
			return nil
		}
	}

	initImage, err := k8s.InitImage(ctx, r.Client, cr, &cr.Spec.Proxy.Router.PodSpec)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, router.Deployment(cr, initImage, configHash), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile Deployment")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupOutdated(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	mysqlExposer := mysql.Exposer(*cr)
	if err := r.cleanupOutdatedServices(ctx, cr, &mysqlExposer); err != nil {
		return errors.Wrap(err, "cleanup MySQL services")
	}

	orcExposer := orchestrator.Exposer(*cr)
	if err := r.cleanupOutdatedServices(ctx, cr, &orcExposer); err != nil {
		return errors.Wrap(err, "cleanup Orchestrator services")
	}

	if err := r.cleanupOutdatedStatefulSets(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup statefulsets")
	}

	if err := r.cleanupProxies(ctx, cr); err != nil {
		return errors.Wrap(err, "cleanup statefulsets")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) getPrimaryFromOrchestrator(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (*orchestrator.Instance, error) {
	pod, err := getOrcPod(ctx, r.Client, cr, 0)
	if err != nil {
		return nil, err
	}
	primary, err := orchestrator.ClusterPrimaryExec(ctx, r.ClientCmd, pod, cr.ClusterHint())
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

	fqdn := mysql.FQDN(cr, 0)

	firstPod, err := getMySQLPod(ctx, r.Client, cr, 0)
	if err != nil {
		return "", err
	}

	db, err := replicator.NewReplicatorExec(firstPod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, fqdn)
	if err != nil {
		return "", errors.Wrapf(err, "open connection to %s", fqdn)
	}

	return db.GetGroupReplicationPrimary(ctx)
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

	orcPod, err := getOrcPod(ctx, r.Client, cr, 0)
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
			repDb, err := replicator.NewReplicatorExec(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, hostname)
			if err != nil {
				return errors.Wrapf(err, "connect to replica %s", hostname)
			}

			if err := orchestrator.StopReplicationExec(gCtx, r.ClientCmd, orcPod, hostname, port); err != nil {
				return errors.Wrapf(err, "stop replica %s", hostname)
			}

			status, _, err := repDb.ReplicationStatus(ctx)
			if err != nil {
				return errors.Wrapf(err, "get replication status of %s", hostname)
			}

			for status == replicator.ReplicationStatusActive {
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

	orcPod, err := getOrcPod(ctx, r.Client, cr, 0)
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
			db, err := replicator.NewReplicatorExec(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, hostname)
			if err != nil {
				return errors.Wrapf(err, "get db connection to %s", hostname)
			}
			defer db.Close()

			log.V(1).Info("Change replication source", "primary", primary.Key.Hostname, "replica", hostname)
			if err := db.ChangeReplicationSource(ctx, primary.Key.Hostname, replicaPass, primary.Key.Port); err != nil {
				return errors.Wrapf(err, "change replication source on %s", hostname)
			}

			if err := orchestrator.StartReplicationExec(gCtx, r.ClientCmd, orcPod, hostname, port); err != nil {
				return errors.Wrapf(err, "start replication on %s", hostname)
			}

			log.V(1).Info("Started replication on replica", "hostname", hostname, "port", port)

			return nil
		})
	}

	return errors.Wrap(g.Wait(), "start replication on replicas")
}

func (r *PerconaServerMySQLReconciler) restartGroupReplication(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, replicaPass string) error {
	log := logf.FromContext(ctx).WithName("restartGroupReplication")

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	hostname := mysql.FQDN(cr, 0)
	firstPod, err := getMySQLPod(ctx, r.Client, cr, 0)
	if err != nil {
		return err
	}
	db, err := replicator.NewReplicatorExec(firstPod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, hostname)
	if err != nil {
		return errors.Wrapf(err, "get db connection to %s", hostname)
	}
	defer db.Close()

	replicas, err := db.GetGroupReplicationReplicas(ctx)
	if err != nil {
		return errors.Wrap(err, "get replicas")
	}

	cmdCli, err := clientcmd.NewClient()
	if err != nil {
		return errors.Wrap(err, "failed to create exec client")
	}

	for _, host := range replicas {
		idx, err := getPodIndexFromHostname(host)
		if err != nil {
			return err
		}

		pod, err := getMySQLPod(ctx, r.Client, cr, idx)
		if err != nil {
			return err
		}
		db, err := replicator.NewReplicatorExec(pod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, host)
		if err != nil {
			return errors.Wrapf(err, "get db connection to %s", hostname)
		}
		defer db.Close()

		// if err := db.StopGroupReplication(ctx); err != nil {
		// 	return errors.Wrapf(err, "stop group replication on %s", host)
		// }
		// log.V(1).Info("Stopped group replication", "hostname", host)

		// if err := db.ChangeGroupReplicationPassword(ctx, replicaPass); err != nil {
		// 	return errors.Wrapf(err, "change group replication password on %s", host)
		// }
		// log.V(1).Info("Changed group replication password", "hostname", host)

		// if err := db.StartGroupReplication(ctx, replicaPass); err != nil {
		// 	return err
		// }

		err = enableMaintananceMode(ctx, cmdCli, pod)
		if err != nil {
			return errors.Wrapf(err, "enable maintainance mode on %s", host)
		}

		if err := db.RestartGroupReplication(replicaPass); err != nil {
			return errors.Wrapf(err, "start group replication on %s", host)
		}

		err = disableMaintananceMode(ctx, cmdCli, pod)
		if err != nil {
			return errors.Wrapf(err, "disable maintainance mode on %s", host)
		}
		log.V(1).Info("Started group replication", "hostname", host)
	}

	primary, err := db.GetGroupReplicationPrimary(ctx)
	if err != nil {
		return errors.Wrap(err, "get primary member")
	}

	idx, err := getPodIndexFromHostname(primary)
	if err != nil {
		return err
	}

	primPod, err := getMySQLPod(ctx, r.Client, cr, idx)
	if err != nil {
		return err
	}
	db, err = replicator.NewReplicatorExec(primPod, r.ClientCmd, apiv1alpha1.UserOperator, operatorPass, primary)
	if err != nil {
		return errors.Wrapf(err, "get db connection to %s", hostname)
	}
	defer db.Close()

	// if err := db.StopGroupReplication(ctx); err != nil {
	// 	return errors.Wrapf(err, "stop group replication on %s", primary)
	// }
	// log.V(1).Info("Stopped group replication", "hostname", primary)

	// if err := db.ChangeGroupReplicationPassword(ctx, replicaPass); err != nil {
	// 	return errors.Wrapf(err, "change group replication password on %s", primary)
	// }
	// log.V(1).Info("Changed group replication password", "hostname", primary)

	// if err := db.StartGroupReplication(ctx, replicaPass); err != nil {
	// 	return nil
	// }

	err = enableMaintananceMode(ctx, cmdCli, primPod)
	if err != nil {
		return errors.Wrapf(err, "enable maintainance mode on %s", primary)
	}

	if err := db.RestartGroupReplication(replicaPass); err != nil {
		return errors.Wrapf(err, "start group replication on %s", primary)
	}

	err = disableMaintananceMode(ctx, cmdCli, primPod)
	if err != nil {
		return errors.Wrapf(err, "disable maintainance mode on %s", primary)
	}
	log.V(1).Info("Started group replication", "hostnaGRStatusme", primary)

	return nil
}

func getMySQLPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL, idx int) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	nn := types.NamespacedName{Namespace: cr.Namespace, Name: mysql.PodName(cr, idx)}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}

func getOrcPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL, idx int) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	nn := types.NamespacedName{Namespace: cr.Namespace, Name: orchestrator.PodName(cr, idx)}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
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
