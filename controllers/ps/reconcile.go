package ps

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv2 "github.com/percona/percona-server-mysql-operator/api/v2"

	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
)

type createObjectFn func(context.Context, client.Object, ...client.CreateOption) error

type ctrl struct {
	client client.Client
	log    logr.Logger
}

type Reconciler interface {
	Reconcile(ctx context.Context, nn types.NamespacedName) error
}

func NewReconciler(client client.Client, log logr.Logger) Reconciler {
	return &ctrl{client: client, log: log}
}

func (c *ctrl) Reconcile(ctx context.Context, nn types.NamespacedName) error {
	cr, err := c.loadCR(ctx, nn)
	if err != nil {
		return errors.Wrap(err, "load CR")
	}

	if err := c.reconcileUserSecrets(ctx, cr); err != nil {
		return errors.Wrap(err, "users secret")
	}
	if err := c.createTLSSecret(ctx, cr); err != nil {
		return errors.Wrap(err, "TLS secret")
	}
	if err := c.reconcileDatabase(ctx, cr); err != nil {
		return errors.Wrap(err, "database")
	}
	if err := c.reconcileOrchestrator(ctx, cr); err != nil {
		return errors.Wrap(err, "orchestrator")
	}
	if err := c.reconcileReplication(ctx, cr); err != nil {
		return errors.Wrap(err, "replication")
	}

	return nil
}

func (c *ctrl) loadCR(ctx context.Context, nn types.NamespacedName) (*apiv2.PerconaServerForMySQL, error) {
	o, err := k8s.GetObjectWithDefaults(ctx, c.client, nn, &apiv2.PerconaServerForMySQL{})
	if err != nil {
		return nil, err
	}

	return o.(*apiv2.PerconaServerForMySQL), nil
}

func (c *ctrl) reconcileUserSecrets(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.SecretsName,
	}

	if ok, err := k8s.ObjectExists(ctx, c.client, nn, &corev1.Secret{}); err != nil {
		return errors.Wrap(err, "check existence")
	} else if ok {
		return nil
	}

	secret, err := generatePasswordsSecret(cr.Spec.SecretsName, cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "generate passwords")
	}

	if err := ensureObject(ctx, c.client, cr, secret, c.client.Scheme()); err != nil {
		return errors.Wrapf(err, "create secret %s", cr.Spec.SecretsName)
	}

	return nil
}

var secretUsers = [...]apiv2.SystemUser{
	apiv2.UserRoot,
	apiv2.UserXtraBackup,
	apiv2.UserMonitor,
	apiv2.UserClusterCheck,
	apiv2.UserProxyAdmin,
	apiv2.UserOperator,
	apiv2.UserReplication,
	apiv2.UserOrchestrator,
}

func generatePasswordsSecret(name, namespace string) (*corev1.Secret, error) {
	data := make(map[string][]byte)
	for _, user := range secretUsers {
		pass, err := secret.GeneratePass()
		if err != nil {
			return nil, errors.Wrapf(err, "create %s user password", user)
		}
		data[string(user)] = pass
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
		Type: corev1.SecretTypeOpaque,
	}
	return secret, nil
}

func (c *ctrl) createTLSSecret(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	nn := types.NamespacedName{
		Name:      cr.Spec.SSLSecretName,
		Namespace: cr.Namespace,
	}

	if ok, err := k8s.ObjectExists(ctx, c.client, nn, &corev1.Secret{}); err != nil {
		return errors.Wrap(err, "check existence")
	} else if ok {
		return nil
	}

	secret, err := secret.GenerateCertsSecret(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "create SSL manually")
	}

	if err := ensureObject(ctx, c.client, cr, secret, c.client.Scheme()); err != nil {
		return errors.Wrap(err, "create secret")
	}

	return nil
}

func (c *ctrl) reconcileDatabase(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	initImage, err := k8s.InitImage(ctx, c.client)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	err = ensureObjectWithHash(ctx, c.client, cr, mysql.StatefulSet(cr, initImage), c.client.Scheme())
	if err != nil {
		return errors.Wrap(err, "reconcile sts")
	}
	err = ensureObjectWithHash(ctx, c.client, cr, mysql.Service(cr), c.client.Scheme())
	if err != nil {
		return errors.Wrap(err, "reconcile svc")
	}
	err = ensureObjectWithHash(ctx, c.client, cr, mysql.PrimaryService(cr), c.client.Scheme())
	if err != nil {
		return errors.Wrap(err, "reconcile primary svc")
	}
	err = ensureObjectWithHash(ctx, c.client, cr, mysql.UnreadyService(cr), c.client.Scheme())
	if err != nil {
		return errors.Wrap(err, "reconcile unready svc")
	}

	return nil
}

func (c *ctrl) reconcileOrchestrator(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	err := ensureObjectWithHash(ctx, c.client, cr, orchestrator.StatefulSet(cr), c.client.Scheme())
	if err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}
	err = ensureObjectWithHash(ctx, c.client, cr, orchestrator.Service(cr), c.client.Scheme())
	if err != nil {
		return errors.Wrap(err, "reconcile Service")
	}

	return nil
}

