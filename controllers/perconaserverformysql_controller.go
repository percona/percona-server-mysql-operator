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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	psv2 "github.com/percona/percona-mysql/api/v2"
	"github.com/percona/percona-mysql/pkg/cluster"
)

// PerconaServerForMYSQLReconciler reconciles a PerconaServerForMYSQL object
type PerconaServerForMySQLReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaserverformysqls;perconaserverformysqls/status;perconaserverformysqls/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods;pods/exec;pods/log;configmaps;services;persistentvolumeclaims;secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments;replicasets;statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=certmanager.k8s.io;cert-manager.io,resources=issuers;certificates,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PerconaServerForMYSQL object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *PerconaServerForMySQLReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := MySQLReconciler.Reconcile(ctx, req.NamespacedName)

	return ctrl.Result{
		RequeueAfter: fiveSeconds,
	}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerForMySQLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	MySQLReconciler = &cluster.MySQLReconciler{
		Client: r.Client,
		Scheme: r.Scheme,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&psv2.PerconaServerForMySQL{}).
		Complete(r)
}
