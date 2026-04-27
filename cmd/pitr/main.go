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
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

// Database defines MySQL operations needed by the PITR implementations.
// The binlog-replay method only uses GetGTIDExecuted; the replication method
// uses the full set.
type Database interface {
	ChangeReplicationSourceRelay(ctx context.Context, relayLogFile string, relayLogPos int, channel string) error
	ChangeReplicationFilterIgnoreDB(ctx context.Context, dbs []string, channel string) error
	StartReplicaUntilGTID(ctx context.Context, gtid string, channel string) error
	WaitReplicaSQLThreadStop(ctx context.Context, pollInterval time.Duration, channel string) error
	StopReplication(ctx context.Context, channel string) error
	ResetReplication(ctx context.Context, channel string) error
	SetGTIDNextAutomatic(ctx context.Context) error
	GetGTIDExecuted(ctx context.Context) (string, error)
	Close() error
}

// pitrChannelName is the dedicated replication channel used by the PITR
// recovery flow. Isolating recovery to its own channel keeps it independent
// of any pre-existing replication state on the default channel.
const pitrChannelName = "pitr"

// pitrIgnoreDBs lists databases whose events the recovery SQL thread must
// skip. mysql_innodb_cluster_metadata is owned by the InnoDB Cluster /
// Group Replication metadata layer and replaying its binlog events on a
// freshly restored cluster would corrupt the local metadata.
var pitrIgnoreDBs = []string{"mysql_innodb_cluster_metadata"}

type newStorageFn func(ctx context.Context, endpoint, accessKey, secretKey, bucket, prefix, region string, verifyTLS bool) (storage.Storage, error)
type newDatabaseFn func(ctx context.Context, params db.DBParams) (Database, error)

// getObjectFn fetches a single object by key and returns a streaming reader.
type getObjectFn func(ctx context.Context, objectKey string) (io.ReadCloser, error)

// applyBinlogsFn starts a single mysql client process and for each object key
// fetches the binlog via getObject and streams it through mysqlbinlog into mysql.
type applyBinlogsFn func(ctx context.Context, objectKeys []string, getObject getObjectFn, mysqlbinlogArgs []string, mysqlArgs []string, mysqlPass string) error

type logWriter struct{}

func (lw *logWriter) Write(bs []byte) (int, error) {
	return fmt.Print(time.Now().UTC().Format(time.RFC3339Nano), " 0 [Info] [K8SPS-642] [Recovery] ", string(bs))
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: pitr <replay|apply>")
	}

	ctx := context.Background()

	log.SetFlags(0)
	log.SetOutput(new(logWriter))

	newDB := func(ctx context.Context, params db.DBParams) (Database, error) {
		return db.NewDatabase(ctx, params)
	}

	switch os.Args[1] {
	case "replay":
		if err := runReplay(ctx, storage.NewS3, newDB, utils.GetSecret, applyBinlogs); err != nil {
			log.Fatalf("replay failed: %v", err)
		}
	case "apply":
		if err := runApply(ctx, storage.NewS3, newDB, utils.GetSecret, getLatestGTIDByDatetime, "/var/lib/mysql"); err != nil {
			log.Fatalf("apply failed: %v", err)
		}
	default:
		log.Fatalf("unknown subcommand: %s", os.Args[1])
	}
}

