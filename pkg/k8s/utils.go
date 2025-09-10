package k8s

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
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

// GetOperatorNamespace returns the namespace of the operator pod
func GetOperatorNamespace() (string, error) {
	ns, found := os.LookupEnv("OPERATOR_NAMESPACE")
	if found {
		return ns, nil
	}

	nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(nsBytes)), nil
}

func objectMetaEqual(old, new metav1.Object) bool {
	return util.SSMapEqual(old.GetLabels(), new.GetLabels()) && util.SSMapEqual(old.GetAnnotations(), new.GetAnnotations())
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

func AddAnnotation(obj client.Object, key, value string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[key] = value
	obj.SetAnnotations(annotations)
}

func IsPodWithNameReady(ctx context.Context, cl client.Client, nn types.NamespacedName) (bool, error) {
	pod := &corev1.Pod{}

	if err := cl.Get(ctx, nn, pod); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return IsPodReady(*pod), nil
}

func IsPodReady(pod corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return false
	}
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
	cr *apiv1.PerconaServerMySQL,
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
	owner metav1.Object,
	obj client.Object,
	s *runtime.Scheme,
) error {
	log := logf.FromContext(ctx)

	if owner != nil {
		if err := controllerutil.SetControllerReference(owner, obj, s); err != nil {
			return errors.Wrapf(err, "set controller reference to %s/%s",
				obj.GetObjectKind().GroupVersionKind().Kind,
				obj.GetName())
		}
	}

	if obj.GetAnnotations() == nil {
		obj.SetAnnotations(make(map[string]string))
	}

	objAnnotations := obj.GetAnnotations()
	delete(objAnnotations, naming.AnnotationLastConfigHash.String())
	obj.SetAnnotations(objAnnotations)

	hash, err := ObjectHash(obj)
	if err != nil {
		return errors.Wrap(err, "calculate object hash")
	}

	objAnnotations = obj.GetAnnotations()
	objAnnotations[naming.AnnotationLastConfigHash.String()] = hash
	obj.SetAnnotations(objAnnotations)

	val := reflect.ValueOf(obj)
	if val.Kind() == reflect.Ptr {
		val = reflect.Indirect(val)
	}
	oldObject := reflect.New(val.Type()).Interface().(client.Object)

	nn := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	if err = cl.Get(ctx, nn, oldObject); err != nil {
		if !k8serrors.IsNotFound(err) {
			return errors.Wrapf(err, "get %v", nn.String())
		}

		log.V(1).Info("Creating object", "name", obj.GetName(), "kind", obj.GetObjectKind())

		if err := cl.Create(ctx, obj); err != nil {
			return errors.Wrapf(err, "create %v", nn.String())
		}

		return nil
	}

	switch obj.(type) {
	case *appsv1.Deployment:
		annotations := obj.GetAnnotations()
		ignoreAnnotations := []string{"deployment.kubernetes.io/revision"}
		for _, key := range ignoreAnnotations {
			v, ok := oldObject.GetAnnotations()[key]
			if ok {
				annotations[key] = v
			}
		}
		obj.SetAnnotations(annotations)
	}

	if oldObject.GetAnnotations()[naming.AnnotationLastConfigHash.String()] != hash ||
		!objectMetaEqual(obj, oldObject) {

		obj.SetResourceVersion(oldObject.GetResourceVersion())
		switch object := obj.(type) {
		case *corev1.Service:
			object.Spec.ClusterIP = oldObject.(*corev1.Service).Spec.ClusterIP
			if object.Spec.Type == corev1.ServiceTypeLoadBalancer {
				object.Spec.HealthCheckNodePort = oldObject.(*corev1.Service).Spec.HealthCheckNodePort
			}
		}

		var patch client.Patch
		switch oldObj := oldObject.(type) {
		case *cm.Certificate:
			patch = client.MergeFrom(oldObj.DeepCopy())
			obj.(*cm.Certificate).TypeMeta = oldObj.DeepCopy().TypeMeta
		default:
			patch = client.StrategicMergeFrom(oldObject)
		}

		log.V(1).Info("Patching object", "name", obj.GetName(), "kind", obj.GetObjectKind())

		if err := cl.Patch(ctx, obj, patch); err != nil {
			return errors.Wrapf(err, "patch %v", nn.String())
		}
	}

	return nil
}

