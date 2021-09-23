package orchestrator

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (o *Orchestrator) StatefulSet() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.Name,
			Namespace: o.Namespace,
			Labels:    o.MatchLabels(),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &o.Size,
			ServiceName: o.Name,
			Selector: &metav1.LabelSelector{
				MatchLabels: o.MatchLabels(),
			},
			VolumeClaimTemplates: o.persistentVolumeClaims(),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: o.MatchLabels(),
				},
				Spec: corev1.PodSpec{
					Containers: o.Containers(),
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy:   corev1.RestartPolicyAlways,
					SchedulerName:   "default-scheduler",
					DNSPolicy:       corev1.DNSClusterFirst,
					Volumes:         o.volumes(),
					SecurityContext: o.PodSecurityContext,
				},
			},
		},
	}
}