// runReplay implements the binlog-replay PITR method: it streams each binlog
// from S3 through `mysqlbinlog` into a single `mysql` client.
func runReplay(ctx context.Context, newS3 newStorageFn, newDB newDatabaseFn, getSecret func(apiv1.SystemUser) (string, error), apply applyBinlogsFn) error {
	binlogsPath := os.Getenv("BINLOGS_PATH")
	if binlogsPath == "" {
		return fmt.Errorf("BINLOGS_PATH is not set")
	}

	data, err := os.ReadFile(binlogsPath)
	if err != nil {
		return fmt.Errorf("read binlogs file: %w", err)
	}

	var entries []binlogserver.BinlogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse binlogs json: %w", err)
	}

	if len(entries) == 0 {
		return fmt.Errorf("no binlog entries found")
	}

	pitrType := os.Getenv("PITR_TYPE")
	pitrDate := os.Getenv("PITR_DATE")
	pitrGTID := os.Getenv("PITR_GTID")

	operatorPass, err := getSecret(apiv1.UserOperator)
	if err != nil {
		return fmt.Errorf("get operator password: %w", err)
	}

	database, err := newDB(ctx, db.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: "127.0.0.1",
	})
	if err != nil {
		return fmt.Errorf("connect to MySQL: %w", err)
	}

	gtidExecuted, err := database.GetGTIDExecuted(ctx)
	if err != nil {
		if closeErr := database.Close(); closeErr != nil {
			log.Printf("close database: %v", closeErr)
		}
		return fmt.Errorf("get GTID_EXECUTED: %w", err)
	}
	log.Printf("GTID_EXECUTED from backup: %s", gtidExecuted)
	if err := database.Close(); err != nil {
		log.Printf("close database: %v", err)
	}

	endpoint := os.Getenv("AWS_ENDPOINT")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	region := os.Getenv("AWS_DEFAULT_REGION")
	bucket := os.Getenv("S3_BUCKET")
	verifyTLS := os.Getenv("VERIFY_TLS") != "false"

	s3Client, err := newS3(ctx, endpoint, accessKey, secretKey, bucket, "", region, verifyTLS)
	if err != nil {
		return fmt.Errorf("create S3 client: %w", err)
	}

	var objectKeys []string
	for _, entry := range entries {
		objectKey, err := objectKeyFromURI(entry.URI, bucket)
		if err != nil {
			return fmt.Errorf("parse URI %s: %w", entry.URI, err)
		}
		objectKeys = append(objectKeys, objectKey)
	}

	mysqlbinlogArgs := []string{"--disable-log-bin"}
	if gtidExecuted != "" {
		mysqlbinlogArgs = append(mysqlbinlogArgs, fmt.Sprintf("--exclude-gtids=%s", gtidExecuted))
	}

	switch pitrType {
	case "date":
		mysqlbinlogArgs = append(mysqlbinlogArgs, fmt.Sprintf("--stop-datetime=%s", pitrDate))
	case "gtid":
		mysqlbinlogArgs = append(mysqlbinlogArgs, fmt.Sprintf("--include-gtids=%s", pitrGTID))
	default:
		return fmt.Errorf("unknown PITR_TYPE: %s", pitrType)
	}

	mysqlArgs := []string{
		"-u", string(apiv1.UserOperator),
		"-h", "127.0.0.1",
		"-P", "33062",
	}
	if os.Getenv("PITR_FORCE") == "true" {
		log.Println("Running mysql with --force, SQL errors will be ignored!!!")
		mysqlArgs = append([]string{"--force"}, mysqlArgs...)
	}

	log.Printf("applying %d binlog(s) with mysqlbinlog args: %v", len(objectKeys), mysqlbinlogArgs)

	if err := apply(ctx, objectKeys, s3Client.GetObject, mysqlbinlogArgs, mysqlArgs, operatorPass); err != nil {
		return fmt.Errorf("apply binlogs: %w", err)
	}

	database, err = newDB(ctx, db.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: "127.0.0.1",
	})
	if err != nil {
		return fmt.Errorf("reconnect to MySQL: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Printf("close db connection: %v", err)
		}
	}()

	gtidExecuted, err = database.GetGTIDExecuted(ctx)
	if err != nil {
		return fmt.Errorf("get GTID_EXECUTED after restore: %w", err)
	}
	log.Printf("GTID_EXECUTED after PITR: %s", gtidExecuted)

	log.Println("PITR complete")
	return nil
}

