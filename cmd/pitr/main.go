package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

type BinlogEntry struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	URI           string `json:"uri"`
	PreviousGTIDs string `json:"previous_gtids"`
	AddedGTIDs    string `json:"added_gtids"`
	MinTimestamp  string `json:"min_timestamp"`
	MaxTimestamp  string `json:"max_timestamp"`
}

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

	var entries []BinlogEntry
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

	storageType := os.Getenv("STORAGE_TYPE")

	var s3Client storage.Storage
	switch storageType {
	case string(apiv1.BackupStorageS3), string(apiv1.BackupStorageGCS):
		endpoint := os.Getenv("AWS_ENDPOINT")
		accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
		secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
		region := os.Getenv("AWS_DEFAULT_REGION")
		bucket := os.Getenv("S3_BUCKET")
		verifyTLS := os.Getenv("VERIFY_TLS") != "false"

		if storageType == string(apiv1.BackupStorageGCS) {
			s3Client, err = storage.NewGCS(ctx, endpoint, accessKey, secretKey, bucket, "", verifyTLS)
		} else {
			s3Client, err = storage.NewS3(ctx, endpoint, accessKey, secretKey, bucket, "", region, verifyTLS)
		}
		if err != nil {
			return fmt.Errorf("create S3 client: %w", err)
		}
	case string(apiv1.BackupStorageAzure):
		storageAccount := os.Getenv("AZURE_STORAGE_ACCOUNT")
		accessKey := os.Getenv("AZURE_ACCESS_KEY")
		endpoint := os.Getenv("AZURE_ENDPOINT")
		container := os.Getenv("AZURE_CONTAINER")

		s3Client, err = storage.NewAzure(ctx, storageAccount, accessKey, endpoint, container, "")
		if err != nil {
			return fmt.Errorf("create Azure client: %w", err)
		}
	default:
		return fmt.Errorf("unsupported storage type: %s", storageType)
	}

	var relayLogFiles []string
	for i, entry := range entries {
		relayLogName := fmt.Sprintf("%s-relay-bin.%06d", hostname, i+1)
		relayLogPath := fmt.Sprintf("/var/lib/mysql/%s", relayLogName)

		log.Printf("downloading binlog %s to %s", entry.URI, relayLogPath)

		obj, err := s3Client.GetObject(ctx, entry.URI)
		if err != nil {
			return fmt.Errorf("download binlog %s: %w", entry.URI, err)
		}

		f, err := os.Create(relayLogPath)
		if err != nil {
			obj.Close()
			return fmt.Errorf("create relay log file %s: %w", relayLogPath, err)
		}

		_, err = io.Copy(f, obj)
		obj.Close()
		f.Close()
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
		Host: "localhost",
	})
	if err != nil {
		return fmt.Errorf("connect to MySQL: %w", err)
	}
	defer database.Close()

	binlogsPath := os.Getenv("BINLOGS_PATH")
	data, err := os.ReadFile(binlogsPath)
	if err != nil {
		return fmt.Errorf("read binlogs file: %w", err)
	}

	var entries []BinlogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse binlogs json: %w", err)
	}

	lastRelayLog := fmt.Sprintf("%s-relay-bin.%06d", hostname, len(entries))
	lastRelayLogPath := fmt.Sprintf("/var/lib/mysql/%s", lastRelayLog)

	var stopPos int
	if pitrType == "date" {
		stopPos, err = getStopPosition(lastRelayLogPath, pitrDate)
		if err != nil {
			return fmt.Errorf("get stop position: %w", err)
		}
		log.Printf("stop position for date %s: %d", pitrDate, stopPos)
	}

	firstRelayLog := fmt.Sprintf("%s-relay-bin.000001", hostname)

	if err := database.ChangeReplicationSourceRelay(ctx, firstRelayLog, 1); err != nil {
		return fmt.Errorf("change replication source: %w", err)
	}

	switch pitrType {
	case "gtid":
		log.Printf("starting replica until GTID: %s", pitrGTID)
		if err := database.StartReplicaUntilGTID(ctx, pitrGTID); err != nil {
			return fmt.Errorf("start replica until GTID: %w", err)
		}
	case "date":
		log.Printf("starting replica until position: %s:%d", lastRelayLog, stopPos)
		if err := database.StartReplicaUntilPosition(ctx, lastRelayLog, stopPos); err != nil {
			return fmt.Errorf("start replica until position: %w", err)
		}
	default:
		return fmt.Errorf("unsupported PITR type: %s", pitrType)
	}

	log.Println("waiting for replication to complete...")
	if err := database.WaitReplicaSQLThreadStop(ctx, time.Second); err != nil {
		return fmt.Errorf("wait for replication: %w", err)
	}

	if err := database.ResetReplication(ctx); err != nil {
		return fmt.Errorf("reset replication: %w", err)
	}

	log.Println("PITR apply complete")
	return nil
}

func getStopPosition(relayLogPath, stopDatetime string) (int, error) {
	cmd := exec.Command("mysqlbinlog", "--stop-datetime="+stopDatetime, relayLogPath)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("run mysqlbinlog: %w", err)
	}

	re := regexp.MustCompile(`end_log_pos (\d+)`)
	matches := re.FindAllStringSubmatch(string(out), -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("no end_log_pos found in mysqlbinlog output")
	}

	lastMatch := matches[len(matches)-1]
	pos, err := strconv.Atoi(lastMatch[1])
	if err != nil {
		return 0, fmt.Errorf("parse end_log_pos: %w", err)
	}

	return pos, nil
}
