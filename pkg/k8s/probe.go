package k8s

import (
	corev1 "k8s.io/api/core/v1"
)

func ExecProbe(probe corev1.Probe, cmd []string) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{Command: cmd},
		},
		InitialDelaySeconds:           probe.InitialDelaySeconds,
		TimeoutSeconds:                probe.TimeoutSeconds,
		PeriodSeconds:                 probe.PeriodSeconds,
		FailureThreshold:              probe.FailureThreshold,
		SuccessThreshold:              probe.SuccessThreshold,
		TerminationGracePeriodSeconds: probe.TerminationGracePeriodSeconds,
	}
}
