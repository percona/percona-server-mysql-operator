package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	v "github.com/hashicorp/go-version"
	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"

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
	version     *v.Version
}

func newShell(host string, version *v.Version) *mysqlsh {
	return &mysqlsh{
		clusterName: os.Getenv("INNODB_CLUSTER_NAME"),
		version:     version,
		host:        host,
	}
}

func (m *mysqlsh) compareVersionWith(ver string) int {
	return m.version.Compare(v.Must(v.NewVersion(ver)))
}

func (m *mysqlsh) getURI() string {
	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, url.QueryEscape(operatorPass), m.host)
}

func (m *mysqlsh) run(ctx context.Context, cmd string) (bytes.Buffer, bytes.Buffer, error) {
	var stdoutb, stderrb bytes.Buffer

	log.Printf("Running %s", sensitiveRegexp.ReplaceAllString(cmd, ":*****@"))

	c := exec.CommandContext(ctx, "mysqlsh", "--no-wizard", "--js", "--uri", m.getURI(), "-e", cmd)

	logWriter := util.NewSensitiveWriter(log.Writer(), sensitiveRegexp)

	c.Stdout = io.MultiWriter(logWriter, &stdoutb)
	c.Stderr = io.MultiWriter(logWriter, &stderrb)

	err := c.Run()

	return stdoutb, stderrb, errors.Wrapf(err, "stderr: %s", stderrb.String())
}

func (m *mysqlsh) clusterStatus(ctx context.Context) (innodbcluster.Status, error) {
	var stdoutb, stderrb bytes.Buffer

	args := []string{"--result-format", "json", "--uri", m.getURI(), "--cluster", "--js", "--", "cluster", "status"}

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

func (m *mysqlsh) rescanCluster(ctx context.Context) error {
	var cmd string
	if m.compareVersionWith("8.4") >= 0 {
		cmd = fmt.Sprintf("dba.getCluster('%s').rescan({'addUnmanaged': true, 'removeObsolete': true})", m.clusterName)
	} else {
		cmd = fmt.Sprintf("dba.getCluster('%s').rescan({'addInstances': 'auto', 'removeInstances': 'auto'})", m.clusterName)
	}

	if _, _, err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "rescan cluster")
	}

	return nil
}

type SQLResult struct {
	Error string              `json:"error,omitempty"`
	Rows  []map[string]string `json:"rows,omitempty"`
}

