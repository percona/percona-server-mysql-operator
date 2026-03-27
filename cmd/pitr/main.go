package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
	"github.com/pkg/errors"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: pitr <setup|apply>")
	}

	ctx := context.Background()

	switch os.Args[1] {
	case "setup":
		if err := runSetup(ctx); err != nil {
			log.Fatalf("setup failed: %v", err)
		}
	case "apply":
		if err := runApply(ctx); err != nil {
			log.Fatalf("apply failed: %v", err)
		}
	default:
		log.Fatalf("unknown subcommand: %s", os.Args[1])
	}
}

func runSetup(ctx context.Context) error {
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

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("get hostname: %w", err)
	}

	endpoint := os.Getenv("AWS_ENDPOINT")
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	region := os.Getenv("AWS_DEFAULT_REGION")
	bucket := os.Getenv("S3_BUCKET")
	verifyTLS := os.Getenv("VERIFY_TLS") != "false"

	s3Client, err := storage.NewS3(ctx, endpoint, accessKey, secretKey, bucket, "", region, verifyTLS)
	if err != nil {
		return fmt.Errorf("create S3 client: %w", err)
	}

	var relayLogFiles []string
	for i, entry := range entries {
		relayLogName := fmt.Sprintf("%s-relay-bin.%06d", hostname, i+1)
		relayLogPath := fmt.Sprintf("/var/lib/mysql/%s", relayLogName)

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

	indexPath := fmt.Sprintf("/var/lib/mysql/%s-relay-bin.index", hostname)
	indexContent := strings.Join(relayLogFiles, "\n") + "\n"
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		return fmt.Errorf("write relay log index: %w", err)
	}

	log.Printf("setup complete: %d relay log files written", len(relayLogFiles))
	return nil
}

func runApply(ctx context.Context) error {
	pitrType := os.Getenv("PITR_TYPE")
	pitrDate := os.Getenv("PITR_DATE")
	pitrGTID := os.Getenv("PITR_GTID")

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("get hostname: %w", err)
	}

	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return fmt.Errorf("get operator password: %w", err)
	}

	database, err := db.NewDatabase(ctx, db.DBParams{
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

	binlogsPath := os.Getenv("BINLOGS_PATH")
	data, err := os.ReadFile(binlogsPath)
	if err != nil {
		return fmt.Errorf("read binlogs file: %w", err)
	}

	var entries []binlogserver.BinlogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse binlogs json: %w", err)
	}

	lastRelayLog := fmt.Sprintf("%s-relay-bin.%06d", hostname, len(entries))
	lastRelayLogPath := fmt.Sprintf("/var/lib/mysql/%s", lastRelayLog)

	if pitrType == "date" {
		pitrGTID, err = getLatestGTIDByDatetime(lastRelayLogPath, pitrDate)
		if err != nil {
			return fmt.Errorf("get latest GTID for date %s: %w", pitrDate, err)
		}
		log.Printf("latest GTID for date %s: %s", pitrDate, pitrGTID)
	}

	firstRelayLog := fmt.Sprintf("%s-relay-bin.000001", hostname)

	log.Println("running 'CHANGE REPLICATION SOURCE'")
	if err := database.ChangeReplicationSourceRelay(ctx, firstRelayLog, 4); err != nil {
		return fmt.Errorf("change replication source: %w", err)
	}

	log.Printf("starting replica until GTID: %s", pitrGTID)
	if err := database.StartReplicaUntilGTID(ctx, pitrGTID); err != nil {
		return fmt.Errorf("start replica until GTID: %w", err)
	}

	log.Println("waiting for replication to complete...")
	if err := database.WaitReplicaSQLThreadStop(ctx, time.Second); err != nil {
		return fmt.Errorf("wait for replication: %w", err)
	}

	log.Println("stopping replication")
	if err := database.StopReplication(ctx); err != nil {
		return errors.Wrap(err, "stop replication")
	}

	log.Println("running 'RESET REPLICA ALL'")
	if err := database.ResetReplication(ctx); err != nil {
		return errors.Wrap(err, "reset replication")
	}

	log.Println("PITR apply complete")
	return nil
}

func getLatestGTIDByDatetime(relayLogPath, stopDatetime string) (string, error) {
	cmd := exec.Command("bash", "-c",
		fmt.Sprintf("mysqlbinlog --stop-datetime='%s' %s | grep GTID_NEXT | grep -v AUTOMATIC | tail -n 1",
			stopDatetime, relayLogPath))

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute mysqlbinlog pipeline: %w", err)
	}

	line := strings.TrimSpace(string(output))
	if line == "" {
		return "", fmt.Errorf("no GTID found before %s in %s", stopDatetime, relayLogPath)
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