type Component interface {
	Name() string
	PerconaServerMySQL() *apiv1.PerconaServerMySQL
	Labels() map[string]string
	MatchLabels() map[string]string
	PodSpec() *apiv1.PodSpec

	Object(ctx context.Context, cl client.Client) (client.Object, error)
}

func EnsureComponent(
	ctx context.Context,
	cl client.Client,
	c Component,
) error {
	cr := c.PerconaServerMySQL()

	obj, err := c.Object(ctx, cl)
	if err != nil {
		return errors.Wrap(err, "statefulset")
	}
	if err := EnsureObjectWithHash(ctx, cl, cr, obj, cl.Scheme()); err != nil {
		return errors.Wrap(err, "failed to ensure statefulset")
	}

	if cr.CompareVersion("0.12.0") < 0 {
		return nil
	}

	podSpec := c.PodSpec()
	if podSpec == nil || podSpec.PodDisruptionBudget == nil {
		return nil
	}

	if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		return errors.Wrap(err, "get statefulset")
	}

	pdb := podDisruptionBudget(cr, podSpec.PodDisruptionBudget, c.Labels(), c.MatchLabels())
	if err := EnsureObjectWithHash(ctx, cl, obj, pdb, cl.Scheme()); err != nil {
		return errors.Wrap(err, "failed to create pdb")
	}

	return nil
}

func EnsureService(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
	svc *corev1.Service,
	s *runtime.Scheme,
	saveOldMeta bool,
) error {
	if !saveOldMeta && len(cr.Spec.IgnoreAnnotations) == 0 && len(cr.Spec.IgnoreLabels) == 0 {
		return EnsureObjectWithHash(ctx, cl, cr, svc, s)
	}
	oldSvc := new(corev1.Service)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      svc.GetName(),
		Namespace: svc.GetNamespace(),
	}, oldSvc)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return EnsureObjectWithHash(ctx, cl, cr, svc, s)
		}
		return errors.Wrap(err, "get object")
	}

	if saveOldMeta {
		svc.SetAnnotations(util.SSMapMerge(oldSvc.GetAnnotations(), svc.GetAnnotations()))
		svc.SetLabels(util.SSMapMerge(oldSvc.GetLabels(), svc.GetLabels()))
	}
	setIgnoredAnnotations(cr, svc, oldSvc)
	setIgnoredLabels(cr, svc, oldSvc)
	return EnsureObjectWithHash(ctx, cl, cr, svc, s)
}

func setIgnoredAnnotations(cr *apiv1.PerconaServerMySQL, obj, oldObject client.Object) {
	oldAnnotations := oldObject.GetAnnotations()
	if len(oldAnnotations) == 0 {
		return
	}

	ignoredAnnotations := util.SSMapFilterByKeys(oldAnnotations, cr.Spec.IgnoreAnnotations)

	annotations := util.SSMapMerge(obj.GetAnnotations(), ignoredAnnotations)
	obj.SetAnnotations(annotations)
}

func setIgnoredLabels(cr *apiv1.PerconaServerMySQL, obj, oldObject client.Object) {
	oldLabels := oldObject.GetLabels()
	if len(oldLabels) == 0 {
		return
	}

	ignoredLabels := util.SSMapFilterByKeys(oldLabels, cr.Spec.IgnoreLabels)

	labels := util.SSMapMerge(obj.GetLabels(), ignoredLabels)
	obj.SetLabels(labels)
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
	case *cm.Certificate:
		dataToMarshal = object.Spec
	case *cm.Issuer:
		dataToMarshal = object.Spec
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

func PodsByLabels(ctx context.Context, cl client.Reader, l map[string]string, namespace string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(l),
		Namespace:     namespace,
	}
	if err := cl.List(ctx, podList, opts); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

func ServicesByLabels(ctx context.Context, cl client.Reader, l map[string]string, namespace string) ([]corev1.Service, error) {
	svcList := &corev1.ServiceList{}

	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(l),
		Namespace:     namespace,
	}
	if err := cl.List(ctx, svcList, opts); err != nil {
		return nil, err
	}

	return svcList.Items, nil
}

