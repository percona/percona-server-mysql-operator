package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

var errRebootClusterFromCompleteOutage = errors.New("run dba.rebootClusterFromCompleteOutage() to reboot the cluster from complete outage")

type mysqlsh struct {
	clusterName string
	host        string
}

func newShell(host string) *mysqlsh {
	return &mysqlsh{
		clusterName: os.Getenv("INNODB_CLUSTER_NAME"),
		host:        host,
	}
}

func (m *mysqlsh) getURI() string {
	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, m.host)
}

func (m *mysqlsh) run(ctx context.Context, cmd string) (bytes.Buffer, bytes.Buffer, error) {
	var stdoutb, stderrb bytes.Buffer

	c := exec.CommandContext(ctx, "mysqlsh", "--no-wizard", "--uri", m.getURI(), "-e", cmd)
	c.Stdout = &stdoutb
	c.Stderr = &stderrb

	err := c.Run()

	return stdoutb, stderrb, err
}

func (m *mysqlsh) clusterStatus(ctx context.Context) (innodbcluster.Status, error) {
	var stdoutb, stderrb bytes.Buffer

	args := []string{"--result-format", "json", "--uri", m.getURI(), "--cluster", "--", "cluster", "status"}

	c := exec.CommandContext(ctx, "mysqlsh", args...)
	c.Stdout = &stdoutb
	c.Stderr = &stderrb

	status := innodbcluster.Status{}

	if err := c.Run(); err != nil {
		return status, errors.Wrapf(err, "run Cluster.status(), stdout: %s, stderr: %s", stdoutb.String(), stderrb.String())
	}

	if err := json.Unmarshal(stdoutb.Bytes(), &status); err != nil {
		return status, errors.Wrap(err, "unmarshal status")
	}

	return status, nil

}

func (m *mysqlsh) configureLocalInstance(ctx context.Context) error {
	stdout, stderr, err := m.run(ctx, fmt.Sprintf("dba.configureLocalInstance('%s', {'clearReadOnly': true})", m.getURI()))
	if err != nil {
		return errors.Wrapf(err, "configure local instance stdout: %s, stderr: %s", stdout.String(), stderr.String())
	}

	return nil
}

func (m *mysqlsh) createCluster(ctx context.Context) error {
	stdout, stderr, err := m.run(ctx, fmt.Sprintf("dba.createCluster('%s')", m.clusterName))
	if err != nil {
		if strings.Contains(stderr.String(), "dba.rebootClusterFromCompleteOutage") {
			return errRebootClusterFromCompleteOutage
		}
		return errors.Wrapf(err, "create InnoDB cluster stdout: %s, stderr: %s", stdout.String(), stderr.String())
	}

	return nil
}

func (m *mysqlsh) rebootClusterFromCompleteOutage(ctx context.Context, force bool) error {
	stdout, stderr, err := m.run(ctx, fmt.Sprintf("dba.rebootClusterFromCompleteOutage('%s', {'force': %t})", m.clusterName, force))
	if err != nil {
		return errors.Wrapf(err, "reboot cluster from complete outage stdout: %s, stderr: %s", stdout.String(), stderr.String())
	}

	return nil
}

func (m *mysqlsh) addInstance(ctx context.Context, instanceDef string) error {
	stdout, stderr, err := m.run(ctx, fmt.Sprintf("dba.getCluster('%s').addInstance('%s', {'recoveryMethod': 'clone', 'waitRecovery': 0})", m.clusterName, instanceDef))
	if err != nil {
		return errors.Wrapf(err, "add instance stdout: %s stderr: %s", stdout.String(), stderr.String())
	}

	return nil
}
func (m *mysqlsh) rejoinInstance(ctx context.Context, instanceDef string) error {
	stdout, stderr, err := m.run(ctx, fmt.Sprintf("dba.getCluster('%s').rejoinInstance('%s')", m.clusterName, instanceDef))
	if err != nil {
		return errors.Wrapf(err, "rejoin instance stdout: %s stderr: %s", stdout.String(), stderr.String())
	}

	return nil
}

func (m *mysqlsh) rescanCluster(ctx context.Context) error {
	stdout, stderr, err := m.run(ctx, fmt.Sprintf("dba.getCluster('%s').rescan({'addInstances': 'auto', 'removeInstances': 'auto'})", m.clusterName))
	if err != nil {
		return errors.Wrapf(err, "rescan cluster stdout: %s stderr: %s", stdout.String(), stderr.String())
	}

	return nil
}

func connectToLocal(ctx context.Context) (*mysqlsh, error) {
	fqdn, err := getFQDN(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return nil, errors.Wrap(err, "get FQDN")
	}

	return newShell(fqdn), nil
}

