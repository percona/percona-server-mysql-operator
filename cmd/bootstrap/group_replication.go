package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

var (
	errRebootClusterFromCompleteOutage   = errors.New("run dba.rebootClusterFromCompleteOutage() to reboot the cluster from complete outage")
	errEmptyGTIDSet                      = errors.New("The target instance has an empty GTID set so it cannot be safely rejoined to the cluster. Please remove it and add it back to the cluster.")
	errInstanceMissingPurgedTransactions = errors.New("The instance is missing transactions that were purged from all cluster members.")
)

var sensitiveRegexp = regexp.MustCompile(":.*@")

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

	log.Printf("Running %s", sensitiveRegexp.ReplaceAllString(cmd, ":*****@"))

	c := exec.CommandContext(ctx, "mysqlsh", "--no-wizard", "--uri", m.getURI(), "-e", cmd)

	logWriter := util.NewSensitiveWriter(log.Writer(), sensitiveRegexp)

	c.Stdout = io.MultiWriter(logWriter, &stdoutb)
	c.Stderr = io.MultiWriter(logWriter, &stderrb)

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

type SQLResult struct {
	Error string              `json:"error,omitempty"`
	Rows  []map[string]string `json:"rows,omitempty"`
}

func (m *mysqlsh) runSQL(ctx context.Context, sql string) (SQLResult, error) {
	var stdoutb, stderrb bytes.Buffer

	cmd := fmt.Sprintf("session.runSql('%s')", sql)
	args := []string{"--uri", m.getURI(), "--json=raw", "--interactive", "--quiet-start", "2", "-e", cmd}

	c := exec.CommandContext(ctx, "mysqlsh", args...)
	c.Stdout = &stdoutb
	c.Stderr = &stderrb

	var result SQLResult

	if err := c.Run(); err != nil {
		return result, errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, stdoutb.String(), stderrb.String())
	}

	if err := json.Unmarshal(stdoutb.Bytes(), &result); err != nil {
		return result, errors.Wrap(err, "unmarshal result")
	}

	if len(result.Error) > 0 {
		return result, errors.New(result.Error)
	}

	return result, nil
}

func (m *mysqlsh) getGTIDExecuted(ctx context.Context) (string, error) {
	result, err := m.runSQL(ctx, "SELECT @@GTID_EXECUTED")
	if err != nil {
		return "", err
	}

	return result.Rows[0]["@@GTID_EXECUTED"], nil
}

func (m *mysqlsh) getGroupSeeds(ctx context.Context) (string, error) {
	result, err := m.runSQL(ctx, "SELECT @@group_replication_group_seeds")
	if err != nil {
		return "", err
	}

	return result.Rows[0]["@@group_replication_group_seeds"], nil
}

func (m *mysqlsh) setGroupSeeds(ctx context.Context, seeds string) (string, error) {
	sql := fmt.Sprintf("SET PERSIST group_replication_group_seeds = \"%s\"", seeds)
	_, err := m.runSQL(ctx, sql)
	if err != nil {
		return "", err
	}

	return "", nil
}

func updateGroupPeers(ctx context.Context, peers sets.Set[string]) error {
	fqdn, err := getFQDN(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return errors.Wrap(err, "get FQDN")
	}

	for _, peer := range peers.UnsortedList() {
		log.Printf("Connecting to peer %s", peer)
		sh := newShell(peer)

		seeds, err := sh.getGroupSeeds(ctx)
		if err != nil {
			log.Printf("ERROR: get @@group_replication_group_seeds from %s: %s", peer, err)
			continue
		}

		log.Printf("Got group_replication_group_seeds from peer %s = %s", peer, seeds)

		tmpSeeds := make([]string, 0)
		if len(seeds) > 0 {
			tmpSeeds = strings.Split(seeds, ",")
		}
		seedSet := sets.New(tmpSeeds...)
		seedSet.Insert(fmt.Sprintf("%s:%d", fqdn, 33061))

		seeds = strings.Join(sets.List(seedSet), ",")

		_, err = sh.setGroupSeeds(ctx, seeds)
		if err != nil {
			log.Printf("ERROR: set @@group_replication_group_seeds in %s: %s", peer, err)
			continue
		}

		log.Printf("Set group_replication_group_seeds in peer %s = %s", peer, seeds)
	}

	return nil
}

func (m *mysqlsh) configureLocalInstance(ctx context.Context) error {
	_, _, err := m.run(ctx, fmt.Sprintf("dba.configureLocalInstance('%s', {'clearReadOnly': true})", m.getURI()))
	if err != nil {
		return errors.Wrap(err, "configure local instance")
	}

	return nil
}

func (m *mysqlsh) createCluster(ctx context.Context) error {
	_, stderr, err := m.run(ctx, fmt.Sprintf("dba.createCluster('%s')", m.clusterName))
	if err != nil {
		if strings.Contains(stderr.String(), "dba.rebootClusterFromCompleteOutage") {
			return errRebootClusterFromCompleteOutage
		}
		return errors.Wrap(err, "create InnoDB cluster")
	}

	return nil
}

