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

// compressExtensions lists file extensions produced by xtrabackup compression algorithms.
var compressExtensions = []string{".zst", ".lz4"}

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

// infoFileCandidates returns a list of possible xbcloud file names to probe
// for a given base file. The order is: plain, compressed (.zst, .lz4),
// encrypted (.xbcrypt), and compressed+encrypted (.zst.xbcrypt, .lz4.xbcrypt).
func infoFileCandidates(base string) []string {
	candidates := []string{base}
	for _, ext := range compressExtensions {
		candidates = append(candidates, base+ext)
	}
	candidates = append(candidates, base+".xbcrypt")
	for _, ext := range compressExtensions {
		candidates = append(candidates, base+ext+".xbcrypt")
	}
	return candidates
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

	// Try to get backup size from xtrabackup_info.
	// xtrabackup applies: compress → encrypt, so we reverse: decrypt → decompress.
	// Probe all candidate file variants and return early on first success.
	for _, candidate := range infoFileCandidates("xtrabackup_info") {
		backupInfo, err := fetchXbcloudFile(req.Context(), log, &backupConf, candidate)
		if err != nil {
			log.Info("failed to get backup info", "file", candidate, "error", err)
			continue
		}
		if backupInfo.BackupSize > 0 {
			info.BackupSize = backupInfo.BackupSize
			info.UncompressedBackupSize = backupInfo.UncompressedBackupSize
			break
		}
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

// fetchXbcloudFile downloads a file from cloud storage via xbcloud, pipes it
// through xbstream (with --decompress and optional --decrypt flags) to handle
// any compression or encryption, then parses the resulting plain text.
func fetchXbcloudFile(
	ctx context.Context,
	log logr.Logger,
	conf *xb.BackupConfig,
	file string,
) (xb.BackupInfo, error) {
	originalFile := file
	for _, ext := range append([]string{".xbcrypt"}, compressExtensions...) {
		originalFile = strings.TrimSuffix(originalFile, ext)
	}

	tmpDir, err := os.MkdirTemp("", "xb-fetch-*")
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	xbcloud := exec.CommandContext(ctx, "xbcloud", conf.XbcloudGetArgs(file)...)

	streamArgs := []string{"-x", "--decompress"}
	streamArgs = append(streamArgs, encryptionXbstreamArgs(conf)...)
	streamArgs = append(streamArgs, "-C", tmpDir)
	xbstream := exec.CommandContext(ctx, "xbstream", streamArgs...)

	xbcloudOut, err := xbcloud.StdoutPipe()
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create xbcloud stdout pipe: %w", err)
	}
	defer logClose(log, xbcloudOut)

	xbcloudErr, err := xbcloud.StderrPipe()
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create xbcloud stderr pipe: %w", err)
	}
	defer logClose(log, xbcloudErr)

	xbstream.Stdin = xbcloudOut

	if err := xbcloud.Start(); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to start xbcloud: %w", err)
	}

	if err := xbstream.Start(); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to start xbstream: %w", err)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		io.Copy(os.Stderr, xbcloudErr) //nolint:errcheck
	})

	if err := xbstream.Wait(); err != nil {
		wg.Wait()
		return xb.BackupInfo{}, fmt.Errorf("xbstream command failed: %w", err)
	}
	wg.Wait()

	if err := xbcloud.Wait(); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("xbcloud command failed: %w", err)
	}

	f, err := os.Open(filepath.Join(tmpDir, originalFile))
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to open extracted file: %w", err)
	}
	defer logClose(log, f)

	var info xb.BackupInfo
	if err := info.ParseFrom(f); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to parse extracted file: %w", err)
	}

	return info, nil
}

func encryptionXbstreamArgs(conf *xb.BackupConfig) []string {
	if conf.EncryptionKeyFile == "" {
		return nil
	}
	algorithm := "AES256"
	if conf.ContainerOptions != nil {
		if v := conf.ContainerOptions.GetArgs().GetXtrabackupFlagValue("--encrypt"); v != "" {
			algorithm = v
		}
	}
	return []string{
		fmt.Sprintf("--decrypt=%s", algorithm),
		fmt.Sprintf("--encrypt-key-file=%s", conf.EncryptionKeyFile),
	}
}
