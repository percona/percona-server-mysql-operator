package orchestrator

import (
	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func StatefulSet(cr *v2.PerconaServerForMySQL) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name(cr),
			Namespace: namespace(cr),
			Labels:    matchLabels(cr),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &cr.Spec.Orchestrator.Size,
			ServiceName: name(cr),
			Selector: &metav1.LabelSelector{
				MatchLabels: matchLabels(cr),
			},
			VolumeClaimTemplates: persistentVolumeClaims(cr),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: matchLabels(cr),
				},
				Spec: corev1.PodSpec{
					Containers: containers(cr),
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy:   corev1.RestartPolicyAlways,
					SchedulerName:   "default-scheduler",
					DNSPolicy:       corev1.DNSClusterFirst,
					Volumes:         volumes(cr),
					SecurityContext: cr.Spec.Orchestrator.PodSecurityContext,
				},
			},
		},
	}
}
