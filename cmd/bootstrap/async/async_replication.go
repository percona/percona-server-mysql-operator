package async

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	database "github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	mysqldb "github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func Bootstrap(ctx context.Context) error {
	timer := stopwatch.NewNamedStopwatch()
	err := timer.AddMany([]string{"clone", "total"})
	if err != nil {
		return errors.Wrap(err, "add timers")
	}
	timer.Start("total")

	defer func() {
		timer.Stop("total")
		log.Printf("bootstrap finished in %f seconds", timer.ElapsedSeconds("total"))
	}()

	svc := os.Getenv("SERVICE_NAME_UNREADY")
	mysqlSvc := os.Getenv("SERVICE_NAME")
	peers, err := utils.Lookup(svc)
	if err != nil {
		return errors.Wrap(err, "lookup")
	}
	log.Printf("Peers: %v", sets.List(peers))

	fqdn, err := utils.GetFQDN(mysqlSvc)
	if err != nil {
		return errors.Wrap(err, "get FQDN")
	}
	log.Printf("FQDN: %s", fqdn)

	primary, replicas, err := getTopology(ctx, fqdn, peers)
	if err != nil {
		return errors.Wrap(err, "select donor")
	}
	log.Printf("Primary: %s Replicas: %v", primary, replicas)

	podHostname, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get hostname")
	}

	podIp, err := utils.GetPodIP(podHostname)
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}
	log.Printf("PodIP: %s", podIp)

	primaryIp, err := utils.GetPodIP(primary)
	if err != nil {
		return errors.Wrap(err, "get primary IP")
	}
	log.Printf("PrimaryIP: %s", primaryIp)

	donor, err := selectDonor(ctx, fqdn, primary, replicas)
	if err != nil {
		return errors.Wrap(err, "select donor")
	}
	log.Printf("Donor: %s", donor)

	log.Printf("Opening connection to %s", podIp)
	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1.UserOperator)
	}

	params := database.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: podIp,
	}
	readTimeout, err := utils.GetReadTimeout()
	if err != nil {
		return errors.Wrap(err, "get read timeout")
	}
	params.ReadTimeoutSeconds = readTimeout

	cloneTimeout, err := utils.GetCloneTimeout()
	if err != nil {
		return errors.Wrap(err, "get clone timeout")
	}
	params.CloneTimeoutSeconds = cloneTimeout

	sourceRetryCount, err := utils.GetSourceRetryCount()
	if err != nil {
		return errors.Wrap(err, "get source retry count")
	}
	params.SourceRetryCount = sourceRetryCount

	sourceConnectRetry, err := utils.GetSourceConnectRetry()
	if err != nil {
		return errors.Wrap(err, "get source connect retry")
	}
	params.SourceConnectRetry = sourceConnectRetry

	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return errors.Wrap(err, "connect to database")
	}
	defer db.Close()

	if err := db.StopReplication(ctx); err != nil {
		return err
	}

	readOnly, err := db.IsReadonly(ctx)
	if err != nil {
		return errors.Wrap(err, "check read only status")
	}

	switch {
	case !readOnly:
		// A writable node is the primary, whatever the topology guess says.
		if err := db.ResetReplication(ctx); err != nil {
			return err
		}

		log.Printf("I'm writable and therefore the primary.")
		return nil
	case donor == "":
		if err := db.ResetReplication(ctx); err != nil {
			return err
		}

		log.Printf("Can't find a donor, we're on our own.")
		return nil
	case donor == fqdn:
		if err := db.ResetReplication(ctx); err != nil {
			return err
		}

		log.Printf("I'm the donor and therefore the primary.")
		return nil
	case primary == fqdn || primaryIp == podIp:
		if err := db.ResetReplication(ctx); err != nil {
			return err
		}

		log.Printf("I'm the primary.")
		return nil
	}

	cloneLock := filepath.Join(mysql.DataMountPath, "clone.lock")
	requireClone, err := isCloneRequired(cloneLock)
	if err != nil {
		return errors.Wrap(err, "check if clone is required")
	}

	if requireClone {
		// Never clone over a datadir that already executed transactions:
		// a former primary returning after a failover has no clone.lock but
		// may hold writes that were never replicated; cloning destroys them.
		gtidExecuted, err := db.GetGTIDExecuted(ctx)
		if err != nil {
			return errors.Wrap(err, "get gtid_executed")
		}
		if gtidExecuted != "" {
			log.Printf("Datadir has executed GTIDs (%s), skipping clone to preserve local data", gtidExecuted)
			requireClone = false

			if err := createCloneLock(cloneLock); err != nil {
				return errors.Wrap(err, "create clone lock")
			}
		}
	}

	log.Printf("Clone required: %t", requireClone)
	if requireClone {
		log.Println("Checking if a clone in progress")
		inProgress, err := db.CloneInProgress(ctx)
		if err != nil {
			return errors.Wrap(err, "check if a clone in progress")
		}

		log.Printf("Clone in progress: %t", inProgress)
		if inProgress {
			return nil
		}

		if err := db.DisableSuperReadonly(ctx); err != nil {
			return errors.Wrap(err, "disable super read only")
		}

		timer.Start("clone")
		log.Printf("Cloning from %s", donor)
		err = db.Clone(ctx, donor, string(apiv1.UserOperator), operatorPass, mysql.DefaultAdminPort, params.CloneTimeoutSeconds)
		timer.Stop("clone")
		if err != nil && !errors.Is(err, database.ErrRestartAfterClone) {
			return errors.Wrapf(err, "clone from donor %s", donor)
		}

		err = createCloneLock(cloneLock)
		if err != nil {
			return errors.Wrap(err, "create clone lock")
		}

		log.Println("Clone finished. Restarting container...")

		// We return with 1 to restart container
		os.Exit(1)
	}

	if !requireClone {
		if err := deleteCloneLock(cloneLock); err != nil {
			return errors.Wrap(err, "delete clone lock")
		}
	}

	rStatus, _, err := db.ReplicationStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "check replication status")
	}

	if rStatus == mysqldb.ReplicationStatusNotInitiated || rStatus == mysqldb.ReplicationStatusStopped {
		// Joining is only safe when this member has no transactions the
		// primary doesn't know about.
		errant, err := errantGTIDs(ctx, db, primaryIp)
		if err != nil {
			return errors.Wrap(err, "check errant GTIDs")
		}
		if errant != "" {
			// Quarantine only against a confirmed (writable) primary. After
			// a full cluster restart nobody is writable and the topology
			// guess is unreliable; leave the member unjoined and let the
			// operator resolve it.
			primaryWritable, err := isPrimaryWritable(ctx, primaryIp)
			if err != nil {
				return errors.Wrapf(err, "check if primary %s is writable", primary)
			}
			if !primaryWritable {
				log.Printf("Local transactions not present on presumed primary %s (errant GTIDs: %s), "+
					"but the presumed primary is not writable; leaving the member unjoined for the operator to resolve", primary, errant)
				if err := db.EnableSuperReadonly(ctx); err != nil {
					return errors.Wrap(err, "enable super read only")
				}
				return nil
			}

			log.Printf("QUARANTINE: local transactions not present on primary %s (errant GTIDs: %s); refusing to join the cluster", primary, errant)
			if err := os.WriteFile(mysql.QuarantineFile, []byte(errant+"\n"), 0o640); err != nil {
				return errors.Wrap(err, "create quarantine file")
			}
			if err := db.EnableSuperReadonly(ctx); err != nil {
				return errors.Wrap(err, "enable super read only")
			}
			return nil
		}

		log.Println("configuring replication")

		replicaPass, err := utils.GetSecret(apiv1.UserReplication)
		if err != nil {
			return errors.Wrapf(err, "get %s password", apiv1.UserReplication)
		}

		if err := db.StopReplication(ctx); err != nil {
			return errors.Wrap(err, "stop replication")
		}

		if err := db.StartReplication(ctx, primary, replicaPass, mysql.DefaultPort, params.SourceRetryCount, params.SourceConnectRetry); err != nil {
			return errors.Wrap(err, "start replication")
		}
	}

	// The member is joined: drop a stale quarantine marker if any.
	if err := os.Remove(mysql.QuarantineFile); err != nil && !os.IsNotExist(err) {
		return errors.Wrap(err, "remove quarantine file")
	}

	if err := db.EnableSuperReadonly(ctx); err != nil {
		return errors.Wrap(err, "enable super read only")
	}

	return nil
}

