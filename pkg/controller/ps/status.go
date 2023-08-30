package ps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
)

func (r *PerconaServerMySQLReconciler) reconcileCRStatus(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if cr == nil || cr.ObjectMeta.DeletionTimestamp != nil {
		return nil
	}
	if err := cr.CheckNSetDefaults(ctx, r.ServerVersion); err != nil {
		cr.Status.State = apiv1alpha1.StateError
		nn := types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}
		return writeStatus(ctx, r.Client, nn, cr.Status)
	}
	log := logf.FromContext(ctx).WithName("reconcileCRStatus")

	mysqlStatus, err := r.appStatus(ctx, cr, mysql.Name(cr), cr.MySQLSpec().Size, mysql.MatchLabels(cr), cr.Status.MySQL.Version)
	if err != nil {
		return errors.Wrap(err, "get MySQL status")
	}
	cr.Status.MySQL = mysqlStatus

	if mysqlStatus.State == apiv1alpha1.StateReady && cr.Spec.MySQL.IsGR() {
		ready, err := r.isGRReady(ctx, cr)
		if err != nil {
			return errors.Wrap(err, "check if GR ready")
		}
		if !ready {
			mysqlStatus.State = apiv1alpha1.StateInitializing
		}
	}
	cr.Status.MySQL = mysqlStatus

	orcStatus := apiv1alpha1.StatefulAppStatus{}
	if cr.OrchestratorEnabled() && cr.Spec.MySQL.IsAsync() {
		orcStatus, err = r.appStatus(ctx, cr, orchestrator.Name(cr), cr.OrchestratorSpec().Size, orchestrator.MatchLabels(cr), cr.Status.Orchestrator.Version)
		if err != nil {
			return errors.Wrap(err, "get Orchestrator status")
		}
	}
	cr.Status.Orchestrator = orcStatus

	routerStatus := apiv1alpha1.StatefulAppStatus{}
	if cr.RouterEnabled() {
		routerStatus, err = r.appStatus(ctx, cr, router.Name(cr), cr.Spec.Proxy.Router.Size, router.MatchLabels(cr), cr.Status.Router.Version)
		if err != nil {
			return errors.Wrap(err, "get Router status")
		}
	}
	cr.Status.Router = routerStatus

	haproxyStatus := apiv1alpha1.StatefulAppStatus{}
	if cr.HAProxyEnabled() {
		haproxyStatus, err = r.appStatus(ctx, cr, haproxy.Name(cr), cr.Spec.Proxy.HAProxy.Size, haproxy.MatchLabels(cr), cr.Status.HAProxy.Version)
		if err != nil {
			return errors.Wrap(err, "get HAProxy status")
		}
	}
	cr.Status.HAProxy = haproxyStatus

	cr.Status.State = apiv1alpha1.StateReady
	if cr.Spec.MySQL.IsAsync() {
		if cr.OrchestratorEnabled() && cr.Status.Orchestrator.State != apiv1alpha1.StateReady {
			cr.Status.State = cr.Status.Orchestrator.State
		}
		if cr.HAProxyEnabled() && cr.Status.HAProxy.State != apiv1alpha1.StateReady {
			cr.Status.State = cr.Status.HAProxy.State
		}
	} else if cr.Spec.MySQL.IsGR() {
		if cr.RouterEnabled() && cr.Status.Router.State != apiv1alpha1.StateReady {
			cr.Status.State = cr.Status.Router.State
		}
		if cr.HAProxyEnabled() && cr.Status.HAProxy.State != apiv1alpha1.StateReady {
			cr.Status.State = cr.Status.HAProxy.State
		}
	}

	if cr.Status.MySQL.State != apiv1alpha1.StateReady {
		cr.Status.State = cr.Status.MySQL.State
	}

	if cr.Spec.MySQL.IsGR() {
		pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
		if err != nil {
			return errors.Wrap(err, "get pods")
		}

		var outb, errb bytes.Buffer
		cmd := []string{"cat", "/var/lib/mysql/full-cluster-crash"}
		fullClusterCrash := false
		for _, pod := range pods {
			if !k8s.IsPodReady(pod) {
				continue
			}

			err = r.ClientCmd.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false)
			if err != nil {
				if strings.Contains(errb.String(), "No such file or directory") {
					continue
				}
				return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
			}

			fullClusterCrash = true
		}

		if fullClusterCrash {
			cr.Status.State = apiv1alpha1.StateError
			r.Recorder.Event(cr, "Warning", "FullClusterCrashDetected", "Full cluster crash detected")
		}
	}

	cr.Status.Host, err = appHost(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "get app host")
	}

	loadBalancersReady, err := r.allLoadBalancersReady(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "check load balancers")
	}

	if !loadBalancersReady {
		cr.Status.State = apiv1alpha1.StateInitializing
	}

	if err := r.checkTLSIssuer(ctx, cr); err != nil {
		cr.Status.State = apiv1alpha1.StateError
	}

	log.V(1).Info(
		"Writing CR status",
		"mysql", cr.Status.MySQL,
		"orchestrator", cr.Status.Orchestrator,
		"router", cr.Status.Router,
		"haproxy", cr.Status.HAProxy,
		"host", cr.Status.Host,
		"loadbalancers", loadBalancersReady,
		"conditions", cr.Status.Conditions,
		"state", cr.Status.State,
	)

	nn := types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}
	return writeStatus(ctx, r.Client, nn, cr.Status)
}

