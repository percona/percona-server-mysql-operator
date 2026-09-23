package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

// warn records a warning event on the cluster
func (c *cluster) warn(ctx context.Context, reason, format string, args ...any) {
	now := metav1.NewTime(time.Now())
	message := fmt.Sprintf(format, args...)

	e := &corev1.Event{
		GenerateName: fmt.Sprintf("%s.", c.cr.Name),
		Namespace:    c.cr.Namespace,
		InvolvedObject: corev1.ObjectReference{
			APIVersion:      apiv1.GroupVersion.String(),
			Kind:            "PerconaServerMySQL",
			Name:            c.cr.Name,
			Namespace:       c.cr.Namespace,
			UID:             c.cr.UID,
			ResourceVersion: c.cr.ResourceVersion,
		},
		Reason:         reason,
		Message:        message,
		Type:           corev1.EventTypeWarning,
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
		Source:         corev1.EventSource{Component: "orc-handler"},
	}

	if err := c.client.Create(ctx, e); err != nil {
		log.Error(errors.Wrap(err, "create event"), "failed to record event", "reason", reason, "message", message)
	}
}
