package ps

import (
	"context"
	"fmt"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/topology"
)

// reconcileGRMySQLPrimaryLabel when cluster type is group replication, it reconciles
// the primary pod based on the mysql cluster state by adding to it the respective label.
func (r *PerconaServerMySQLReconciler) reconcileGRMySQLPrimaryLabel(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	logger := logf.FromContext(ctx)

	if !cr.Spec.MySQL.IsGR() {
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		// the internal secret for the password might not be available immediately
		if k8serrors.IsNotFound(errors.Cause(err)) {
			return nil
		}
		return errors.Wrap(err, "get operator password")
	}
	top, err := topology.GroupReplication(ctx, r.Client, r.ClientCmd, cr, operatorPass)
	if err != nil {
		return errors.Wrap(err, "get topology replication")
	}

	primaryPodName, err := podNameFromFQDN(top.Primary)
	if err != nil {
		logger.Error(err, "get primary pod name")
	}
	if primaryPodName == "" {
		return nil
	}

	logger.Info(fmt.Sprintf("primary pod name %s", primaryPodName))

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	var currentPrimaryPod *corev1.Pod
	var correctPrimaryPod *corev1.Pod

	for i, pod := range pods {
		if pod.Labels[naming.LabelMySQLPrimary] == "true" {
			currentPrimaryPod = &pods[i]
		}
		if pod.Name == primaryPodName {
			correctPrimaryPod = &pods[i]
		}
	}

	if currentPrimaryPod != nil && correctPrimaryPod != nil && currentPrimaryPod.Name == correctPrimaryPod.Name {
		return nil
	}

	if currentPrimaryPod != nil && (correctPrimaryPod == nil || currentPrimaryPod.Name != correctPrimaryPod.Name) {
		logger.Info(fmt.Sprintf("Removing primary label from pod %s", currentPrimaryPod.Name))
		if err := r.removePrimaryLabel(ctx, currentPrimaryPod); err != nil {
			return errors.Wrap(err, "remove primary label from wrong pod")
		}
	}

	if correctPrimaryPod != nil {
		logger.Info(fmt.Sprintf("Assigning primary label to pod %s", correctPrimaryPod.Name))
		if err := r.assignPrimaryLabel(ctx, correctPrimaryPod); err != nil {
			return errors.Wrap(err, "assign primary label to correct pod")
		}
	}

	return nil
}

func podNameFromFQDN(fqdn string) (string, error) {
	hh := strings.Split(fqdn, ".")
	if len(hh) == 0 {
		return "", errors.New("can't parse FQDN")
	}
	return hh[0], nil
}

// removePrimaryLabel removes the primary label from a pod
func (r *PerconaServerMySQLReconciler) removePrimaryLabel(ctx context.Context, pod *corev1.Pod) error {
	if pod.Labels == nil {
		return nil
	}

	if _, exists := pod.Labels[naming.LabelMySQLPrimary]; !exists {
		return nil
	}

	delete(pod.Labels, naming.LabelMySQLPrimary)

	if err := r.Update(ctx, pod); err != nil {
		return errors.Wrap(err, "update pod to remove primary label")
	}

	return nil
}

// assignPrimaryLabel assigns the primary label to a pod
func (r *PerconaServerMySQLReconciler) assignPrimaryLabel(ctx context.Context, pod *corev1.Pod) error {
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}

	pod.Labels[naming.LabelMySQLPrimary] = "true"

	if err := r.Update(ctx, pod); err != nil {
		return errors.Wrap(err, "update pod to assign primary label")
	}

	return nil
}