func (c *ctrl) reconcileReplication(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	if err := reconcileReplicationPrimaryPod(ctx, c.client, cr); err != nil {
		return errors.Wrap(err, "reconcile primary pod")
	}
	if err := reconcileReplicationSemiSync(ctx, c.client, cr); err != nil {
		return errors.Wrap(err, "reconcile semi-sync")
	}

	return nil
}

func ensureObject(
	ctx context.Context,
	cl k8s.APICreater,
	cr *apiv2.PerconaServerForMySQL,
	o client.Object,
	s *runtime.Scheme,
) error {
	if err := controllerutil.SetControllerReference(cr, o, s); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s",
			o.GetObjectKind().GroupVersionKind().Kind,
			o.GetName())
	}

	if err := cl.Create(ctx, o); err != nil {
		return errors.Wrapf(err, "create %s/%s",
			o.GetObjectKind().GroupVersionKind().Kind,
			o.GetName())
	}

	return nil
}

func ensureObjectWithHash(
	ctx context.Context,
	cl k8s.APIGetCreatePatcher,
	cr *apiv2.PerconaServerForMySQL,
	obj client.Object,
	s *runtime.Scheme,
) error {
	if err := controllerutil.SetControllerReference(cr, obj, s); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s",
			obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetName())
	}

	metaAccessor, ok := obj.(metav1.ObjectMetaAccessor)
	if !ok {
		return errors.New("can't convert object to ObjectMetaAccessor")
	}

	objectMeta := metaAccessor.GetObjectMeta()

	if objectMeta.GetAnnotations() == nil {
		objectMeta.SetAnnotations(make(map[string]string))
	}

	objAnnotations := objectMeta.GetAnnotations()
	delete(objAnnotations, "percona.com/last-config-hash")
	objectMeta.SetAnnotations(objAnnotations)

	hash, err := getObjectHash(obj)
	if err != nil {
		return errors.Wrap(err, "calculate object hash")
	}

	val := reflect.ValueOf(obj)
	if val.Kind() == reflect.Ptr {
		val = reflect.Indirect(val)
	}
	oldObject := reflect.New(val.Type()).Interface().(client.Object)

	nn := types.NamespacedName{
		Name:      objectMeta.GetName(),
		Namespace: objectMeta.GetNamespace(),
	}
	if err = cl.Get(ctx, nn, oldObject); err != nil {
		if !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "get %v", nn.String())
		}

		if err := cl.Create(ctx, obj); err != nil {
			return errors.Wrapf(err, "create %v", nn.String())
		}

		return nil
	}

	oldObjectMeta := oldObject.(metav1.ObjectMetaAccessor).GetObjectMeta()

	if oldObjectMeta.GetAnnotations()["percona.com/last-config-hash"] != hash ||
		!k8s.LabelsEqual(objectMeta, oldObjectMeta) {

		objectMeta.SetResourceVersion(oldObjectMeta.GetResourceVersion())
		switch object := obj.(type) {
		case *corev1.Service:
			object.Spec.ClusterIP = oldObject.(*corev1.Service).Spec.ClusterIP
			if object.Spec.Type == corev1.ServiceTypeLoadBalancer {
				object.Spec.HealthCheckNodePort = oldObject.(*corev1.Service).Spec.HealthCheckNodePort
			}
		}

		patch := client.StrategicMergeFrom(oldObject)
		if err := cl.Patch(ctx, obj, patch); err != nil {
			return errors.Wrapf(err, "patch %v", nn.String())
		}
	}

	return nil
}

func getObjectHash(obj runtime.Object) (string, error) {
	var dataToMarshall interface{}
	switch object := obj.(type) {
	case *appsv1.StatefulSet:
		dataToMarshall = object.Spec
	case *appsv1.Deployment:
		dataToMarshall = object.Spec
	case *corev1.Service:
		dataToMarshall = object.Spec
	default:
		dataToMarshall = obj
	}
	data, err := json.Marshal(dataToMarshall)
	if err != nil {
		return "", err
	}
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:]), nil
}

func reconcileReplicationPrimaryPod(
	ctx context.Context,
	cl k8s.APIListPatcher,
	cr *apiv2.PerconaServerForMySQL,
) error {
	pods, err := podsByLabels(ctx, cl, mysql.MatchLabels(cr))
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

func reconcileReplicationSemiSync(ctx context.Context, rdr client.Reader, cr *apiv2.PerconaServerForMySQL) error {
	host := orchestrator.APIHost(orchestrator.ServiceName(cr))
	primary, err := orchestrator.ClusterPrimary(ctx, host, cr.ClusterHint())
	if err != nil {
		return errors.Wrap(err, "get primary from orchestrator")
	}

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

	if cr.Spec.MySQL.SizeSemiSync < 1 {
		return nil
	}

	if err := db.SetSemiSyncSize(cr.MySQLSpec().SizeSemiSync); err != nil {
		return errors.Wrapf(err, "set semi-sync size on %s", primary.Hostname())
	}

	return nil
}

func podsByLabels(ctx context.Context, cl k8s.APIList, l map[string]string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	opts := &client.ListOptions{LabelSelector: labels.SelectorFromSet(l)}
	if err := cl.List(ctx, podList, opts); err != nil {
		return nil, err
	}

	return podList.Items, nil
}