// applyBinlogs starts a single mysql client and for each object key
// fetches the binlog from storage and streams it through mysqlbinlog into mysql.
func applyBinlogs(ctx context.Context, objectKeys []string, getObject getObjectFn, mysqlbinlogArgs []string, mysqlArgs []string, mysqlPass string) error {
	mysqlCmd := exec.CommandContext(ctx, "mysql", mysqlArgs...)
	mysqlCmd.Env = append(os.Environ(), fmt.Sprintf("MYSQL_PWD=%s", mysqlPass))
	mysqlStdin, err := mysqlCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create mysql stdin pipe: %w", err)
	}

	var mysqlStderr bytes.Buffer
	mysqlCmd.Stderr = &mysqlStderr

	if err := mysqlCmd.Start(); err != nil {
		return fmt.Errorf("start mysql: %w", err)
	}

	for _, objectKey := range objectKeys {
		log.Printf("streaming binlog %s", objectKey)

		obj, err := getObject(ctx, objectKey)
		if err != nil {
			if closeErr := mysqlStdin.Close(); closeErr != nil {
				log.Printf("close mysql stdin: %v", closeErr)
			}
			if waitErr := mysqlCmd.Wait(); waitErr != nil {
				log.Printf("wait for mysql: %v", waitErr)
			}
			return fmt.Errorf("fetch binlog %s: %w", objectKey, err)
		}

		args := append(mysqlbinlogArgs, "-")
		binlogCmd := exec.CommandContext(ctx, "mysqlbinlog", args...)
		binlogCmd.Stdin = obj

		var binlogStderr bytes.Buffer
		binlogCmd.Stdout = mysqlStdin
		binlogCmd.Stderr = &binlogStderr

		if err := binlogCmd.Run(); err != nil {
			if closeErr := obj.Close(); closeErr != nil {
				log.Printf("close object %s: %v", objectKey, closeErr)
			}
			if closeErr := mysqlStdin.Close(); closeErr != nil {
				log.Printf("close mysql stdin: %v", closeErr)
			}
			if waitErr := mysqlCmd.Wait(); waitErr != nil {
				log.Printf("wait for mysql: %v", waitErr)
			}
			return fmt.Errorf("mysqlbinlog %s failed: %w, stderr: %s", objectKey, err, binlogStderr.String())
		}
		if err := obj.Close(); err != nil {
			log.Printf("close object %s: %v", objectKey, err)
		}
	}

	if err := mysqlStdin.Close(); err != nil {
		log.Printf("close mysql stdin: %v", err)
	}

	if err := mysqlCmd.Wait(); err != nil {
		return fmt.Errorf("mysql failed: %w, stderr: %s", err, mysqlStderr.String())
	}

	return nil
}

