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
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
)

// PerconaServerForMYSQLReconciler reconciles a PerconaServerForMYSQL object
type PerconaServerForMySQLReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaserverformysqls;perconaserverformysqls/status;perconaserverformysqls/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods;configmaps;services;secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerForMySQLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv2.PerconaServerForMySQL{}).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PerconaServerForMYSQL object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *PerconaServerForMySQLReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithName("PerconaServerForMySQL")

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

func (r *PerconaServerForMySQLReconciler) doReconcile(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
	if err := r.ensureUserSecrets(ctx, cr); err != nil {
		return errors.Wrap(err, "users secret")
	}
	if err := r.ensureTLSSecret(ctx, cr); err != nil {
		return errors.Wrap(err, "TLS secret")
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

	return nil
}

func (r *PerconaServerForMySQLReconciler) getCRWithDefaults(
	ctx context.Context,
	nn types.NamespacedName,
) (*apiv2.PerconaServerForMySQL, error) {
	cr := &apiv2.PerconaServerForMySQL{}
	if err := r.Client.Get(ctx, nn, cr); err != nil {
		return nil, errors.Wrapf(err, "get %v", nn.String())
	}
	if err := cr.CheckNSetDefaults(); err != nil {
		return nil, errors.Wrapf(err, "check and set defaults for %v", nn.String())
	}

	return cr, nil
}

func (r *PerconaServerForMySQLReconciler) ensureUserSecrets(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.SecretsName,
	}

	if ok, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{}); err != nil {
		return errors.Wrap(err, "check existence")
	} else if ok {
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

func (r *PerconaServerForMySQLReconciler) ensureTLSSecret(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
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

func (r *PerconaServerForMySQLReconciler) reconcileDatabase(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
	initImage, err := k8s.InitImage(ctx, r.Client)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.StatefulSet(cr, initImage), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile sts")
	}

	if err := r.reconcileMySQLServices(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile services")
	}

	return nil
}

func (r *PerconaServerForMySQLReconciler) reconcileMySQLServices(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
	l := log.FromContext(ctx).WithName("reconcileMySQLServices")

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.PrimaryService(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile primary svc")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.UnreadyService(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile unready svc")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, mysql.HeadlessService(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile headless svc")
	}

	mysqlSize := int(cr.Spec.MySQL.Size)
	svcNames := make(map[string]struct{}, mysqlSize)
	for i := 0; i < mysqlSize; i++ {
		svcName := mysql.Name(cr) + "-" + strconv.Itoa(i)
		svcNames[svcName] = struct{}{}
		svc := mysql.PodService(cr, cr.Spec.MySQL.Expose.Type, svcName)

		if cr.Spec.MySQL.Expose.Enabled {
			if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, svc, r.Scheme); err != nil {
				return errors.Wrapf(err, "reconcile svc for pod %s", svcName)
			}
		} else {
			if err := r.Client.Delete(ctx, svc); err != nil && !k8serrors.IsNotFound(err) {
				return errors.Wrapf(err, "delete svc for pod %s", svcName)
			}
		}
	}

	// Clean up outdated services
	svcLabels := mysql.MatchLabels(cr)
	svcLabels[apiv2.ExposedLabel] = "true"
	services, err := k8s.ServicesByLabels(ctx, r.Client, svcLabels)
	if err != nil {
		return errors.Wrap(err, "get MySQL services")
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

func (r *PerconaServerForMySQLReconciler) reconcileOrchestrator(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.StatefulSet(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.Service(cr), r.Scheme); err != nil {
		return errors.Wrap(err, "reconcile Service")
	}

	return nil
}

func (r *PerconaServerForMySQLReconciler) reconcileReplication(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
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
	cr *apiv2.PerconaServerForMySQL,
) error {
	l := log.FromContext(ctx).WithName("reconcileReplicationPrimaryPod")

	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get MySQL pod list")
	}
	l.V(1).Info(fmt.Sprintf("got %v pods", len(pods)))

	host := orchestrator.APIHost(orchestrator.ServiceName(cr))
	primary, err := orchestrator.ClusterPrimary(ctx, host, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}
	primaryAlias := primary.Alias()
	l.V(1).Info(fmt.Sprintf("got cluster primary alias: %v", primaryAlias), "data", primary)

	for i := range pods {
		pod := pods[i].DeepCopy()
		if pod.GetLabels()[apiv2.MySQLPrimaryLabel] == "true" {
			if pod.Name == primaryAlias {
				l.Info(fmt.Sprintf("primary %v is not changed. skip", primaryAlias))
				return nil
			}

			k8s.RemoveLabel(pod, apiv2.MySQLPrimaryLabel)
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
			k8s.AddLabel(pod, apiv2.MySQLPrimaryLabel, "true")
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
	cr *apiv2.PerconaServerForMySQL,
) error {
	l := log.FromContext(ctx).WithName("reconcileReplicationSemiSync")

	host := orchestrator.APIHost(orchestrator.ServiceName(cr))
	primary, err := orchestrator.ClusterPrimary(ctx, host, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}

	primaryHost := primary.Hostname()
	if host == "" {
		primaryHost = primary.Alias()
	}
	if primaryHost == "" {
		l.V(1).Info("no primary host provided. skip", "clusterPrimary", primary)
		return nil
	}
	l.Info("Got primary from orchestrator", "primary", primary)

	primaryHost = fmt.Sprintf("%v.%v.%v", host, mysql.ServiceName(cr), cr.GetNamespace())

	l.V(1).Info(fmt.Sprintf("use primary host: %v", primaryHost), "clusterPrimary", primary)

	operatorPass, err := k8s.UserPassword(ctx, cl, cr, apiv2.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	db, err := replicator.NewReplicator(apiv2.UserOperator,
		operatorPass,
		primary.Hostname(),
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

func (r *PerconaServerForMySQLReconciler) reconcileCRStatus(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
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

	nn := types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}
	return writeStatus(ctx, r.Client, nn, cr.Status)
}

func appStatus(
	ctx context.Context,
	cl client.Reader,
	size int32,
	labels map[string]string,
) (apiv2.StatefulAppStatus, error) {
	status := apiv2.StatefulAppStatus{
		Size:  size,
		State: apiv2.StateInitializing,
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
		status.State = apiv2.StateReady
	}

	return status, nil
}

func writeStatus(
	ctx context.Context,
	cl client.Client,
	nn types.NamespacedName,
	status apiv2.PerconaServerForMySQLStatus,
) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		cr := &apiv2.PerconaServerForMySQL{}
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
