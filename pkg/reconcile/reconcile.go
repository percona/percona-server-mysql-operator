package reconcile

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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2 "github.com/percona/percona-server-mysql-operator/api/v2"

	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

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
		return errors.Wrap(err, "reconcile users secret")
	}

	if err := reconcileSSLSecrets(ctx, c.client, cr); err != nil {
		return errors.Wrap(err, "reconcile TLS secret")
	}

	if err := c.reconcileMySQL(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile MySQL")
	}

	if err := c.reconcileOrchestrator(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile orchestrator")
	}

	if err := c.reconcileReplication(ctx, cr); err != nil {
		return errors.Wrap(err, "reconcile replication")
	}

	return nil
}

func (c *ctrl) loadCR(ctx context.Context, nn types.NamespacedName) (*v2.PerconaServerForMySQL, error) {
	o, err := k8s.GetObject(ctx, c.client, nn, &v2.PerconaServerForMySQL{})
	if err != nil {
		return nil, err
	}

	return o.(*v2.PerconaServerForMySQL), nil
}

func (c *ctrl) reconcileUserSecrets(ctx context.Context, cr *v2.PerconaServerForMySQL) error {
	secretObj := corev1.Secret{}
	err := c.client.Get(ctx,
		types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.Spec.SecretsName,
		},
		&secretObj,
	)
	if err == nil {
		return nil
	} else if !k8serrors.IsNotFound(err) {
		return errors.Wrap(err, "get secret")
	}

	users := []string{
		v2.USERS_SECRET_KEY_ROOT,
		v2.USERS_SECRET_KEY_XTRABACKUP,
		v2.USERS_SECRET_KEY_MONITOR,
		v2.USERS_SECRET_KEY_CLUSTERCHECK,
		v2.USERS_SECRET_KEY_PROXYADMIN,
		v2.USERS_SECRET_KEY_OPERATOR,
		v2.USERS_SECRET_KEY_REPLICATION,
		v2.USERS_SECRET_KEY_ORCHESTRATOR,
	}
	data := make(map[string][]byte)
	for _, user := range users {
		data[user], err = generatePass()
		if err != nil {
			return errors.Wrapf(err, "create %s user password", user)
		}
	}

	secretObj = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Spec.SecretsName,
			Namespace: cr.Namespace,
		},
		Data: data,
		Type: corev1.SecretTypeOpaque,
	}

	if err := k8s.SetControllerReference(cr, &secretObj, c.client.Scheme()); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", secretObj.Kind, secretObj.Name)
	}

	err = c.client.Create(ctx, &secretObj)
	return errors.Wrapf(err, "create users secret '%s'", cr.Spec.SecretsName)
}

func (c *ctrl) reconcileMySQL(ctx context.Context, cr *v2.PerconaServerForMySQL) error {
	initImage, err := k8s.InitImage(ctx, c.client)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	if err := c.ensureObject(ctx, cr, mysql.StatefulSet(cr, initImage)); err != nil {
		return errors.Wrap(err, "reconcile sts")
	}
	if err := c.ensureObject(ctx, cr, mysql.Service(cr)); err != nil {
		return errors.Wrap(err, "reconcile svc")
	}
	if err := c.ensureObject(ctx, cr, mysql.PrimaryService(cr)); err != nil {
		return errors.Wrap(err, "reconcile primary svc")
	}

	return nil
}

func (c *ctrl) reconcileOrchestrator(ctx context.Context, cr *v2.PerconaServerForMySQL) error {
	if err := c.ensureObject(ctx, cr, orchestrator.StatefulSet(cr)); err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}
	if err := c.ensureObject(ctx, cr, orchestrator.Service(cr)); err != nil {
		return errors.Wrap(err, "reconcile Service")
	}

	return nil
}

func (c *ctrl) reconcileReplication(ctx context.Context, cr *v2.PerconaServerForMySQL) error {
	if err := reconcileReplicationPrimaryPod(ctx, c.client, cr); err != nil {
		return errors.Wrap(err, "reconcile primary pod")
	}
	if err := reconcileReplicationSemiSync(ctx, c.client, cr); err != nil {
		return errors.Wrap(err, "reconcile semi-sync")
	}

	return nil
}

func (c *ctrl) ensureObject(ctx context.Context, cr *v2.PerconaServerForMySQL, o client.Object) error {
	if err := k8s.SetControllerReference(cr, o, c.client.Scheme()); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s",
			o.GetObjectKind().GroupVersionKind().Kind,
			o.GetName())
	}

	if err := c.createOrUpdate(ctx, o); err != nil {
		return errors.Wrapf(err, "create or update %s/%s",
			o.GetObjectKind().GroupVersionKind().Kind,
			o.GetName())
	}

	return nil
}

func (c *ctrl) createOrUpdate(ctx context.Context, obj client.Object) error {
	log := c.log.WithValues("object", obj)

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
	log = log.WithValues("hash", hash)

	objAnnotations = objectMeta.GetAnnotations()
	objAnnotations["percona.com/last-config-hash"] = hash
	objectMeta.SetAnnotations(objAnnotations)

	val := reflect.ValueOf(obj)
	if val.Kind() == reflect.Ptr {
		val = reflect.Indirect(val)
	}
	oldObject := reflect.New(val.Type()).Interface().(client.Object)

	err = c.client.Get(ctx, types.NamespacedName{
		Name:      objectMeta.GetName(),
		Namespace: objectMeta.GetNamespace(),
	}, oldObject)

	if err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrap(err, "get object")
	}

	if k8serrors.IsNotFound(err) {
		log.Info("object not found. creating")
		return c.client.Create(ctx, obj)
	}

	oldObjectMeta := oldObject.(metav1.ObjectMetaAccessor).GetObjectMeta()

	if oldObjectMeta.GetAnnotations()["percona.com/last-config-hash"] != hash ||
		!k8s.IsObjectMetaEqual(objectMeta, oldObjectMeta) {

		objectMeta.SetResourceVersion(oldObjectMeta.GetResourceVersion())
		switch object := obj.(type) {
		case *corev1.Service:
			object.Spec.ClusterIP = oldObject.(*corev1.Service).Spec.ClusterIP
			if object.Spec.Type == corev1.ServiceTypeLoadBalancer {
				object.Spec.HealthCheckNodePort = oldObject.(*corev1.Service).Spec.HealthCheckNodePort
			}
		}

		log.Info("updating")
		return c.client.Update(ctx, obj)
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
