package psmdb

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

	apiv2 "github.com/percona/percona-server-mysql-operator/api/v2"

	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
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
	o, err := k8s.GetObject(ctx, c.client, nn, &apiv2.PerconaServerForMySQL{})
	return o.(*apiv2.PerconaServerForMySQL), err
}

func (c *ctrl) reconcileUserSecrets(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.SecretsName,
	}

	if ok, err := k8s.ObjectExists(ctx, c.client, nn, &corev1.Secret{}); err != nil {
		return errors.Wrap(err, "check existance")
	} else if ok {
		return nil
	}

	secret, err := generatePasswordsSecret(k8s.SecretsName(cr), k8s.Namespace(cr))
	if err != nil {
		return errors.Wrap(err, "generate passwords")
	}

	if err := ensureObject(ctx, cr, secret, c.client.Scheme(), c.client.Create); err != nil {
		return errors.Wrapf(err, "create secret %s", k8s.SecretsName(cr))
	}

	return nil
}

var secretUsers = [...]string{
	apiv2.USERS_SECRET_KEY_ROOT,
	apiv2.USERS_SECRET_KEY_XTRABACKUP,
	apiv2.USERS_SECRET_KEY_MONITOR,
	apiv2.USERS_SECRET_KEY_CLUSTERCHECK,
	apiv2.USERS_SECRET_KEY_PROXYADMIN,
	apiv2.USERS_SECRET_KEY_OPERATOR,
	apiv2.USERS_SECRET_KEY_REPLICATION,
	apiv2.USERS_SECRET_KEY_ORCHESTRATOR,
}

func generatePasswordsSecret(name, namespace string) (*corev1.Secret, error) {
	data := make(map[string][]byte)
	for _, user := range secretUsers {
		pass, err := secret.GeneratePass()
		if err != nil {
			return nil, errors.Wrapf(err, "create %s user password", user)
		}
		data[user] = pass
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
		return errors.Wrap(err, "check existance")
	} else if ok {
		return nil
	}

	secret, err := secret.GenerateCertsSecret(ctx, cr)
	if err != nil {
		return errors.Wrap(err, "create SSL manually")
	}

	if err := ensureObject(ctx, cr, secret, c.client.Scheme(), c.client.Create); err != nil {
		return errors.Wrap(err, "create secret")
	}

	return nil
}

func (c *ctrl) reconcileDatabase(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	initImage, err := k8s.InitImage(ctx, c.client)
	if err != nil {
		return errors.Wrap(err, "get init image")
	}

	createFn := makeCreateWithAnnotation(c.client, c.log)
	if err := ensureObject(ctx, cr, mysql.StatefulSet(cr, initImage), c.client.Scheme(), createFn); err != nil {
		return errors.Wrap(err, "reconcile sts")
	}
	if err := ensureObject(ctx, cr, mysql.Service(cr), c.client.Scheme(), createFn); err != nil {
		return errors.Wrap(err, "reconcile svc")
	}
	if err := ensureObject(ctx, cr, mysql.PrimaryService(cr), c.client.Scheme(), createFn); err != nil {
		return errors.Wrap(err, "reconcile primary svc")
	}

	return nil
}

func (c *ctrl) reconcileOrchestrator(ctx context.Context, cr *apiv2.PerconaServerForMySQL) error {
	createFn := makeCreateWithAnnotation(c.client, c.log)
	if err := ensureObject(ctx, cr, orchestrator.StatefulSet(cr), c.client.Scheme(), createFn); err != nil {
		return errors.Wrap(err, "reconcile StatefulSet")
	}
	if err := ensureObject(ctx, cr, orchestrator.Service(cr), c.client.Scheme(), createFn); err != nil {
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
	cr *apiv2.PerconaServerForMySQL,
	o client.Object,
	s *runtime.Scheme,
	create createObjectFn,
) error {
	if err := k8s.SetControllerReference(cr, o, s); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s",
			o.GetObjectKind().GroupVersionKind().Kind,
			o.GetName())
	}

	if err := create(ctx, o); err != nil {
		return errors.Wrapf(err, "create %s/%s",
			o.GetObjectKind().GroupVersionKind().Kind,
			o.GetName())
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

func makeCreateWithAnnotation(cl k8s.APIGetCreateUpdater, log logr.Logger) createObjectFn {
	return func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
		log = log.WithValues("object", obj)

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

		err = cl.Get(ctx, types.NamespacedName{
			Name:      objectMeta.GetName(),
			Namespace: objectMeta.GetNamespace(),
		}, oldObject)

		if err != nil && !k8serrors.IsNotFound(err) {
			return errors.Wrap(err, "get object")
		}

		if k8serrors.IsNotFound(err) {
			log.Info("object not found. creating")
			return cl.Create(ctx, obj, opts...)
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
			return cl.Update(ctx, obj)
		}

		return nil
	}
}
