package binlogserver

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver/gtid"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

const BinlogServerBinary = "/usr/bin/binlog_server"

const (
	SearchByTimestampCommand = "search_by_timestamp"
	SearchByGTIDCommand      = "search_by_gtid_set"
	SearchContainerName      = "binlog-search"
	binlogTimestampLayout    = "2006-01-02T15:04:05"
)

func parsePITRDate(value string) (string, error) {
	var timestamp time.Time
	var err error
	if timestamp, err = time.Parse(time.RFC3339Nano, value); err != nil {
		if timestamp, err = time.Parse(binlogTimestampLayout, strings.Replace(value, " ", "T", 1)); err != nil {
			return "", errors.Wrap(err, "failed to parse time")
		}
	}

	// binlog_server accepts UTC timestamps without a timezone suffix, to the second.
	return timestamp.UTC().Format(binlogTimestampLayout), nil
}

func SearchArgs(restore *apiv1.PerconaServerMySQLRestore) (string, string, error) {
	if restore == nil || restore.Spec.PITR == nil {
		return "", "", errors.New("pitr spec is not set")
	}

	switch restore.Spec.PITR.Type {
	case apiv1.PITRDate:
		date, err := parsePITRDate(restore.Spec.PITR.Date)
		if err != nil {
			return "", "", errors.Errorf("invalid pitr date %q, expected RFC3339 or %q", restore.Spec.PITR.Date, "2006-01-02 15:04:05")
		}
		return SearchByTimestampCommand, date, nil
	case apiv1.PITRGtid:
		set, err := gtid.Parse(restore.Spec.PITR.GTID)
		if err != nil {
			return "", "", errors.Wrap(err, "parse pitr target")
		}
		if set.IsEmpty() {
			return "", "", errors.New("GTID set is empty")
		}
		return SearchByGTIDCommand, restore.Spec.PITR.GTID, nil
	default:
		return "", "", errors.Errorf("unknown PITR type: %s", restore.Spec.PITR.Type)
	}
}

type SearchResponse struct {
	Version int           `json:"version"`
	Status  string        `json:"status"`
	Result  []BinlogEntry `json:"result"`
	Message string        `json:"message,omitempty"`
}

func (r *SearchResponse) Error() error {
	if r.Status == "success" {
		return nil
	}
	if r.Message != "" {
		return errors.Errorf("binlog search failed: %s", r.Message)
	}
	return errors.Errorf("binlog search failed with status: %s", r.Status)
}

type BinlogEntry struct {
	Name          string      `json:"name"`
	URI           string      `json:"uri"`
	Size          int64       `json:"size,omitempty"`
	PreviousGTIDs string      `json:"previous_gtids,omitempty"`
	AddedGTIDs    string      `json:"added_gtids,omitempty"`
	MinTimestamp  string      `json:"min_timestamp,omitempty"`
	MaxTimestamp  string      `json:"max_timestamp,omitempty"`
	Encryption    *Encryption `json:"encryption,omitempty"`
}

type Encryption struct {
	FileKeyEnvelope  *FileKeyEnvelope  `json:"file_key_envelope,omitempty"`
	FileDataEnvelope *FileDataEnvelope `json:"file_data_envelope,omitempty"`
}

type FileKeyEnvelope struct {
	KekID   string `json:"kek_id"`
	DataHex string `json:"data_hex"`
	IVHex   string `json:"iv_hex"`
	TagHex  string `json:"tag_hex"`
}

type FileDataEnvelope struct {
	Cipher string `json:"cipher"`
	IVHex  string `json:"iv_hex"`
}

func SearchByGTID(ctx context.Context, cl client.Client, cliCmd clientcmd.Client, cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore, gtidSet string) (*SearchResponse, error) {
	return execSearch(ctx, cl, cliCmd, cr, restore, SearchByGTIDCommand, gtidSet)
}

func SearchByTimestamp(ctx context.Context, cl client.Client, cliCmd clientcmd.Client, cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore, timestamp string) (*SearchResponse, error) {
	return execSearch(ctx, cl, cliCmd, cr, restore, SearchByTimestampCommand, timestamp)
}

func execSearch(ctx context.Context, cl client.Client, cliCmd clientcmd.Client, cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore, subcommand, arg string) (*SearchResponse, error) {
	pod, err := GetBinlogServerPod(ctx, cl, cr, restore)
	if err != nil {
		return nil, errors.Wrap(err, "get binlog server pod")
	}

	cmd := []string{BinlogServerBinary, subcommand, path.Join(ConfigMountPath, ConfigKey), arg}
	var stdout, stderr bytes.Buffer
	if err := cliCmd.Exec(ctx, pod, AppName, cmd, nil, &stdout, &stderr, false); err != nil {
		return nil, errors.Wrapf(err, "exec binlog_server %s: stdout: %s stderr: %s", subcommand, stdout.String(), stderr.String())
	}

	var resp SearchResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, errors.Wrapf(err, "unmarshal response: %s", stdout.String())
	}
	return &resp, nil
}

func GetBinlogServerPod(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) (*corev1.Pod, error) {
	nn := types.NamespacedName{Namespace: cr.Namespace, Name: BinlogServerPodName(cr, restore)}
	pod := &corev1.Pod{}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, errors.Wrapf(err, "get pod %s", nn)
	}
	if !k8s.IsPodReady(*pod) {
		return nil, errors.Errorf("binlog server pod %s is not ready", nn)
	}
	return pod, nil
}

func BinlogServerPodName(cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) string {
	if restore != nil && restore.Spec.PITR != nil && restore.Spec.PITR.BackupSource != nil && restore.Spec.PITR.BackupSource.BinlogServer != nil {
		return RestoreName(cr, restore) + "-0"
	}
	return Name(cr) + "-0"
}