func (r *PerconaServerMySQLReconciler) isGRReady(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (bool, error) {
	log := logf.FromContext(ctx).WithName("groupReplicationStatus")
	if cr.Status.MySQL.Ready != cr.Spec.MySQL.Size {
		return false, nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return false, errors.Wrap(err, "get operator password")
	}

	firstPod, err := getMySQLPod(ctx, r.Client, cr, 0)
	if err != nil {
		return false, err
	}

	firstPodUri := mysql.PodName(cr, 0) + "." + mysql.ServiceName(cr) + "." + cr.Namespace
	uri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, firstPodUri)
	mysh, err := mysqlsh.NewWithExec(r.ClientCmd, firstPod, uri)
	if err != nil {
		return false, err
	}

	if !mysh.DoesClusterExistWithExec(ctx, cr.InnoDBClusterName()) {
		return false, nil
	}

	status, err := mysh.ClusterStatusWithExec(ctx, cr.InnoDBClusterName())
	if err != nil {
		return false, errors.Wrap(err, "get cluster status")
	}

	for addr, member := range status.DefaultReplicaSet.Topology {
		for _, err := range member.InstanceErrors {
			log.WithName(addr).Info(err)
		}
	}

	log.V(1).Info("GR status", "status", status.DefaultReplicaSet.Status, "statusText", status.DefaultReplicaSet.StatusText)

	switch status.DefaultReplicaSet.Status {
	case innodbcluster.ClusterStatusOK:
		return true, nil
	case innodbcluster.ClusterStatusOKPartial, innodbcluster.ClusterStatusOKNoTolerance, innodbcluster.ClusterStatusOKNoTolerancePartial:
		log.Info("GR status", "status", status.DefaultReplicaSet.Status, "statusText", status.DefaultReplicaSet.StatusText)
		return true, nil
	default:
		return false, nil
	}
}

func (r *PerconaServerMySQLReconciler) allLoadBalancersReady(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (bool, error) {
	opts := &client.ListOptions{Namespace: cr.Namespace, LabelSelector: labels.SelectorFromSet(cr.Labels())}
	svcList := &corev1.ServiceList{}
	if err := r.Client.List(ctx, svcList, opts); err != nil {
		return false, errors.Wrap(err, "list services")
	}
	for _, svc := range svcList.Items {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || !metav1.IsControlledBy(&svc, cr) {
			continue
		}

		if svc.Status.LoadBalancer.Ingress == nil {
			return false, nil
		}
	}
	return true, nil
}

func appHost(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL) (string, error) {
	var serviceName string

	if cr.RouterEnabled() {
		serviceName = router.ServiceName(cr)
		if cr.Spec.Proxy.Router.Expose.Type != corev1.ServiceTypeLoadBalancer {
			return serviceName + "." + cr.GetNamespace(), nil
		}
	}

	if cr.HAProxyEnabled() {
		serviceName = haproxy.ServiceName(cr)
		if cr.Spec.Proxy.HAProxy.Expose.Type != corev1.ServiceTypeLoadBalancer {
			return serviceName + "." + cr.GetNamespace(), nil
		}
	}

	if !cr.RouterEnabled() && !cr.HAProxyEnabled() {
		return mysql.ServiceName(cr) + "." + cr.GetNamespace(), nil
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

func (r *PerconaServerMySQLReconciler) appStatus(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, compName string, size int32, labels map[string]string, version string) (apiv1alpha1.StatefulAppStatus, error) {
	status := apiv1alpha1.StatefulAppStatus{
		Size:  size,
		State: apiv1alpha1.StateInitializing,
	}

	sfsObj := &appsv1.StatefulSet{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: compName, Namespace: cr.Namespace}, sfsObj)
	if err != nil && !k8serrors.IsNotFound(err) {
		return status, err
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, labels)
	if err != nil {
		return status, errors.Wrap(err, "get pod list")
	}

	for i := range pods {
		if k8s.IsPodReady(pods[i]) {
			status.Ready++
		}
	}

	switch {
	case cr.Spec.Pause && status.Ready > 0:
		status.State = apiv1alpha1.StateStopping
	case cr.Spec.Pause && status.Ready == 0:
		status.State = apiv1alpha1.StatePaused
	case sfsObj.Status.Replicas > sfsObj.Status.UpdatedReplicas:
		status.State = apiv1alpha1.StateInitializing
	case status.Ready == status.Size:
		status.State = apiv1alpha1.StateReady
	}

	status.Version = version

	return status, nil
}

func writeStatus(ctx context.Context, cl client.Client, nn types.NamespacedName, status apiv1alpha1.PerconaServerMySQLStatus) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		cr := &apiv1alpha1.PerconaServerMySQL{}
		if err := cl.Get(ctx, nn, cr); err != nil {
			return errors.Wrapf(err, "get %v", nn.String())
		}

		cr.Status = status
		return cl.Status().Update(ctx, cr)
	})
}
