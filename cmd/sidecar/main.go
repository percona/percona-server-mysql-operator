package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/pkg/errors"
)

var log = logf.Log.WithName("sidecar")
var sensitiveFlags = regexp.MustCompile("--password=(.*)|--.*-access-key=(.*)|--.*secret-key=(.*)")

type BackupConf struct {
	Destination string                        `json:"destination"`
	Type        apiv1alpha1.BackupStorageType `json:"type"`
	VerifyTLS   bool                          `json:"verifyTLS,omitempty"`
	S3          struct {
		Bucket       string `json:"bucket"`
		Region       string `json:"region,omitempty"`
		EndpointURL  string `json:"endpointUrl,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
		AccessKey    string `json:"accessKey,omitempty"`
		SecretKey    string `json:"secretKey,omitempty"`
	} `json:"s3,omitempty"`
	GCS struct {
		Bucket       string `json:"bucket"`
		EndpointURL  string `json:"endpointUrl,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
		AccessKey    string `json:"accessKey,omitempty"`
		SecretKey    string `json:"secretKey,omitempty"`
	} `json:"gcs,omitempty"`
	Azure struct {
		ContainerName  string `json:"containerName"`
		EndpointURL    string `json:"endpointUrl,omitempty"`
		StorageClass   string `json:"storageClass,omitempty"`
		StorageAccount string `json:"storageAccount,omitempty"`
		AccessKey      string `json:"accessKey,omitempty"`
	} `json:"azure,omitempty"`
}

func main() {
	opts := zap.Options{Development: true}
	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mux := http.NewServeMux()

	mux.HandleFunc("/health/", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "OK")
	})
	mux.HandleFunc("/backup/", backupHandler)
	mux.HandleFunc("/logs/", logHandler)

	log.Info("starting http server")
	log.Error(http.ListenAndServe(":6033", mux), "http server failed")
}

func getSecret(username apiv1alpha1.SystemUser) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, string(username))
	sBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

func sanitizeCmd(cmd *exec.Cmd) string {
	c := []string{cmd.Path}

	for _, arg := range cmd.Args[1:] {
		c = append(c, sensitiveFlags.ReplaceAllString(arg, ""))
	}

	return strings.Join(c, " ")
}

func getNamespace() (string, error) {
	ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", errors.Wrap(err, "read namespace file")
	}

	return string(ns), nil
}

func xtrabackupArgs(user, pass string) []string {
	return []string{
		"--backup",
		"--stream=xbstream",
		fmt.Sprintf("--user=%s", user),
		fmt.Sprintf("--password=%s", pass),
	}
}

func xbcloudArgs(conf BackupConf) []string {
	args := []string{"put", "--parallel=10", "--curl-retriable-errors=7"}

	if !conf.VerifyTLS {
		args = append(args, "--insecure")
	}

	switch conf.Type {
	case apiv1alpha1.BackupStorageGCS:
		args = append(
			args,
			[]string{
				"--md5",
				"--storage=google",
				fmt.Sprintf("--google-bucket=%s", conf.GCS.Bucket),
				fmt.Sprintf("--google-endpoint=%s", conf.GCS.EndpointURL),
				fmt.Sprintf("--google-access-key=%s", conf.GCS.AccessKey),
				fmt.Sprintf("--google-secret-key=%s", conf.GCS.SecretKey),
			}...,
		)
	case apiv1alpha1.BackupStorageS3:
		args = append(
			args,
			[]string{
				"--md5",
				"--storage=s3",
				fmt.Sprintf("--s3-bucket=%s", conf.S3.Bucket),
				fmt.Sprintf("--s3-region=%s", conf.S3.Region),
				fmt.Sprintf("--s3-endpoint=%s", conf.S3.EndpointURL),
				fmt.Sprintf("--s3-access-key=%s", conf.S3.AccessKey),
				fmt.Sprintf("--s3-secret-key=%s", conf.S3.SecretKey),
			}...,
		)
	case apiv1alpha1.BackupStorageAzure:
		args = append(
			args,
			[]string{
				"--storage=azure",
				fmt.Sprintf("--azure-storage-account=%s", conf.Azure.StorageAccount),
				fmt.Sprintf("--azure-container-name=%s", conf.Azure.ContainerName),
				fmt.Sprintf("--azure-endpoint=%s", conf.Azure.EndpointURL),
				fmt.Sprintf("--azure-access-key=%s", conf.Azure.AccessKey),
			}...,
		)
	}

	args = append(args, conf.Destination)

	return args
}