func (m *mysqlsh) runSQL(ctx context.Context, sql string) (SQLResult, error) {
	var stdoutb, stderrb bytes.Buffer

	cmd := fmt.Sprintf("session.runSql(\"%s\")", sql)
	args := []string{"--uri", m.getURI(), "--js", "--json=raw", "--interactive", "--quiet-start", "2", "-e", cmd}

	c := exec.CommandContext(ctx, "mysqlsh", args...)
	c.Stdout = &stdoutb
	c.Stderr = &stderrb

	var result SQLResult

	if err := c.Run(); err != nil {
		return result, errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, stdoutb.String(), stderrb.String())
	}

	if err := json.Unmarshal(stdoutb.Bytes(), &result); err != nil {
		return result, errors.Wrapf(err, "unmarshal result %s", stdoutb.String())
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

func (m *mysqlsh) getGTIDPurged(ctx context.Context) (string, error) {
	result, err := m.runSQL(ctx, "SELECT @@GTID_PURGED")
	if err != nil {
		return "", err
	}

	return result.Rows[0]["@@GTID_PURGED"], nil
}

func (m *mysqlsh) getGroupSeeds(ctx context.Context) (string, error) {
	result, err := m.runSQL(ctx, "SELECT @@group_replication_group_seeds")
	if err != nil {
		return "", err
	}

	return result.Rows[0]["@@group_replication_group_seeds"], nil
}

func (m *mysqlsh) setGroupSeeds(ctx context.Context, seeds string) error {
	sql := fmt.Sprintf("SET PERSIST group_replication_group_seeds = '%s'", seeds)

	_, err := m.runSQL(ctx, sql)
	if err != nil {
		return err
	}

	return nil
}

func updateGroupPeers(ctx context.Context, peers sets.Set[string], version *v.Version) error {
	log.Printf("Updating group seeds in peers: %v", peers)

	seedList := make([]string, 0)
	for _, peer := range peers.UnsortedList() {
		seedList = append(seedList, fmt.Sprintf("%s:%d", peer, 3306))
	}

	slices.SortFunc(seedList, func(a, b string) int {
		return strings.Compare(a, b)
	})

	for _, peer := range peers.UnsortedList() {
		sh := newShell(peer, version)

		log.Printf("Connected to peer %s", peer)

		tmpSeeds := make([]string, 0)
		for _, seed := range seedList {
			// peer shouldn't have its own host as seed
			if seed == fmt.Sprintf("%s:%d", peer, 3306) {
				continue
			}
			tmpSeeds = append(tmpSeeds, seed)
		}

		seeds := strings.Join(tmpSeeds, ",")
		if seeds == "" {
			log.Printf("seeds are empty")
			continue
		}

		if err := sh.setGroupSeeds(ctx, seeds); err != nil {
			log.Printf("ERROR: failed to set @@group_replication_group_seeds in %s: %s", peer, err)
			continue
		}

		log.Printf("Seeds: %s", seeds)
		log.Printf("Updated group_replication_group_seeds in peer %s", peer)
	}

	return nil
}

func (m *mysqlsh) configureInstance(ctx context.Context) error {
	var cmd string
	if m.compareVersionWith("8.4") >= 0 {
		cmd = fmt.Sprintf("dba.configureInstance('%s')", m.getURI())
	} else {
		cmd = fmt.Sprintf("dba.configureLocalInstance('%s', {'clearReadOnly': true})", m.getURI())
	}

	if _, _, err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "configure instance")
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

type GTIDSetRelation string

const (
	GTIDSetEqual      GTIDSetRelation = "EQUAL"
	GTIDSetContained  GTIDSetRelation = "CONTAINED"
	GTIDSetContains   GTIDSetRelation = "CONTAINS"
	GTIDSetDisjoint   GTIDSetRelation = "DISJOINT"
	GTIDSetIntersects GTIDSetRelation = "INTERSECTS"
)

func gtidSubtract(ctx context.Context, shell *mysqlsh, a, b string) (string, error) {
	a = strings.ReplaceAll(a, "\n", "")
	b = strings.ReplaceAll(b, "\n", "")

	query := fmt.Sprintf("SELECT GTID_SUBTRACT('%s', '%s') AS sub", a, b)

	result, err := shell.runSQL(ctx, query)
	if err != nil {
		return "", errors.Wrapf(err, "execute %s", query)
	}

	return result.Rows[0]["sub"], nil
}

func compareGTIDs(ctx context.Context, shell *mysqlsh, a, b string) (GTIDSetRelation, error) {
	if a == "" || b == "" {
		if a == "" {
			if b == "" {
				return GTIDSetEqual, nil
			}
			return GTIDSetContained, nil
		}

		return GTIDSetContains, nil
	}

	aSubB, err := gtidSubtract(ctx, shell, a, b)
	if err != nil {
		return "", errors.Wrapf(err, "a sub b")
	}

	bSubA, err := gtidSubtract(ctx, shell, b, a)
	if err != nil {
		return "", errors.Wrapf(err, "b sub a")
	}

	if aSubB == "" && bSubA == "" {
		return GTIDSetEqual, nil
	} else if aSubB == "" && bSubA != "" {
		return GTIDSetContained, nil
	} else if aSubB != "" && bSubA == "" {
		return GTIDSetContains, nil
	} else {
		query := fmt.Sprintf("GTID_SUBTRACT('%s', '%s')", a, b)
		abIntersection, err := gtidSubtract(ctx, shell, a, query)
		if err != nil {
			return "", errors.Wrapf(err, "intersection")
		}

		if abIntersection == "" {
			return GTIDSetDisjoint, nil
		}

		return GTIDSetIntersects, nil
	}
}

// If purged has more gtids than the executed on the replica
// it means some data will not be recoverable
func comparePrimaryPurged(ctx context.Context, shell *mysqlsh, purged, executed string) bool {
	query := fmt.Sprintf("SELECT GTID_SUBTRACT('%s', '%s') = ''", purged, executed)

	result, err := shell.runSQL(ctx, query)
	if err != nil {
		return false
	}

	sub, err := strconv.Atoi(result.Rows[0]["GTID_SUBTRACT"])
	if err != nil {
		return false
	}

	return sub == 0
}

func checkReplicaState(
	ctx context.Context,
	primary, replica string,
	version *v.Version,
) (innodbcluster.ReplicaGtidState, error) {
	primarySh := newShell(primary, version)
	replicaSh := newShell(replica, version)

	primaryExecuted, err := primarySh.getGTIDExecuted(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get GTID_EXECUTED from primary")
	}
	log.Printf("Primary GTID_EXECUTED=%s", primaryExecuted)

	primaryPurged, err := primarySh.getGTIDPurged(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get GTID_PURGED from primary")
	}
	log.Printf("Primary GTID_PURGED=%s", primaryPurged)

	replicaExecuted, err := replicaSh.getGTIDExecuted(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get GTID_EXECUTED from replica")
	}
	log.Printf("Replica GTID_EXECUTED=%s", replicaExecuted)

	if replicaExecuted == "" && primaryPurged == "" && primaryExecuted != "" {
		return innodbcluster.ReplicaGtidNew, nil
	}

	relation, err := compareGTIDs(ctx, primarySh, primaryExecuted, replicaExecuted)
	if err != nil {
		return "", errors.Wrap(err, "compare GTIDs")
	}

	switch relation {
	case GTIDSetIntersects, GTIDSetDisjoint, GTIDSetContained:
		return innodbcluster.ReplicaGtidDiverged, nil
	case GTIDSetEqual:
		return innodbcluster.ReplicaGtidIdentical, nil
	case GTIDSetContains:
		if primaryPurged == "" || comparePrimaryPurged(ctx, primarySh, primaryPurged, replicaExecuted) {
			return innodbcluster.ReplicaGtidRecovarable, nil
		}
		return innodbcluster.ReplicaGtidIrrecovarable, nil
	}

	return "", errors.New("internal error")
}

func getRecoveryMethod(
	ctx context.Context,
	primary, replica string,
	version *v.Version,
) (innodbcluster.RecoveryMethod, error) {
	replicaState, err := checkReplicaState(ctx, primary, replica, version)
	if err != nil {
		return "", errors.Wrap(err, "check replica state")
	}

	switch replicaState {
	case innodbcluster.ReplicaGtidDiverged:
		return innodbcluster.RecoveryClone, nil
	case innodbcluster.ReplicaGtidIrrecovarable:
		return innodbcluster.RecoveryClone, nil
	case innodbcluster.ReplicaGtidRecovarable, innodbcluster.ReplicaGtidIdentical:
		return innodbcluster.RecoveryIncremental, nil
	case innodbcluster.ReplicaGtidNew:
		return innodbcluster.RecoveryIncremental, nil
	default:
		return innodbcluster.RecoveryClone, nil
	}
}

func (m *mysqlsh) addInstance(ctx context.Context, instanceDef string, method innodbcluster.RecoveryMethod) error {
	var cmd string

	if m.compareVersionWith("8.4") >= 0 {
		cmd = fmt.Sprintf(
			"dba.getCluster('%s').addInstance('%s', {'recoveryMethod': '%s', 'recoveryProgress': 1})",
			m.clusterName,
			instanceDef,
			method,
		)
	} else {
		cmd = fmt.Sprintf(
			"dba.getCluster('%s').addInstance('%s', {'recoveryMethod': '%s', 'waitRecovery': 2})",
			m.clusterName,
			instanceDef,
			method,
		)
	}

	if _, _, err := m.run(ctx, cmd); err != nil {
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

func connectToLocal(ctx context.Context, version *v.Version) (*mysqlsh, error) {
	fqdn, err := getFQDN(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return nil, errors.Wrap(err, "get FQDN")
	}

	return newShell(fqdn, version), nil
}

func connectToCluster(ctx context.Context, peers sets.Set[string], version *v.Version) (*mysqlsh, error) {
	for _, peer := range sets.List(peers) {
		shell := newShell(peer, version)
		stdout, stderr, err := shell.run(ctx, fmt.Sprintf("dba.getCluster('%s')", shell.clusterName))
		if err != nil {
			log.Printf("Failed get cluster from peer %s, stdout: %s stderr: %s", peer, stdout.String(), stderr.String())
			continue
		}

		return shell, nil
	}

	return nil, errors.New("failed to open connection to cluster")
}

func handleFullClusterCrash(ctx context.Context, version *v.Version) error {
	localShell, err := connectToLocal(ctx, version)
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

func getMySQLShellVersion(ctx context.Context) (*v.Version, error) {
	re, err := regexp.Compile(`MySQL (\d+\.\d+\.\d+)`)
	if err != nil {
		return nil, err
	}

	var stdoutb, stderrb bytes.Buffer

	c := exec.CommandContext(ctx, "mysqlsh", "--version")
	c.Stdout = &stdoutb
	c.Stderr = &stderrb

	if err := c.Run(); err != nil {
		return nil, errors.Wrapf(err, "run mysqlsh --version (stdout: %s, stderr: %s)", stdoutb.String(), stderrb.String())
	}

	f := re.FindSubmatch(stdoutb.Bytes())
	if len(f) < 1 {
		return nil, errors.Errorf("couldn't extract version information from mysqlsh --version (stdout: %s, stderr: %s)", stdoutb.String(), stderrb.String())
	}

	version, err := v.NewVersion(string(f[1]))
	if err != nil {
		return nil, errors.Wrap(err, "parse version")
	}

	return version, nil
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

	log.Println("Starting bootstrap...")

	mysqlshVer, err := getMySQLShellVersion(ctx)
	if err != nil {
		return errors.Wrap(err, "get mysqlsh version")
	}
	log.Println("mysql-shell version:", mysqlshVer)

	localShell, err := connectToLocal(ctx, mysqlshVer)
	if err != nil {
		return errors.Wrap(err, "connect to local")
	}

	err = localShell.configureInstance(ctx)
	if err != nil {
		return err
	}
	log.Printf("Instance (%s) configured to join to the InnoDB cluster", localShell.host)

	peers, err := lookup(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return errors.Wrap(err, "lookup")
	}
	log.Printf("peers: %v", sets.List(peers))

	shell, err := connectToCluster(ctx, peers, mysqlshVer)
	if err != nil {
		log.Printf("Failed to connect to the cluster: %v", err)
		if peers.Len() == 1 {
			log.Printf("Creating InnoDB cluster: %s", localShell.clusterName)

			err := localShell.createCluster(ctx)
			if err != nil {
				if errors.Is(err, errRebootClusterFromCompleteOutage) {
					log.Printf("Cluster already exists, we need to reboot")
					if err := handleFullClusterCrash(ctx, mysqlshVer); err != nil {
						return errors.Wrap(err, "handle full cluster crash")
					}

					// force restart container
					os.Exit(1)
				} else {
					return err
				}
			}

			shell, err = connectToCluster(ctx, peers, mysqlshVer)
			if err != nil {
				return errors.Wrap(err, "connect to the cluster")
			}
		} else {
			log.Printf("Can't connect to any of the peers, we need to reboot")
			if err := handleFullClusterCrash(ctx, mysqlshVer); err != nil {
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

	var primary string
	for _, member := range status.DefaultReplicaSet.Topology {
		if member.MemberRole == innodbcluster.MemberRolePrimary {
			primary = member.Address
			if member.MemberState != innodbcluster.MemberStateOnline {
				log.Printf("Primary (%s) is not ONLINE. Starting full cluster crash recovery...", member.Address)

				if err := handleFullClusterCrash(ctx, mysqlshVer); err != nil {
					return errors.Wrap(err, "handle full cluster crash")
				}

				// force restart container
				os.Exit(1)
			}
		}
	}
	log.Printf("Primary is %s", primary)

	member, ok := status.DefaultReplicaSet.Topology[fmt.Sprintf("%s:%d", localShell.host, 3306)]
	if !ok {
		recoveryMethod, err := getRecoveryMethod(ctx, primary, localShell.host, mysqlshVer)
		if err != nil {
			return err
		}

		log.Printf("Adding instance (%s) to InnoDB cluster using %s recovery", localShell.host, recoveryMethod)

		if err := shell.addInstance(ctx, localShell.getURI(), recoveryMethod); err != nil {
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
	}

	if err := updateGroupPeers(ctx, peers, mysqlshVer); err != nil {
		return err
	}

	if rescanNeeded {
		err := retry.OnError(retry.DefaultBackoff, func(err error) bool {
			return strings.Contains(err.Error(), "Another operation requiring access to the member is still in progress")
		}, func() error {
			return shell.rescanCluster(ctx)
		})
		if err != nil {
			return err
		}

		log.Println("Cluster rescanned")
	}

	return nil
}
