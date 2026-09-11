package main

import (
	"bytes"
	"context"
	"flag"
	"sort"
	"time"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

const failoverBinary = "/opt/percona/failover"

// execSlack is how much longer than the failover binary this command waits
const execSlack = 10 * time.Second

func runFailover(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("failover", flag.ExitOnError)
	source := fs.String("source", "", "Hostname of the server to fetch the missing binary logs from")
	target := fs.String("target", "", "Hostname of the server to apply the missing binary logs on. Defaults to the most up to date replica of the source")
	timeout := fs.Duration("timeout", time.Minute, "How long fetching and applying the missing binary logs may take")
	failureType := fs.String("failure-type", "", "Analysis code orchestrator reported the problem as. The command only runs for the ones whose recovery promotes a replica")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !orchestrator.IsMasterFailover(*failureType) {
		log.Info("Recovery does not promote a replica, nothing to apply", "failureType", *failureType)
		return nil
	}

	if *source == "" {
		return errors.New("source flag should not be empty")
	}

	if *timeout <= 0 {
		return errors.Errorf("timeout must be positive, got %s", *timeout)
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout+execSlack)
	defer cancel()

	return failover(ctx, *source, *target, *timeout)
}

func failover(ctx context.Context, source, target string, timeout time.Duration) error {
	log := log.WithName("failover")

	c, err := connect(ctx)
	if err != nil {
		return err
	}

	orcPod, err := orchestrator.GetReadyPod(ctx, c.client, c.cr)
	if err != nil {
		return errors.Wrap(err, "get ready orchestrator pod")
	}

	candidate := orchestrator.InstanceKey{Hostname: target, Port: mysql.DefaultPort}
	if target == "" {
		instances, err := orchestrator.Cluster(ctx, c.cliCmd, orcPod, c.cr.ClusterHint())
		if err != nil {
			return errors.Wrap(err, "get cluster instances")
		}

		replicas := replicasOf(instances, source)
		if len(replicas) == 0 {
			log.Info("Source has no replicas, nothing to apply", "source", source)
			return nil
		}

		candidate = mostUpToDate(replicas)
		log.Info("Picked the most up to date replica", "replica", candidate.Hostname, "replicas", len(replicas))
	}

	sourcePod, err := c.pod(ctx, source)
	if err != nil {
		return err
	}
	if sourcePod.Status.PodIP == "" {
		return errors.Errorf("source pod %s has no IP", sourcePod.GetName())
	}

	pod, err := c.pod(ctx, candidate.Hostname)
	if err != nil {
		return err
	}

	cmd := []string{failoverBinary, "-source", sourcePod.Status.PodIP, "-timeout", timeout.String()}

	log.Info("Applying missing binary logs", "pod", pod.GetName(), "source", source, "sourceIP", sourcePod.Status.PodIP)

	var stdout, stderr bytes.Buffer
	if err := c.cliCmd.Exec(ctx, pod, mysql.AppName, cmd, nil, &stdout, &stderr, false); err != nil {
		return errors.Wrapf(err, "run %s in pod %s, stdout: %s, stderr: %s", cmd, pod.GetName(), stdout.String(), stderr.String())
	}

	log.Info("Applied missing binary logs", "pod", pod.GetName(), "source", source, "stdout", stdout.String(), "stderr", stderr.String())

	if err := orchestrator.RegisterCandidate(ctx, c.cliCmd, orcPod, candidate.Hostname, candidate.Port, orchestrator.PromotionRulePrefer); err != nil {
		return errors.Wrapf(err, "register %s as promotion candidate", candidate.Hostname)
	}

	log.Info("Registered the promotion candidate", "candidate", candidate.Hostname)
	return nil
}

func replicasOf(instances []*orchestrator.Instance, host string) []*orchestrator.Instance {
	replicas := make([]*orchestrator.Instance, 0, len(instances))

	for _, instance := range instances {
		if instance.MasterKey.Hostname == host {
			replicas = append(replicas, instance)
		}
	}

	return replicas
}

// mostUpToDate returns the replica with the least to catch up on.
func mostUpToDate(replicas []*orchestrator.Instance) orchestrator.InstanceKey {
	sort.SliceStable(replicas, func(i, j int) bool {
		return betterCandidate(replicas[i], replicas[j])
	})

	return replicas[0].Key
}

func betterCandidate(a, b *orchestrator.Instance) bool {
	if (len(a.Problems) == 0) != (len(b.Problems) == 0) {
		return len(a.Problems) == 0
	}

	if a.ExecBinlogCoordinates != b.ExecBinlogCoordinates {
		return ahead(a.ExecBinlogCoordinates, b.ExecBinlogCoordinates)
	}

	return a.Key.Hostname < b.Key.Hostname
}

func ahead(a, b orchestrator.BinlogCoordinates) bool {
	if a.LogFile != b.LogFile {
		return a.LogFile > b.LogFile
	}

	return a.LogPos > b.LogPos
}