// isPrimaryWritable reports whether the node at primaryIp accepts writes.
func isPrimaryWritable(ctx context.Context, primaryIp string) (bool, error) {
	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return false, errors.Wrapf(err, "get %s password", apiv1.UserOperator)
	}
	readTimeout, err := utils.GetReadTimeout()
	if err != nil {
		return false, errors.Wrap(err, "get read timeout")
	}
	primaryDB, err := database.NewDatabase(ctx, database.DBParams{
		User:               apiv1.UserOperator,
		Pass:               operatorPass,
		Host:               primaryIp,
		ReadTimeoutSeconds: readTimeout,
	})
	if err != nil {
		return false, errors.Wrapf(err, "connect to primary %s", primaryIp)
	}
	defer primaryDB.Close()

	readOnly, err := primaryDB.IsReadonly(ctx)
	if err != nil {
		return false, errors.Wrap(err, "check primary read only status")
	}
	return !readOnly, nil
}

// errantGTIDs returns the GTIDs executed locally but absent on the primary.
func errantGTIDs(ctx context.Context, localDB *database.DB, primaryIp string) (string, error) {
	localGTIDs, err := localDB.GetGTIDExecuted(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get local gtid_executed")
	}
	if localGTIDs == "" {
		return "", nil
	}

	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return "", errors.Wrapf(err, "get %s password", apiv1.UserOperator)
	}
	readTimeout, err := utils.GetReadTimeout()
	if err != nil {
		return "", errors.Wrap(err, "get read timeout")
	}
	primaryDB, err := database.NewDatabase(ctx, database.DBParams{
		User:               apiv1.UserOperator,
		Pass:               operatorPass,
		Host:               primaryIp,
		ReadTimeoutSeconds: readTimeout,
	})
	if err != nil {
		return "", errors.Wrapf(err, "connect to primary %s", primaryIp)
	}
	defer primaryDB.Close()

	primaryGTIDs, err := primaryDB.GetGTIDExecuted(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get primary gtid_executed")
	}

	return localDB.GTIDSubtract(ctx, localGTIDs, primaryGTIDs)
}

