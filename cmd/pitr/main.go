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

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

// Database defines the MySQL operations needed for PITR.
type Database interface {
	GetGTIDExecuted(ctx context.Context) (string, error)
	Close() error
}

type newStorageFn func(ctx context.Context, endpoint, accessKey, secretKey, bucket, prefix, region string, verifyTLS bool) (storage.Storage, error)
type newDatabaseFn func(ctx context.Context, params db.DBParams) (Database, error)

// applyBinlogsFn starts a single mysql client process and for each binlog file
// runs mysqlbinlog with the given args, piping the output into mysql's stdin.
type applyBinlogsFn func(ctx context.Context, binlogPaths []string, mysqlbinlogArgs []string, mysqlArgs []string, mysqlPass string) error

func main() {
	ctx := context.Background()

	newDB := func(ctx context.Context, params db.DBParams) (Database, error) {
		return db.NewDatabase(ctx, params)
	}

	if err := run(ctx, storage.NewS3, newDB, utils.GetSecret, applyBinlogs); err != nil {
		log.Fatalf("pitr failed: %v", err)
	}
}

func run(ctx context.Context, newS3 newStorageFn, newDB newDatabaseFn, getSecret func(apiv1.SystemUser) (string, error), apply applyBinlogsFn) error {
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

	// Connect to MySQL to get the backup's GTID_EXECUTED.
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
		database.Close()
		return fmt.Errorf("get GTID_EXECUTED: %w", err)
	}
	log.Printf("GTID_EXECUTED from backup: %s", gtidExecuted)
	database.Close()

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

	tmpDir, err := os.MkdirTemp("", "pitr-binlogs-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var binlogPaths []string
	for i, entry := range entries {
		binlogName := fmt.Sprintf("binlog.%06d", i+1)
		binlogPath := filepath.Join(tmpDir, binlogName)

		objectKey, err := objectKeyFromURI(entry.URI, bucket)
		if err != nil {
			return fmt.Errorf("parse URI %s: %w", entry.URI, err)
		}

		log.Printf("downloading binlog %s to %s", objectKey, binlogPath)

		obj, err := s3Client.GetObject(ctx, objectKey)
		if err != nil {
			return fmt.Errorf("download binlog %s: %w", entry.URI, err)
		}

		f, err := os.Create(binlogPath)
		if err != nil {
			obj.Close()
			return fmt.Errorf("create binlog file %s: %w", binlogPath, err)
		}

		_, err = io.Copy(f, obj)
		obj.Close()
		f.Close()
		if err != nil {
			return fmt.Errorf("write binlog file %s: %w", binlogPath, err)
		}

		binlogPaths = append(binlogPaths, binlogPath)
	}

	// Build mysqlbinlog args.
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

	// Build mysql client args.
	mysqlArgs := []string{
		"-u", string(apiv1.UserOperator),
		"-h", "127.0.0.1",
		"-P", "33062",
	}

	log.Printf("applying %d binlog(s) with mysqlbinlog args: %v", len(binlogPaths), mysqlbinlogArgs)

	if err := apply(ctx, binlogPaths, mysqlbinlogArgs, mysqlArgs, operatorPass); err != nil {
		return fmt.Errorf("apply binlogs: %w", err)
	}

	// Reconnect to log the final GTID state.
	database, err = newDB(ctx, db.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: "127.0.0.1",
	})
	if err != nil {
		return fmt.Errorf("reconnect to MySQL: %w", err)
	}
	defer database.Close()

	gtidExecuted, err = database.GetGTIDExecuted(ctx)
	if err != nil {
		return fmt.Errorf("get GTID_EXECUTED after restore: %w", err)
	}
	log.Printf("GTID_EXECUTED after PITR: %s", gtidExecuted)

	log.Println("PITR complete")
	return nil
}

// applyBinlogs starts a single mysql client and for each binlog file
// spawns mysqlbinlog, piping its output into mysql's stdin.
func applyBinlogs(ctx context.Context, binlogPaths []string, mysqlbinlogArgs []string, mysqlArgs []string, mysqlPass string) error {
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

	for _, binlogPath := range binlogPaths {
		args := append(mysqlbinlogArgs, binlogPath)
		binlogCmd := exec.CommandContext(ctx, "mysqlbinlog", args...)

		var binlogStderr bytes.Buffer
		binlogCmd.Stdout = mysqlStdin
		binlogCmd.Stderr = &binlogStderr

		log.Printf("running: mysqlbinlog %s", binlogPath)

		if err := binlogCmd.Run(); err != nil {
			mysqlStdin.Close()
			mysqlCmd.Wait()
			return fmt.Errorf("mysqlbinlog %s failed: %w, stderr: %s", binlogPath, err, binlogStderr.String())
		}
	}

	mysqlStdin.Close()

	if err := mysqlCmd.Wait(); err != nil {
		return fmt.Errorf("mysql failed: %w, stderr: %s", err, mysqlStderr.String())
	}

	return nil
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