func connectToCluster(ctx context.Context, peers sets.Set[string]) (*mysqlsh, error) {
	for _, peer := range peers.UnsortedList() {
		shell := newShell(peer)
		stdout, stderr, err := shell.run(ctx, fmt.Sprintf("dba.getCluster('%s')", shell.clusterName))
		if err != nil {
			log.Printf("Failed get cluster from peer %s, stdout: %s stderr: %s", peer, stdout.String(), stderr.String())
			continue
		}

		return shell, nil
	}

	return nil, errors.New("failed to open connection to cluster")
}

func bootstrapGroupReplicationMySQLShell(ctx context.Context) error {
	timer := stopwatch.NewNamedStopwatch()
	err := timer.Add("total")
	if err != nil {
		return errors.Wrap(err, "add timer")
	}
	timer.Start("total")

	defer func() {
		timer.Stop("total")
		log.Printf("bootstrap finished in %f seconds", timer.ElapsedSeconds("total"))
	}()

	exists, err := lockExists()
	if err != nil {
		return errors.Wrap(err, "lock file check")
	}
	if exists {
		log.Printf("Waiting for bootstrap.lock to be deleted")
		if err = waitLockRemoval(); err != nil {
			return errors.Wrap(err, "wait lock removal")
		}
	}

	log.Println("Bootstrap starting...")

	localShell, err := connectToLocal(ctx)
	if err != nil {
		return errors.Wrap(err, "connect to local")
	}

	err = localShell.configureLocalInstance(ctx)
	if err != nil {
		return err
	}
	log.Printf("Instance (%s) configured to join to the InnoDB cluster", localShell.host)

	podName, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get pod hostname")
	}

	peers, err := lookup(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return errors.Wrap(err, "lookup")
	}
	log.Printf("peers: %v", peers.UnsortedList())

	shell, err := connectToCluster(ctx, peers)
	if err != nil {
		if peers.Len() == 1 {
			log.Printf("Creating InnoDB cluster: %s", localShell.clusterName)

			err := localShell.createCluster(ctx)
			if err != nil {
				if errors.Is(err, errRebootClusterFromCompleteOutage) {
					log.Printf("Cluster already exists, we need to reboot")
					err := localShell.rebootClusterFromCompleteOutage(ctx, peers.Len() == 1)
					if err != nil {
						return err
					}
				} else {
					return err
				}
			}

			shell, err = connectToCluster(ctx, peers)
			if err != nil {
				return errors.Wrap(err, "connect to the cluster")
			}
		} else if peers.Len() > 1 && strings.HasSuffix(podName, "-0") {
			log.Printf("Can't connect to any of the peers, we need to reboot")
			err := localShell.rebootClusterFromCompleteOutage(ctx, false)
			if err != nil {
				return err
			}
		} else {
			return errors.Wrap(err, "connect to the cluster")
		}
	}

	log.Printf("Connected to peer %s", shell.host)

	status, err := shell.clusterStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "get cluster status")
	}

	log.Printf("Cluster status:\n%s", status)

	member, ok := status.DefaultReplicaSet.Topology[fmt.Sprintf("%s:%d", localShell.host, 3306)]
	if !ok {
		log.Printf("Adding instance (%s) to InnoDB cluster", localShell.host)

		if err := shell.addInstance(ctx, localShell.getURI()); err != nil {
			return err
		}

		log.Printf("Added instance (%s) to InnoDB cluster", localShell.host)

		return nil
	}

	rescanNeeded := false
	if len(member.InstanceErrors) > 0 {
		log.Printf("Instance (%s) has errors:", localShell.host)
		for i, instErr := range member.InstanceErrors {
			log.Printf("Error %d: %s", i, instErr)
			rescanNeeded = rescanNeeded || strings.Contains(instErr, "rescan()")
		}
	}

	if rescanNeeded {
		err := shell.rescanCluster(ctx)
		if err != nil {
			return err
		}
		log.Println("Cluster rescanned")
	}

	switch member.MemberState {
	case innodbcluster.MemberStateOnline:
		log.Printf("Instance (%s) is already in InnoDB Cluster and its state is %s", localShell.host, member.MemberState)
		return nil
	case innodbcluster.MemberStateMissing:
		log.Printf("Instance (%s) is in InnoDB Cluster but its state is %s", localShell.host, member.MemberState)
		err := shell.rejoinInstance(ctx, localShell.getURI())
		if err != nil {
			return err
		}

		log.Printf("Instance (%s) rejoined to InnoDB cluster", localShell.host)
	default:
		log.Printf("Instance (%s) state is %s", localShell.host, member.MemberState)
	}

	return nil
}