// runApply drives the replication PITR method: it issues CHANGE REPLICATION
// SOURCE first, then downloads binlogs into the MySQL data directory as relay
// logs, then starts the SQL thread until the target GTID.
//
// The order matters: CHANGE REPLICATION SOURCE must run before the binlogs
// land on disk. If Retrieved_Gtid_Set is non-empty when CRS executes, MySQL
// rewrites existing relay log files, corrupting any binlogs we already placed.
// Running CRS first against a not-yet-existing relay log file leaves only
// metadata to update; the actual files we write afterwards are untouched
// until START REPLICA reads them.
func runApply(ctx context.Context, newS3 newStorageFn, newDB newDatabaseFn, getSecret func(apiv1.SystemUser) (string, error), getGTIDByDatetime func(string, string) (string, error), mysqlDir string) error {
	binlogsPath := os.Getenv("BINLOGS_PATH")
	if binlogsPath == "" {
		return fmt.Errorf("BINLOGS_PATH is not set")
	}

	data, err := os.ReadFile(binlogsPath)
	if err != nil {
		return fmt.Errorf("read binlogs file: %w", err)
	}

	var entries []binlogserver.BinlogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse binlogs json: %w", err)
	}

	if len(entries) == 0 {
		return fmt.Errorf("no binlog entries found")
	}

	pitrType := os.Getenv("PITR_TYPE")
	pitrDate := os.Getenv("PITR_DATE")
	pitrGTID := os.Getenv("PITR_GTID")

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("get hostname: %w", err)
	}

	operatorPass, err := getSecret(apiv1.UserOperator)
	if err != nil {
		return fmt.Errorf("get operator password: %w", err)
	}

	database, err := newDB(ctx, db.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: "127.0.0.1",
	})
	if err != nil {
		return fmt.Errorf("connect to MySQL: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()

	currentGTID, err := database.GetGTIDExecuted(ctx)
	if err != nil {
		return fmt.Errorf("get current GTID_EXECUTED: %w", err)
	}
	log.Printf("GTID_EXECUTED: %s", currentGTID)

	firstRelayLog := fmt.Sprintf("%s-relay-bin.000001", hostname)
	log.Printf("CHANGE REPLICATION SOURCE TO RELAY_LOG_FILE='%s', RELAY_LOG_POS=%d, SOURCE_HOST='dummy' FOR CHANNEL '%s'", firstRelayLog, 4, pitrChannelName)
	if err := database.ChangeReplicationSourceRelay(ctx, firstRelayLog, 4, pitrChannelName); err != nil {
		return fmt.Errorf("change replication source: %w", err)
	}

	log.Printf("CHANGE REPLICATION FILTER REPLICATE_IGNORE_DB = (%s) FOR CHANNEL '%s'", strings.Join(pitrIgnoreDBs, ", "), pitrChannelName)
	if err := database.ChangeReplicationFilterIgnoreDB(ctx, pitrIgnoreDBs, pitrChannelName); err != nil {
		return fmt.Errorf("change replication filter: %w", err)
	}

	if err := downloadRelayLogs(ctx, newS3, entries, mysqlDir, hostname); err != nil {
		return err
	}

	if pitrType == "date" {
		lastRelayLog := fmt.Sprintf("%s-relay-bin.%06d", hostname, len(entries))
		lastRelayLogPath := filepath.Join(mysqlDir, lastRelayLog)
		pitrGTID, err = getGTIDByDatetime(lastRelayLogPath, pitrDate)
		if err != nil {
			return fmt.Errorf("get latest GTID for date %s: %w", pitrDate, err)
		}
		log.Printf("latest GTID for date %s: %s", pitrDate, pitrGTID)
	}

	log.Printf("START REPLICA SQL_THREAD UNTIL SQL_AFTER_GTIDS='%s' FOR CHANNEL '%s'", pitrGTID, pitrChannelName)
	if err := database.StartReplicaUntilGTID(ctx, pitrGTID, pitrChannelName); err != nil {
		return fmt.Errorf("start replica until GTID: %w", err)
	}

	log.Println("waiting for replication to complete...")
	if err := database.WaitReplicaSQLThreadStop(ctx, time.Second, pitrChannelName); err != nil {
		return fmt.Errorf("wait for replication: %w", err)
	}

	log.Println("stopping replication")
	if err := database.StopReplication(ctx, pitrChannelName); err != nil {
		return errors.Wrap(err, "stop replication")
	}

	log.Println("running 'RESET REPLICA ALL'")
	if err := database.ResetReplication(ctx, pitrChannelName); err != nil {
		return errors.Wrap(err, "reset replication")
	}

	currentGTID, err = database.GetGTIDExecuted(ctx)
	if err != nil {
		return fmt.Errorf("get GTID_EXECUTED after restore: %w", err)
	}
	log.Printf("GTID_EXECUTED: %s", currentGTID)

	log.Println("setting GTID_NEXT to AUTOMATIC")
	if err := database.SetGTIDNextAutomatic(ctx); err != nil {
		return fmt.Errorf("set GTID_NEXT to AUTOMATIC: %w", err)
	}

	log.Println("PITR apply complete")
	return nil
}

// downloadRelayLogs streams every binlog from S3 into the MySQL data directory
// as a relay log file and writes the accompanying relay-bin.index.
func downloadRelayLogs(ctx context.Context, newS3 newStorageFn, entries []binlogserver.BinlogEntry, mysqlDir, hostname string) error {
	endpoint := os.Getenv("AWS_ENDPOINT")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	region := os.Getenv("AWS_DEFAULT_REGION")
	bucket := os.Getenv("S3_BUCKET")
	verifyTLS := os.Getenv("VERIFY_TLS") != "false"

	s3Client, err := newS3(ctx, endpoint, accessKey, secretKey, bucket, "", region, verifyTLS)
	if err != nil {
		return fmt.Errorf("create S3 client: %w", err)
	}

	relayLogFiles := make([]string, 0, len(entries))
	for i, entry := range entries {
		relayLogName := fmt.Sprintf("%s-relay-bin.%06d", hostname, i+1)
		relayLogPath := filepath.Join(mysqlDir, relayLogName)

		objectKey, err := objectKeyFromURI(entry.URI, bucket)
		if err != nil {
			return fmt.Errorf("parse URI %s: %w", entry.URI, err)
		}

		log.Printf("downloading binlog %s to %s", objectKey, relayLogPath)

		obj, err := s3Client.GetObject(ctx, objectKey)
		if err != nil {
			return fmt.Errorf("download binlog %s: %w", entry.URI, err)
		}

		f, err := os.Create(relayLogPath)
		if err != nil {
			if closeErr := obj.Close(); closeErr != nil {
				log.Printf("close object %s: %v", entry.URI, closeErr)
			}
			return fmt.Errorf("create relay log file %s: %w", relayLogPath, err)
		}

		_, err = io.Copy(f, obj)
		if closeErr := obj.Close(); closeErr != nil {
			log.Printf("close object %s: %v", entry.URI, closeErr)
		}
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("close relay log file %s: %v", relayLogPath, closeErr)
		}
		if err != nil {
			return fmt.Errorf("write relay log file %s: %w", relayLogPath, err)
		}

		relayLogFiles = append(relayLogFiles, "./"+relayLogName)
	}

	indexPath := filepath.Join(mysqlDir, fmt.Sprintf("%s-relay-bin.index", hostname))
	indexContent := strings.Join(relayLogFiles, "\n") + "\n"
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		return fmt.Errorf("write relay log index: %w", err)
	}

	log.Printf("downloaded %d relay log files", len(relayLogFiles))
	return nil
}

