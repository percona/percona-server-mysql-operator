package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	"github.com/percona/percona-server-mysql-operator/cmd/sidecar/handler/backup"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func Backup() http.Handler {
	return new(backup.Handler)
}

func LogsHandlerFunc(w http.ResponseWriter, req *http.Request) {
	path := strings.Split(req.URL.Path, "/")
	if len(path) < 3 {
		http.Error(w, "backup name must be provided in URL", http.StatusBadRequest)
		return
	}

	backupName := path[2]
	if err := backup.ValidateBackupName(backupName); err != nil {
		http.Error(w, "invalid backup name", http.StatusBadRequest)
		return
	}
	logFile, err := os.Open(filepath.Join(mysql.BackupLogDir, backupName+".log"))
	if err != nil {
		http.Error(w, "failed to open log file", http.StatusInternalServerError)
		return
	}
	defer logFile.Close() //nolint:errcheck

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Connection", "keep-alive")

	buf := bufio.NewScanner(logFile)
	for buf.Scan() {
		if _, err := fmt.Fprintln(w, buf.Text()); err != nil {
			http.Error(w, "failed to scan log", http.StatusInternalServerError)
			return
		}
	}
	if err := buf.Err(); err != nil {
		http.Error(w, "failed to scan log", http.StatusInternalServerError)
		return
	}
}

func GetBackupInfoFunc(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
		return
	}

	log := logf.Log.WithName("GetBackupInfo")

	defer logClose(log, req.Body)
	data, err := io.ReadAll(req.Body)
	if err != nil {
		log.Error(err, "failed to read request body")
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}

	backupConf := xb.BackupConfig{}
	if err := json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "failed to unmarshal backup config", http.StatusBadRequest)
		return
	}

	info, err := fetchXbcloudFile(req.Context(), log, &backupConf, "xtrabackup_checkpoints")
	if err != nil {
		log.Error(err, "failed to get checkpoint info")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	backupInfo, err := fetchXbcloudFile(req.Context(), log, &backupConf, "xtrabackup_info")
	if err != nil {
		log.Info("failed to get backup info from xtrabackup_info, skipping backup size", "error", err)
	} else if backupInfo.BackupSize == 0 {
		// For compressed backups, xtrabackup_info is stored as xtrabackup_info.zst
		// Need to decompress it until [!!!insert link to PXB ticket!!!] is fixed.
		compressedInfo, err := fetchXbcloudFileDecompressed(req.Context(), log, &backupConf, "xtrabackup_info.zst")
		if err != nil {
			log.Info("failed to get compressed xtrabackup_info.zst, skipping backup size", "error", err)
		} else {
			info.BackupSize = compressedInfo.BackupSize
			info.UncompressedBackupSize = compressedInfo.UncompressedBackupSize
		}
	} else {
		info.BackupSize = backupInfo.BackupSize
		info.UncompressedBackupSize = backupInfo.UncompressedBackupSize
	}

	infoB, err := json.Marshal(info)
	if err != nil {
		log.Error(err, "failed to marshal backup info")
		http.Error(w, "failed to marshal backup info", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err = w.Write(infoB); err != nil {
		log.Error(err, "failed to write response")
	}
}

func logClose(log logr.Logger, closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Error(err, "failed to close")
	}
}

func fetchXbcloudFile(
	ctx context.Context,
	log logr.Logger,
	conf *xb.BackupConfig,
	file string) (xb.BackupInfo, error) {
	xbcloud := exec.CommandContext(ctx, "xbcloud", conf.XbcloudGetArgs(file)...)

	xbOut, err := xbcloud.StdoutPipe()
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	defer logClose(log, xbOut)

	xbErr, err := xbcloud.StderrPipe()
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	defer logClose(log, xbErr)

	if err := xbcloud.Start(); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to start xbcloud: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(os.Stderr, xbErr) //nolint:errcheck
	}()

	var info xb.BackupInfo
	if err := info.ParseFrom(xbOut); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to read backup info: %w", err)
	}

	wg.Wait()

	if err := xbcloud.Wait(); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("xbcloud command failed: %w", err)
	}

	return info, nil
}

func fetchXbcloudFileDecompressed(
	ctx context.Context,
	log logr.Logger,
	conf *xb.BackupConfig,
	file string) (xb.BackupInfo, error) {
	// xbcloud outputs data in xbstream format, so we need to:
	// 1. xbcloud get <file> — downloads chunks in xbstream format
	// 2. xbstream -x --decompress — extracts and decompresses .zst files
	// 3. Read the resulting plain text file

	originalFile := strings.TrimSuffix(file, ".zst")

	tmpDir, err := os.MkdirTemp("", "xb-decompress-*")
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	xbcloudArgs := conf.XbcloudGetArgs(file)
	//nolint:gosec
	cmd := exec.CommandContext(ctx, "bash", "-c",
		fmt.Sprintf("xbcloud %s | xbstream -x --decompress -C %s",
			shelljoin(xbcloudArgs), tmpDir))

	cmdErr, err := cmd.StderrPipe()
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	defer logClose(log, cmdErr)

	if err := cmd.Start(); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to start command: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(os.Stderr, cmdErr) //nolint:errcheck
	}()

	if err := cmd.Wait(); err != nil {
		wg.Wait()
		return xb.BackupInfo{}, fmt.Errorf("command failed: %w", err)
	}
	wg.Wait()

	f, err := os.Open(filepath.Join(tmpDir, originalFile))
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to open decompressed file: %w", err)
	}
	defer logClose(log, f)

	var info xb.BackupInfo
	if err := info.ParseFrom(f); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to parse decompressed file: %w", err)
	}

	return info, nil
}

func shelljoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}