func getTopology(ctx context.Context, fqdn string, peers sets.Set[string]) (string, []string, error) {
	replicas := sets.New[string]()
	primary := ""
	stoppedSource := ""

	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return "", nil, errors.Wrapf(err, "get %s password", apiv1.UserOperator)
	}

	for _, peer := range sets.List(peers) {
		params := database.DBParams{
			User: apiv1.UserOperator,
			Pass: operatorPass,
			Host: peer,
		}
		readTimeout, err := utils.GetReadTimeout()
		if err != nil {
			return "", nil, errors.Wrap(err, "get read timeout")
		}
		params.ReadTimeoutSeconds = readTimeout

		db, err := database.NewDatabase(ctx, params)
		if err != nil {
			return "", nil, errors.Wrapf(err, "connect to %s", peer)
		}
		defer db.Close()

		status, source, err := db.ReplicationStatus(ctx)
		if err != nil {
			return "", nil, errors.Wrap(err, "check replication status")
		}

		replicaHost, err := db.ReportHost(ctx)
		if err != nil {
			return "", nil, errors.Wrap(err, "get report_host")
		}
		if replicaHost == "" {
			continue
		}
		replicas.Insert(replicaHost)

		if status == mysqldb.ReplicationStatusActive {
			primary = source
		} else if status == mysqldb.ReplicationStatusStopped && source != "" && source != replicaHost {
			stoppedSource = source
		}
	}

	if primary == "" && stoppedSource != "" {
		// A stopped channel remembers its source — the primary. Honor the
		// hint only if the source resolves: after a pause/resume the lowest
		// ordinal boots alone and its channel points at a peer that does not
		// exist yet.
		if _, err := utils.GetPodIP(stoppedSource); err == nil {
			log.Printf("No active replication, using stopped channel source as primary: %s", stoppedSource)
			primary = stoppedSource
		} else {
			log.Printf("Stopped channel source %s does not resolve, ignoring the hint", stoppedSource)
		}
	}

	if primary == "" && peers.Len() == 1 {
		primary = sets.List(peers)[0]
	} else if primary == "" {
		for _, r := range sets.List(replicas) {
			// We should set primary to the first replica, which is not the bootstrapped pod.
			// The bootstrapped pod can't be a primary.
			// Even if it was a primary before, orchestrator will promote another replica "as result of DeadMaster".
			if r != fqdn {
				primary = r
				break
			}
		}
	}

	if replicas.Len() > 0 {
		replicas.Delete(primary)
	}

	return primary, sets.List(replicas), nil
}

func selectDonor(ctx context.Context, fqdn, primary string, replicas []string) (string, error) {
	donor := ""

	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return "", errors.Wrapf(err, "get %s password", apiv1.UserOperator)
	}

	for _, replica := range replicas {
		params := database.DBParams{
			User: apiv1.UserOperator,
			Pass: operatorPass,
			Host: replica,
		}
		readTimeout, err := utils.GetReadTimeout()
		if err != nil {
			return "", errors.Wrap(err, "get read timeout")
		}
		params.ReadTimeoutSeconds = readTimeout

		db, err := database.NewDatabase(ctx, params)
		if err != nil {
			continue
		}
		db.Close()

		if fqdn != replica {
			donor = replica
			break
		}
	}

	if donor == "" && fqdn != primary {
		donor = primary
	}

	return donor, nil
}

func isCloneRequired(file string) (bool, error) {
	_, err := os.Stat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, errors.Wrapf(err, "stat %s", file)
	}

	return false, nil
}

func createCloneLock(file string) error {
	_, err := os.Create(file)
	return errors.Wrapf(err, "create %s", file)
}

func deleteCloneLock(file string) error {
	err := os.Remove(file)
	return errors.Wrapf(err, "remove %s", file)
}
