package k8s

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"

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

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const WatchNamespaceEnvVar = "WATCH_NAMESPACE"

// GetWatchNamespace returns the namespace the operator should be watching for changes
func GetWatchNamespace() (string, error) {
	ns, found := os.LookupEnv(WatchNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", WatchNamespaceEnvVar)
	}
	return ns, nil
}

func LabelsEqual(old, new metav1.Object) bool {
	return util.SSMapEqual(old.GetLabels(), new.GetLabels())
}

func RemoveLabel(obj client.Object, key string) {
	labels := obj.GetLabels()
	delete(obj.GetLabels(), key)
	obj.SetLabels(labels)
}

func AddLabel(obj client.Object, key, value string) {
	labels := obj.GetLabels()
	labels[key] = value
	obj.SetLabels(labels)
}

func IsPodReady(pod corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.ContainersReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

func ObjectExists(ctx context.Context, cl client.Reader, nn types.NamespacedName, o client.Object) (bool, error) {
	if err := cl.Get(ctx, nn, o); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

func EnsureObject(
	ctx context.Context,
	cl client.Client,
	cr *apiv1alpha1.PerconaServerMySQL,
	o client.Object,
	s *runtime.Scheme,
) error {
	if err := controllerutil.SetControllerReference(cr, o, s); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s",
			o.GetObjectKind().GroupVersionKind().Kind,
			o.GetName())
	}

	val := reflect.ValueOf(o)
	if val.Kind() == reflect.Ptr {
		val = reflect.Indirect(val)
	}
	oldObject := reflect.New(val.Type()).Interface().(client.Object)

	nn := types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}
	if err := cl.Get(ctx, nn, oldObject); err != nil {
		if !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "get %s/%s", o.GetObjectKind().GroupVersionKind().Kind, o.GetName())
		}

		if err := cl.Create(ctx, o); err != nil {
			return errors.Wrapf(err, "create %s/%s", o.GetObjectKind().GroupVersionKind().Kind, o.GetName())
		}

		return nil
	}

	if err := cl.Update(ctx, o); err != nil {
		return errors.Wrapf(err, "update %s/%s", o.GetObjectKind().GroupVersionKind().Kind, o.GetName())
	}

	return nil
}

func EnsureObjectWithHash(
	ctx context.Context,
	cl client.Client,
	cr *apiv1alpha1.PerconaServerMySQL,
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

	hash, err := ObjectHash(obj)
	if err != nil {
		return errors.Wrap(err, "calculate object hash")
	}

	objAnnotations = objectMeta.GetAnnotations()
	objAnnotations["percona.com/last-config-hash"] = hash
	objectMeta.SetAnnotations(objAnnotations)

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
		!LabelsEqual(objectMeta, oldObjectMeta) {

		objectMeta.SetResourceVersion(oldObjectMeta.GetResourceVersion())
		switch object := obj.(type) {
		case *corev1.Service:
			object.Spec.ClusterIP = oldObject.(*corev1.Service).Spec.ClusterIP
			if object.Spec.Type == corev1.ServiceTypeLoadBalancer {
				object.Spec.HealthCheckNodePort = oldObject.(*corev1.Service).Spec.HealthCheckNodePort
			}
		}

		if err := cl.Patch(ctx, obj, client.StrategicMergeFrom(oldObject)); err != nil {
			return errors.Wrapf(err, "patch %v", nn.String())
		}
	}

	return nil
}

func ObjectHash(obj runtime.Object) (string, error) {
	var dataToMarshal interface{}

	switch object := obj.(type) {
	case *appsv1.StatefulSet:
		dataToMarshal = object.Spec
	case *appsv1.Deployment:
		dataToMarshal = object.Spec
	case *corev1.Service:
		dataToMarshal = object.Spec
	case *corev1.Secret:
		dataToMarshal = object.Data
	default:
		dataToMarshal = obj
	}

	data, err := json.Marshal(dataToMarshal)
	if err != nil {
		return "", err
	}

	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:]), nil
}

func PodsByLabels(ctx context.Context, cl client.Reader, l map[string]string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	opts := &client.ListOptions{LabelSelector: labels.SelectorFromSet(l)}
	if err := cl.List(ctx, podList, opts); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

func ServicesByLabels(ctx context.Context, cl client.Reader, l map[string]string) ([]corev1.Service, error) {
	svcList := &corev1.ServiceList{}

	opts := &client.ListOptions{LabelSelector: labels.SelectorFromSet(l)}
	if err := cl.List(ctx, svcList, opts); err != nil {
		return nil, err
	}

	return svcList.Items, nil
}

// DefaultAPINamespace returns namespace for direct api access from a pod
// https://v1-21.docs.kubernetes.io/docs/tasks/run-application/access-api-from-pod/#directly-accessing-the-rest-api
func DefaultAPINamespace() (string, error) {
	nsBytes, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(nsBytes)), nil
}

// RolloutRestart restarts pods owned by object by updating the pod template with passed annotation key-value.
func RolloutRestart(ctx context.Context, cl client.Client, obj runtime.Object, key apiv1alpha1.AnnotationKey, value string) error {
	switch obj := obj.(type) {
	case *appsv1.StatefulSet:
		orig := obj.DeepCopy()

		if obj.Spec.Template.ObjectMeta.Annotations == nil {
			obj.Spec.Template.ObjectMeta.Annotations = make(map[string]string)
		}
		obj.Spec.Template.ObjectMeta.Annotations[string(key)] = value

		if err := cl.Patch(ctx, obj, client.StrategicMergeFrom(orig)); err != nil {
			return errors.Wrap(err, "patch object")
		}

		return nil
	default:
		return errors.New("not supported")
	}
}
