package node

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator"
	orclient "github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator/client"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

type NodeReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

func (r *NodeReconciler) Reconcile(ctx context.Context, t types.NamespacedName) error {
	log := log.FromContext(ctx).WithName("Pod").WithValues("name", t.Name, "namespace", t.Namespace)

	pod := corev1.Pod{}
	if err := r.Client.Get(ctx, t, &pod); err != nil {
		if k8serrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile tuest.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			return nil
		}
		return errors.Wrapf(err, "get pod with name %s in namespace %s", t.Name, t.Namespace)
	}

	labels := pod.GetLabels()

	crName, ok := labels["app.kubernetes.io/instance"]
	if !ok {
		return nil
	}

	comp, ok := labels["app.kubernetes.io/component"]
	if !ok {
		return nil
	}
	if comp != "mysql" {
		return nil
	}

	cr := &v2.PerconaServerForMySQL{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: crName, Namespace: t.Namespace}, cr); err != nil {
		return errors.Wrapf(err, "get cluster with name %s in namespace %s", crName, t.Namespace)
	}

	o := orchestrator.New(cr)
	orc := orclient.New(o.APIHost())

	clusterHint := cr.ClusterHint()
	primary, err := orc.ClusterMaster(clusterHint)
	if err != nil {
		return errors.Wrap(err, "get primary from orchestrator")
	}

	log.Info("cluster master", "pod", pod.Name, "master", primary.InstanceAlias)

	newLabels := k8s.CloneLabels(labels)

	if primary.InstanceAlias == pod.Name {
		newLabels["mysql.percona.com/primary"] = "true"
	} else {
		delete(newLabels, "mysql.percona.com/primary")
	}

	if k8s.IsLabelsEqual(labels, newLabels) {
		return nil
	}

	pod.SetLabels(newLabels)
	if err := r.Client.Update(ctx, &pod); err != nil {
		return errors.Wrapf(err, "update pod %s", pod.Name)
	}

	return nil
}
