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
	// Attempt order: plain → encrypted → compressed → compressed+encrypted.
	backupInfo, err := fetchXbcloudFile(req.Context(), log, &backupConf, "xtrabackup_info")
	if err != nil {
		log.Info("failed to get backup info from xtrabackup_info", "error", err)
	} else if backupInfo.BackupSize > 0 {
		info.BackupSize = backupInfo.BackupSize
		info.UncompressedBackupSize = backupInfo.UncompressedBackupSize
	}

	// Try encrypted only: xtrabackup_info.xbcrypt (decrypt first)
	if info.BackupSize == 0 {
		encryptArgs := encryptionXbstreamArgs(&backupConf)
		if len(encryptArgs) > 0 {
			encInfo, err := fetchXbcloudFileWithXbstream(req.Context(), log, &backupConf, "xtrabackup_info.xbcrypt", encryptArgs...)
			if err != nil {
				log.Info("failed to get encrypted xtrabackup_info.xbcrypt", "error", err)
			} else if encInfo.BackupSize > 0 {
				info.BackupSize = encInfo.BackupSize
				info.UncompressedBackupSize = encInfo.UncompressedBackupSize
			}
		}
	}

	// Try compressed only: xtrabackup_info.zst or xtrabackup_info.lz4 (decompress)
	for _, compExt := range compressExtensions {
		if info.BackupSize == 0 {
			compFile := "xtrabackup_info" + compExt
			compressedInfo, err := fetchXbcloudFileWithXbstream(req.Context(), log, &backupConf, compFile, "--decompress")
			if err != nil {
				log.Info("failed to get compressed "+compFile, "error", err)
			} else if compressedInfo.BackupSize > 0 {
				info.BackupSize = compressedInfo.BackupSize
				info.UncompressedBackupSize = compressedInfo.UncompressedBackupSize
			}
		}
	}

	// Try compressed + encrypted: xtrabackup_info.{zst,lz4}.xbcrypt (decrypt then decompress)
	for _, compExt := range compressExtensions {
		if info.BackupSize == 0 {
			encryptArgs := encryptionXbstreamArgs(&backupConf)
			if len(encryptArgs) > 0 {
				compEncFile := "xtrabackup_info" + compExt + ".xbcrypt"
				bothArgs := append(encryptArgs, "--decompress")
				compEncInfo, err := fetchXbcloudFileWithXbstream(req.Context(), log, &backupConf, compEncFile, bothArgs...)
				if err != nil {
					log.Info("failed to get compressed+encrypted "+compEncFile+", skipping backup size", "error", err)
				} else {
					info.BackupSize = compEncInfo.BackupSize
					info.UncompressedBackupSize = compEncInfo.UncompressedBackupSize
				}
			}
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
	wg.Go(func() {
		io.Copy(os.Stderr, xbErr) //nolint:errcheck
	})

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

func fetchXbcloudFileWithXbstream(
	ctx context.Context,
	log logr.Logger,
	conf *xb.BackupConfig,
	file string,
	xbstreamArgs ...string) (xb.BackupInfo, error) {
	// xbcloud outputs data in xbstream format, so we need to:
	// 1. xbcloud get <file> — downloads chunks in xbstream format
	// 2. xbstream -x [--decompress] [--decrypt=...] — extracts and processes files
	// 3. Read the resulting plain text file

	originalFile := file
	for _, ext := range append([]string{".xbcrypt"}, compressExtensions...) {
		originalFile = strings.TrimSuffix(originalFile, ext)
	}

	tmpDir, err := os.MkdirTemp("", "xb-decompress-*")
	if err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	xbcloud := exec.CommandContext(ctx, "xbcloud", conf.XbcloudGetArgs(file)...)

	streamArgs := append([]string{"-x"}, xbstreamArgs...)
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
		return xb.BackupInfo{}, fmt.Errorf("failed to open decompressed file: %w", err)
	}
	defer logClose(log, f)

	var info xb.BackupInfo
	if err := info.ParseFrom(f); err != nil {
		return xb.BackupInfo{}, fmt.Errorf("failed to parse decompressed file: %w", err)
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
