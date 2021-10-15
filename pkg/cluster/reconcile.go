package cluster

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
	"sigs.k8s.io/controller-runtime/pkg/log"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

type MySQLReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

func (r *MySQLReconciler) Reconcile(ctx context.Context, t types.NamespacedName) error {
	log := log.FromContext(ctx).WithName("PerconaServerForMySQL").WithValues("name", t.Name, "namespace", t.Namespace)

	cr := &v2.PerconaServerForMySQL{}
	err := r.Client.Get(ctx, t, cr)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			return nil
		}
		return errors.Wrapf(err, "get cluster with name %s in namespace %s", t.Name, t.Namespace)
	}

	if err := cr.CheckNSetDefaults(log); err != nil {
		return errors.Wrap(err, "check CR options")
	}

	if err := r.reconcileUsersSecret(cr); err != nil {
		return errors.Wrap(err, "reconcile users secret")
	}

	if err := r.reconcileSSL(log, cr); err != nil {
		return errors.Wrap(err, "reconcile TLS secret")
	}

	if err := r.reconcileMySQL(log, cr); err != nil {
		return errors.Wrap(err, "reconcile MySQL")
	}

	if err := r.reconcileOrchestrator(log, cr); err != nil {
		return errors.Wrap(err, "reconcile orchestrator")
	}

	return nil
}

func (r *MySQLReconciler) createOrUpdate(log logr.Logger, obj client.Object) error {
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

	err = r.Client.Get(context.Background(), types.NamespacedName{
		Name:      objectMeta.GetName(),
		Namespace: objectMeta.GetNamespace(),
	}, oldObject)

	if err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrap(err, "get object")
	}

	if k8serrors.IsNotFound(err) {
		log.Info("object not found. creating")
		return r.Client.Create(context.TODO(), obj)
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
		return r.Client.Update(context.TODO(), obj)
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
