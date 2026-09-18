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

	primaryIp := ""
	if primary != fqdn {
		primaryIp, err = utils.GetPodIP(primary)
		if err != nil {
			return errors.Wrap(err, "get primary IP")
		}
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

	switch {
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

	donorExecuted, donorPurged, err := donorGTIDs(ctx, donor, operatorPass)
	if err != nil {
		return errors.Wrapf(err, "get GTID sets from donor %s", donor)
	}

	localExecuted, err := db.GetGTIDExecuted(ctx)
	if err != nil {
		return errors.Wrap(err, "get local GTID_EXECUTED")
	}

	requireClone, err := cloneRequired(ctx, db, localExecuted, donorExecuted, donorPurged)
	if err != nil {
		return err
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

	if err := db.EnableSuperReadonly(ctx); err != nil {
		return errors.Wrap(err, "enable super read only")
	}

	return nil
}

func getTopology(ctx context.Context, fqdn string, peers sets.Set[string]) (string, []string, error) {
	replicas := sets.New[string]()
	gtids := make(map[string]string)
	primary := ""

	var subtractor gtidSubtractor

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

		if subtractor == nil {
			subtractor = db
		}

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

		gtid, err := db.GetGTIDExecuted(ctx)
		if err != nil {
			return "", nil, errors.Wrapf(err, "get GTID_EXECUTED from %s", peer)
		}
		gtids[replicaHost] = gtid
		log.Printf("Peer %s GTIDExecuted=%s", replicaHost, gtid)

		if status == mysqldb.ReplicationStatusActive {
			primary = source
		}
	}

	if primary == "" && peers.Len() == 1 {
		primary = sets.List(peers)[0]
	} else if primary == "" {
		primary, err = electPrimary(ctx, subtractor, gtids)
		if err != nil {
			return "", nil, err
		}

		// The peers hold the same transactions, so there is nothing to lose
		// whichever way round we point replication. Prefer another pod: ours has
		// just started and is the one asking.
		if primary == "" {
			for _, r := range sets.List(replicas) {
				if r != fqdn {
					primary = r
					break
				}
			}
		}
	}

	if replicas.Len() > 0 {
		replicas.Delete(primary)
	}

	donors, err := orderDonors(ctx, subtractor, sets.List(replicas), fqdn, gtids)
	if err != nil {
		return "", nil, err
	}

	return primary, donors, nil
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

var (
	errAheadOfDonor  = errors.New("local data is ahead of the donor")
	errDivergedPeers = errors.New("peers have diverged")
)

// orderDonors puts the replica worth cloning from first: the one holding the most transactions.
func orderDonors(ctx context.Context, s gtidSubtractor, replicas []string, fqdn string, gtids map[string]string) ([]string, error) {
	local := gtids[fqdn]

	rest := make([]string, 0, len(replicas))
	for _, replica := range replicas {
		missing, err := s.GTIDSubtract(ctx, local, gtids[replica])
		if err != nil {
			return nil, errors.Wrapf(err, "compare %s against the local GTID set", replica)
		}
		if missing != "" {
			log.Printf("Not a donor: %s is missing %s", replica, missing)
			continue
		}

		rest = append(rest, replica)
	}

	ordered := make([]string, 0, len(rest))

	for len(rest) > 0 {
		best := 0

		for i := 1; i < len(rest); i++ {
			extra, err := s.GTIDSubtract(ctx, gtids[rest[i]], gtids[rest[best]])
			if err != nil {
				return nil, errors.Wrapf(err, "compare %s against %s", rest[i], rest[best])
			}
			if extra == "" {
				continue
			}

			// Only overtake a replica we hold nothing over, so replicas that have
			// each gone their own way keep the order they came in.
			short, err := s.GTIDSubtract(ctx, gtids[rest[best]], gtids[rest[i]])
			if err != nil {
				return nil, errors.Wrapf(err, "compare %s against %s", rest[best], rest[i])
			}
			if short == "" {
				best = i
			}
		}

		ordered = append(ordered, rest[best])
		rest = append(rest[:best], rest[best+1:]...)
	}

	return ordered, nil
}

// electPrimary picks the peer whose executed GTID set holds every other peer's.
func electPrimary(ctx context.Context, s gtidSubtractor, gtids map[string]string) (string, error) {
	if len(gtids) == 0 {
		return "", nil
	}

	complete := make([]string, 0, len(gtids))

	for _, candidate := range sets.List(sets.KeySet(gtids)) {
		holdsAll := true

		for peer, gtid := range gtids {
			if peer == candidate {
				continue
			}

			missing, err := s.GTIDSubtract(ctx, gtid, gtids[candidate])
			if err != nil {
				return "", errors.Wrapf(err, "compare GTID sets of %s and %s", peer, candidate)
			}
			if missing != "" {
				holdsAll = false
				break
			}
		}

		if holdsAll {
			complete = append(complete, candidate)
		}
	}

	switch len(complete) {
	case 0:
		return "", errors.Wrapf(errDivergedPeers, "none of %v holds every transaction", sets.List(sets.KeySet(gtids)))
	case 1:
		return complete[0], nil
	default:
		return "", nil
	}
}

type gtidSubtractor interface {
	GTIDSubtract(ctx context.Context, set, other string) (string, error)
}

// cloneRequired reports whether the local data directory has to be re-provisioned
// from the donor before it can replicate.
func cloneRequired(ctx context.Context, s gtidSubtractor, local, donorExecuted, donorPurged string) (bool, error) {
	ahead, err := s.GTIDSubtract(ctx, local, donorExecuted)
	if err != nil {
		return false, errors.Wrap(err, "compare local GTID set against the donor")
	}
	if ahead != "" {
		// CLONE INSTANCE drops all user data. These transactions are on no other
		// node, so cloning is the thing that would lose them.
		return false, errors.Wrapf(errAheadOfDonor, "donor is missing %s", ahead)
	}

	missing, err := s.GTIDSubtract(ctx, donorPurged, local)
	if err != nil {
		return false, errors.Wrap(err, "compare donor's purged GTID set against the local one")
	}

	// The donor no longer has the binary logs we would need to catch up.
	return missing != "", nil
}

func donorGTIDs(ctx context.Context, donor, operatorPass string) (string, string, error) {
	params := database.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: donor,
	}
	readTimeout, err := utils.GetReadTimeout()
	if err != nil {
		return "", "", errors.Wrap(err, "get read timeout")
	}
	params.ReadTimeoutSeconds = readTimeout

	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return "", "", errors.Wrapf(err, "connect to %s", donor)
	}
	defer db.Close()

	executed, err := db.GetGTIDExecuted(ctx)
	if err != nil {
		return "", "", err
	}

	purged, err := db.GetGTIDPurged(ctx)
	if err != nil {
		return "", "", err
	}

	return executed, purged, nil
}

func createCloneLock(file string) error {
	_, err := os.Create(file)
	return errors.Wrapf(err, "create %s", file)
}

func deleteCloneLock(file string) error {
	err := os.Remove(file)
	if os.IsNotExist(err) {
		return nil
	}

	return errors.Wrapf(err, "remove %s", file)
}
