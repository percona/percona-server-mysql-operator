package mysql

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (m *MySQL) StatefulSet() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
			Labels:    m.MatchLabels(),
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: m.MatchLabels(),
			},
			VolumeClaimTemplates: m.persistentVolumeClaims(),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: m.MatchLabels(),
				},
				Spec: corev1.PodSpec{
					Containers: m.Containers(),
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy:   corev1.RestartPolicyAlways,
					SchedulerName:   "default-scheduler",
					DNSPolicy:       corev1.DNSClusterFirst,
					SecurityContext: m.PodSecurityContext,
					Volumes:         m.volumes(),
				},
			},
		},
	}
}

