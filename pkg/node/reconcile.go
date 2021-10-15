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
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
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

	pod := &corev1.Pod{}
	if err := r.Client.Get(ctx, t, pod); err != nil {
		if k8serrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile tuest.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			return nil
		}
		return errors.Wrapf(err, "get pod with name %s in namespace %s", t.Name, t.Namespace)
	}

	crName, err := v2.GetClusterNameFromObject(pod)
	if err != nil {
		// it's a pod that is not managed by us
		return nil
	}

	if !mysql.IsMySQL(pod) {
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

	log.Info("check cluster primary", "pod", pod.Name, "primary", primary.InstanceAlias)

	newLabels := k8s.CloneLabels(pod.GetLabels())

	if primary.InstanceAlias == pod.Name {
		newLabels[v2.MySQLPrimaryLabel] = "true"
	} else {
		delete(newLabels, v2.MySQLPrimaryLabel)
	}

	if k8s.IsLabelsEqual(pod.GetLabels(), newLabels) {
		return nil
	}

	pod.SetLabels(newLabels)
	if err := r.Client.Update(ctx, pod); err != nil {
		return errors.Wrapf(err, "update pod %s", pod.Name)
	}

	return nil
}