func getLatestGTIDByDatetime(relayLogPath, startDatetime string) (string, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", startDatetime, time.UTC)
	if err != nil {
		return "", fmt.Errorf("parse datetime %q: %w", startDatetime, err)
	}
	stopDatetime := t.Add(time.Second).Format("2006-01-02 15:04:05")

	cmd := exec.Command("bash", "-c",
		fmt.Sprintf("mysqlbinlog --stop-datetime='%s' %s | grep GTID_NEXT | grep -v AUTOMATIC | tail -n 1",
			stopDatetime, relayLogPath))

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute mysqlbinlog pipeline: %w", err)
	}

	line := strings.TrimSpace(string(output))
	if line == "" {
		return "", fmt.Errorf("no GTID found at %s in %s", startDatetime, relayLogPath)
	}

	// Extract GTID from: SET @@SESSION.GTID_NEXT= 'uuid:n,uuid:n'/*!*/;
	start := strings.Index(line, "'")
	end := strings.LastIndex(line, "'")
	if start == -1 || end == -1 || start == end {
		return "", fmt.Errorf("failed to parse GTID from line: %s", line)
	}

	gtid := line[start+1 : end]
	return gtid, nil
}

// objectKeyFromURI extracts the S3 object key from a full URI.
// e.g. "https://minio-service:9000/bucket/binlogs/binlog.000001" -> "binlogs/binlog.000001"
// e.g. "s3://bucket/prefix/binlog.000001" -> "prefix/binlog.000001"
func objectKeyFromURI(uri, bucket string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme == "s3" {
		return strings.TrimPrefix(u.Path, "/"), nil
	}
	key := strings.TrimPrefix(u.Path, "/"+bucket+"/")
	return key, nil
}
