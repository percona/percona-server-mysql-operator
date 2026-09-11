package main

import (
	"context"
	"flag"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func runSetPrimaryLabel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("set-primary-label", flag.ExitOnError)
	primary := fs.String("primary", "", "Primary hostname")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *primary == "" {
		return errors.New("primary flag should not be empty")
	}

	return setPrimaryLabel(ctx, *primary)
}

func setPrimaryLabel(ctx context.Context, primary string) error {
	log := log.WithName("setPrimaryLabel")

	c, err := connect(ctx)
	if err != nil {
		return err
	}
	cr, cl := c.cr, c.client

	primaryName := podName(cr, primary)

	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get MySQL pods")
	}

	if len(pods) == 0 {
		return errors.New("MySQL pods not found")
	}

	var primaryPod *corev1.Pod
	for i := range pods {
		if pods[i].Name == primaryName {
			primaryPod = &pods[i]
			continue
		}
		pod := pods[i].DeepCopy()
		if pod.GetLabels()[naming.LabelMySQLPrimary] == "true" {
			k8s.RemoveLabel(pod, naming.LabelMySQLPrimary)
			if err := cl.Patch(ctx, pod, client.StrategicMergeFrom(&pods[i])); err != nil {
				return errors.Wrapf(err, "remove label from old primary pod: %v/%v", pod.GetNamespace(), pod.GetName())
			}

			log.Info("Removed label from the old primary pod", "pod", pod.GetName(), "namespace", pod.GetNamespace())
		}
	}

	if primaryPod == nil {
		return errors.Errorf("primary pod %s not found %s", primaryName, primary)
	}

	if primaryPod.GetLabels()[naming.LabelMySQLPrimary] == "true" {
		log.Info("Primary pod is not changed, skipping", "pod", primaryName)
		return nil
	}

	pod := primaryPod.DeepCopy()
	k8s.AddLabel(pod, naming.LabelMySQLPrimary, "true")
	if err := cl.Patch(ctx, pod, client.StrategicMergeFrom(primaryPod)); err != nil {
		return errors.Wrapf(err, "add label to new primary pod %v/%v", pod.GetNamespace(), pod.GetName())
	}

	log.Info("Labels added to the new primary pod", "pod", pod.GetName(), "namespace", pod.GetNamespace())
	return nil
}