func PVCsByLabels(ctx context.Context, cl client.Reader, l map[string]string, namespace string) ([]corev1.PersistentVolumeClaim, error) {
	pvcList := &corev1.PersistentVolumeClaimList{}

	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(l),
		Namespace:     namespace,
	}
	if err := cl.List(ctx, pvcList, opts); err != nil {
		return nil, err
	}

	return pvcList.Items, nil
}

// DefaultAPINamespace returns namespace for direct api access from a pod
// https://v1-21.docs.kubernetes.io/docs/tasks/run-application/access-api-from-pod/#directly-accessing-the-rest-api
func DefaultAPINamespace() (string, error) {
	nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(nsBytes)), nil
}

// RolloutRestart restarts pods owned by object by updating the pod template with passed annotation key-value.
func RolloutRestart(ctx context.Context, cl client.Client, obj runtime.Object, key naming.AnnotationKey, value string) error {
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

func GetCRWithDefaults(
	ctx context.Context,
	cl client.Client,
	nn types.NamespacedName,
	serverVersion *platform.ServerVersion,
) (*apiv1.PerconaServerMySQL, error) {
	cr := new(apiv1.PerconaServerMySQL)
	if err := cl.Get(ctx, nn, cr); err != nil {
		return nil, errors.Wrapf(err, "get %v", nn.String())
	}
	if err := SetCRVersion(ctx, cl, cr); err != nil {
		return cr, errors.Wrapf(err, "set CR version for %v", nn.String())
	}

	if err := cr.CheckNSetDefaults(ctx, serverVersion); err != nil {
		return cr, errors.Wrapf(err, "check and set defaults for %v", nn.String())
	}

	return cr, nil
}

func DeleteSecrets(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, secretNames []string) error {
	for _, secretName := range secretNames {
		secret := &corev1.Secret{}
		err := cl.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      secretName,
		}, secret)
		if err != nil {
			continue
		}

		err = cl.Delete(ctx, secret,
			&client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &secret.UID}})
		if err != nil {
			return errors.Wrapf(err, "delete secret %s", secretName)
		}
	}

	return nil
}

func GetImageIDFromPod(pod *corev1.Pod, containerName string) (string, error) {
	idx := slices.IndexFunc(pod.Status.ContainerStatuses, func(s corev1.ContainerStatus) bool {
		return s.Name == containerName
	})

	if idx == -1 {
		return "", errors.Errorf("%s not found in pod", containerName)
	}

	return pod.Status.ContainerStatuses[idx].ImageID, nil
}

func GetTLSHash(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL) (string, error) {
	secret := new(corev1.Secret)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      cr.Spec.SSLSecretName,
		Namespace: cr.Namespace,
	}, secret)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return "", nil
		}
		return "", errors.Wrap(err, "get secret")
	}

	hash, err := ObjectHash(secret)
	if err != nil {
		return "", errors.Wrap(err, "get secret hash")
	}

	return hash, nil
}

func SetCRVersion(
	ctx context.Context,
	cl client.Client,
	cr *apiv1.PerconaServerMySQL,
) error {
	if cr.Spec.CRVersion != "" {
		return nil
	}

	orig := cr.DeepCopy()
	cr.Spec.CRVersion = version.Version()

	if err := cl.Patch(ctx, cr, client.MergeFrom(orig)); err != nil {
		return errors.Wrap(err, "patch CR version")
	}

	logf.FromContext(ctx).Info("Set CR version", "version", cr.Spec.CRVersion)
	return nil
}
func EqualMetadata(m ...metav1.ObjectMeta) bool {
	if len(m) <= 1 {
		return true
	}
	filter := func(m metav1.ObjectMeta) metav1.ObjectMeta {
		delete(m.Annotations, naming.AnnotationLastConfigHash.String())
		return metav1.ObjectMeta{
			Name:                       m.Name,
			GenerateName:               m.GenerateName,
			Namespace:                  m.Namespace,
			DeletionGracePeriodSeconds: m.DeletionGracePeriodSeconds,
			Labels:                     m.Labels,
			Annotations:                m.Annotations,
			Finalizers:                 m.Finalizers,
		}
	}
	first := m[0]
	for i := 1; i < len(m); i++ {
		if !reflect.DeepEqual(filter(first), filter(m[i])) {
			return false
		}
	}
	return true
}