func (m *mysqlsh) addInstance(ctx context.Context, instanceDef string) error {
	_, _, err := m.run(ctx, fmt.Sprintf("dba.getCluster('%s').addInstance('%s', {'recoveryMethod': 'clone', 'waitRecovery': 3})", m.clusterName, instanceDef))
	if err != nil {
		return errors.Wrap(err, "add instance")
	}

	return nil
}

func (m *mysqlsh) rejoinInstance(ctx context.Context, instanceDef string) error {
	_, stderr, err := m.run(ctx, fmt.Sprintf("dba.getCluster('%s').rejoinInstance('%s')", m.clusterName, instanceDef))
	if err != nil {
		if strings.Contains(stderr.String(), "empty GTID set") {
			return errEmptyGTIDSet
		}
		if strings.Contains(stderr.String(), "is missing transactions that were purged from all cluster members") {
			return errInstanceMissingPurgedTransactions
		}
		return errors.Wrap(err, "rejoin instance")
	}

	return nil
}

func (m *mysqlsh) removeInstance(ctx context.Context, instanceDef string, force bool) error {
	_, _, err := m.run(ctx, fmt.Sprintf("dba.getCluster('%s').removeInstance('%s', {'force': %t})", m.clusterName, instanceDef, force))
	if err != nil {
		return errors.Wrap(err, "remove instance")
	}

	return nil
}

func (m *mysqlsh) rescanCluster(ctx context.Context) error {
	_, _, err := m.run(ctx, fmt.Sprintf("dba.getCluster('%s').rescan({'addInstances': 'auto', 'removeInstances': 'auto'})", m.clusterName))
	if err != nil {
		return errors.Wrap(err, "rescan cluster")
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
	for _, peer := range sets.List(peers) {
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

func handleFullClusterCrash(ctx context.Context) error {
	localShell, err := connectToLocal(ctx)
	if err != nil {
		return errors.Wrap(err, "connect to local")
	}

	result, err := localShell.getGTIDExecuted(ctx)
	if err != nil {
		return errors.Wrap(err, "get GTID_EXECUTED")
	}

	gtidExecuted := strings.ReplaceAll(result, "\n", "")
	log.Printf("GTID_EXECUTED: %s", gtidExecuted)

	if err := createFile(fullClusterCrashFile, gtidExecuted); err != nil {
		return err
	}

	return nil
}

func bootstrapGroupReplication(ctx context.Context) error {
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

	peers, err := lookup(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return errors.Wrap(err, "lookup")
	}
	log.Printf("peers: %v", sets.List(peers))

	shell, err := connectToCluster(ctx, peers)
	if err != nil {
		log.Printf("Failed to connect to the cluster: %v", err)
		if peers.Len() == 1 {
			log.Printf("Creating InnoDB cluster: %s", localShell.clusterName)

			err := localShell.createCluster(ctx)
			if err != nil {
				if errors.Is(err, errRebootClusterFromCompleteOutage) {
					log.Printf("Cluster already exists, we need to reboot")
					if err := handleFullClusterCrash(ctx); err != nil {
						return errors.Wrap(err, "handle full cluster crash")
					}

					// force restart container
					os.Exit(1)
				} else {
					return err
				}
			}

			shell, err = connectToCluster(ctx, peers)
			if err != nil {
				return errors.Wrap(err, "connect to the cluster")
			}
		} else {
			log.Printf("Can't connect to any of the peers, we need to reboot")
			if err := handleFullClusterCrash(ctx); err != nil {
				return errors.Wrap(err, "handle full cluster crash")
			}

			// force restart container
			os.Exit(1)
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
	}

	rescanNeeded := false
	if len(member.InstanceErrors) > 0 {
		log.Printf("Instance (%s) has errors:", localShell.host)
		for i, instErr := range member.InstanceErrors {
			log.Printf("Error %d: %s", i, instErr)
			rescanNeeded = rescanNeeded || strings.Contains(instErr, "rescan()")
		}
	}

	switch member.MemberState {
	case innodbcluster.MemberStateOnline:
		log.Printf("Instance (%s) is already in InnoDB Cluster and its state is %s", localShell.host, member.MemberState)
	case innodbcluster.MemberStateMissing:
		log.Printf("Instance (%s) is in InnoDB Cluster but its state is %s", localShell.host, member.MemberState)
		err := shell.rejoinInstance(ctx, localShell.getURI())
		if err != nil {
			if errors.Is(err, errEmptyGTIDSet) || errors.Is(err, errInstanceMissingPurgedTransactions) {
				log.Printf("Removing instance (%s) from cluster", localShell.host)
				err := shell.removeInstance(ctx, localShell.getURI(), true)
				if err != nil {
					return err
				}

				// we deliberately fail the bootstrap after removing instance to add it back
				os.Exit(1)
			}
			return err
		}

		log.Printf("Instance (%s) rejoined to InnoDB cluster", localShell.host)
	case innodbcluster.MemberStateUnreachable:
		log.Printf("Instance (%s) is in InnoDB Cluster but its state is %s", localShell.host, member.MemberState)
		os.Exit(1)
	default:
		log.Printf("Instance (%s) state is %s", localShell.host, member.MemberState)
	}

	if err := updateGroupPeers(ctx, peers); err != nil {
		return err
	}

	if rescanNeeded {
		err := shell.rescanCluster(ctx)
		if err != nil {
			return err
		}
		log.Println("Cluster rescanned")
	}

	return nil
}