func backupHandler(w http.ResponseWriter, req *http.Request) {
	ns, err := getNamespace()
	if err != nil {
		log.Error(err, "failed to detect namespace")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	path := strings.Split(req.URL.Path, "/")
	if len(path) < 3 {
		http.Error(w, "backup name must be provided in URL", http.StatusBadRequest)
		return
	}
	backupName := path[2]
	log = log.WithValues("namespace", ns, "name", backupName)

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Error(err, "failed to read request data")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	backupConf := BackupConf{}
	if err := json.Unmarshal(data, &backupConf); err != nil {
		log.Error(err, "failed to unmarshal backup config")
		http.Error(w, "backup failed", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Connection", "keep-alive")

	backupUser := apiv1alpha1.UserXtraBackup
	backupPass, err := getSecret(backupUser)
	if err != nil {
		log.Error(err, "failed to get backup password")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	xtrabackup := exec.Command("xtrabackup", xtrabackupArgs(string(backupUser), backupPass)...)

	xbOut, err := xtrabackup.StdoutPipe()
	if err != nil {
		log.Error(err, "xtrabackup stdout pipe failed")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer xbOut.Close()

	xbErr, err := xtrabackup.StderrPipe()
	if err != nil {
		log.Error(err, "xtrabackup stderr pipe failed")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer xbErr.Close()

	backupLog, err := os.Create(filepath.Join(mysql.BackupLogDir, backupName+".log"))
	if err != nil {
		log.Error(err, "failed to create log file")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	logWriter := io.MultiWriter(backupLog, os.Stderr)

	xbcloud := exec.Command("xbcloud", xbcloudArgs(backupConf)...)
	xbcloud.Stdin = xbOut
	xbcloud.Stderr = logWriter

	log.Info(
		"Backup starting",
		"destination", backupConf.Destination,
		"storage", backupConf.Type,
		"xtrabackupCmd", sanitizeCmd(xtrabackup),
		"xbcloudCmd", sanitizeCmd(xbcloud),
	)

	if err := xtrabackup.Start(); err != nil {
		log.Error(err, "failed to start xtrabackup command")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	if err := xbcloud.Run(); err != nil {
		log.Error(err, "failed to run xbcloud")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(logWriter, xbErr); err != nil {
		log.Error(err, "failed to copy xtrabackup stderr")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	if err := xtrabackup.Wait(); err != nil {
		log.Error(err, "failed waiting for xtrabackup to finish")
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	log.Info("Backup finished successfully", "destination", backupConf.Destination, "storage", backupConf.Type)
}

func logHandler(w http.ResponseWriter, req *http.Request) {
	path := strings.Split(req.URL.Path, "/")
	if len(path) < 3 {
		http.Error(w, "backup name must be provided in URL", http.StatusBadRequest)
		return
	}

	backupName := path[2]
	logFile, err := os.Open(filepath.Join(mysql.BackupLogDir, backupName+".log"))
	if err != nil {
		http.Error(w, "failed to open log file", http.StatusInternalServerError)
		return
	}
	defer logFile.Close()

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Connection", "keep-alive")

	buf := bufio.NewScanner(logFile)
	for buf.Scan() {
		fmt.Fprintln(w, buf.Text())
	}
	if err := buf.Err(); err != nil {
		http.Error(w, "failed to scan log", http.StatusInternalServerError)
		return
	}
}
