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
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	nn := req.NamespacedName
	l := log.FromContext(ctx).
		WithName("PerconaServerForMySQL").
		WithValues("name", nn.Name, "namespace", nn.Namespace)

	cr, err := r.getCRWithDefaults(ctx, nn)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 5 * time.Second}, errors.Wrap(err, "get CR")
	}

	if err := r.doReconcile(ctx, l, cr); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, errors.Wrap(err, "reconcile")
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *PerconaServerForMySQLReconciler) doReconcile(
	ctx context.Context,
	log logr.Logger,
	cr *apiv2.PerconaServerForMySQL,
) error {
	if err := r.reconcileUserSecrets(ctx, cr); err != nil {
		return errors.Wrap(err, "users secret")
	}
	if err := r.createTLSSecret(ctx, cr); err != nil {
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

func (r *PerconaServerForMySQLReconciler) reconcileUserSecrets(
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

func (r *PerconaServerForMySQLReconciler) createTLSSecret(
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
	err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.StatefulSet(cr), r.Scheme)
	if err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}
	err = k8s.EnsureObjectWithHash(ctx, r.Client, cr, orchestrator.Service(cr), r.Scheme)
	if err != nil {
		return errors.Wrap(err, "reconcile Service")
	}

	return nil
}

func (r *PerconaServerForMySQLReconciler) reconcileReplication(
	ctx context.Context,
	cr *apiv2.PerconaServerForMySQL,
) error {
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
	cl k8s.APIListPatcher,
	cr *apiv2.PerconaServerForMySQL,
) error {
	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get MySQL pod list")
	}

	host := orchestrator.APIHost(orchestrator.ServiceName(cr))
	primary, err := orchestrator.ClusterPrimary(ctx, host, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get cluster from orchestrator")
	}
	primaryAlias := primary.Alias()

	for i := range pods {
		pod := &pods[i]
		if pod.GetLabels()[apiv2.MySQLPrimaryLabel] == "true" {
			if pod.Name == primaryAlias {
				// primary is not changed
				return nil
			}

			patch := client.StrategicMergeFrom(pod)
			k8s.RemoveLabel(pod, apiv2.MySQLPrimaryLabel)
			if err := cl.Patch(ctx, pod, patch); err != nil {
				return errors.Wrap(err, "remove label from old primary pod")
			}

			break
		}
	}

	for i := range pods {
		pod := &pods[i]
		if pods[i].Name == primaryAlias {
			patch := client.StrategicMergeFrom(pod)
			k8s.AddLabel(pod, apiv2.MySQLPrimaryLabel, "true")
			if err := cl.Patch(ctx, pod, patch); err != nil {
				return errors.Wrap(err, "add label to new primary pod")
			}

			break
		}
	}

	return nil
}

func reconcileReplicationSemiSync(
	ctx context.Context,
	rdr client.Reader,
	cr *apiv2.PerconaServerForMySQL,
) error {
	l := log.FromContext(ctx).WithName("reconcileReplicationSemiSync")

	host := orchestrator.APIHost(orchestrator.ServiceName(cr))
	primary, err := orchestrator.ClusterPrimary(ctx, host, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get primary from orchestrator")
	}
	l.Info("Got primary from orchestrator", "primary", primary)

	operatorPass, err := k8s.UserPassword(ctx, rdr, cr, apiv2.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	db, err := replicator.NewReplicator(apiv2.UserOperator,
		operatorPass,
		primary.Hostname(),
		mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrapf(err, "connect to %s", primary.Hostname())
	}
	defer db.Close()

	if err := db.SetSemiSyncSource(cr.MySQLSpec().SizeSemiSync > 0); err != nil {
		return errors.Wrapf(err, "set semi-sync on %s", primary.Hostname())
	}

	if err := db.SetSemiSyncSize(cr.MySQLSpec().SizeSemiSync); err != nil {
		return errors.Wrapf(err, "set semi-sync size on %s", primary.Hostname())
	}

	return nil
}
