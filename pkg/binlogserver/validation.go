package binlogserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/pkg/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver/gtid"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

const (
	binlogIndexName         = "binlog.index"
	binlogMetadataExtension = ".json"
)

var (
	ErrTargetNotCovered = errors.New("pitr target is not covered by the binlog archive")
	ErrInvalidTarget    = errors.New("invalid pitr target")
)

type archiveMetadata struct {
	PreviousGTIDs *string `json:"previous_gtids"`
	AddedGTIDs    *string `json:"added_gtids"`
	MinTimestamp  *string `json:"min_timestamp"`
}

// ValidateTarget rejects a restore target if it is older than the archived binlogs
func ValidateTarget(ctx context.Context, st storage.Storage, restore *apiv1.PerconaServerMySQLRestore) error {
	subcommand, targetArg, err := SearchArgs(restore)
	if err != nil {
		return errors.Wrap(ErrInvalidTarget, err.Error())
	}

	name, err := oldestBinlogName(ctx, st)
	if err != nil {
		return errors.Wrap(err, "read binlog index")
	}
	if name == "" {
		return nil
	}
	metadata, err := getBinlogMetadata(ctx, st, name)
	if err != nil {
		return errors.Wrapf(err, "read metadata of binlog %s", name)
	}

	switch subcommand {
	case SearchByTimestampCommand:
		return validateTimestampTarget(name, metadata, targetArg)
	case SearchByGTIDCommand:
		return validateGTIDTarget(name, metadata, targetArg)
	default:
		return errors.Wrapf(ErrInvalidTarget, "unknown search command %s", subcommand)
	}
}

// validateTimestampTarget rejects times before the oldest binlog starts.
func validateTimestampTarget(name string, metadata *archiveMetadata, targetArg string) error {
	target, err := time.Parse(binlogTimestampLayout, targetArg)
	if err != nil {
		return errors.Wrapf(ErrInvalidTarget, "parse pitr target %s: %v", targetArg, err)
	}

	if metadata.MinTimestamp == nil || *metadata.MinTimestamp == "" {
		return errors.Errorf("binlog %s has no min_timestamp", name)
	}
	oldest, err := time.Parse(binlogTimestampLayout, *metadata.MinTimestamp)
	if err != nil {
		return errors.Wrapf(err, "parse min_timestamp %s", *metadata.MinTimestamp)
	}
	if target.Before(oldest) {
		return errors.Wrapf(ErrTargetNotCovered,
			"timestamp is too old, the archive starts at %s", *metadata.MinTimestamp)
	}
	return nil
}

// validateGTIDTarget rejects GTIDs that were executed before the oldest binlog started.
func validateGTIDTarget(name string, metadata *archiveMetadata, targetArg string) error {
	target, err := gtid.Parse(targetArg)
	if err != nil {
		return errors.Wrapf(ErrInvalidTarget, "parse pitr target: %v", err)
	}

	if metadata.PreviousGTIDs == nil || metadata.AddedGTIDs == nil {
		return errors.Errorf("binlog %s has incomplete GTID metadata", name)
	}

	previous, err := gtid.Parse(*metadata.PreviousGTIDs)
	if err != nil {
		return errors.Wrapf(err, "parse previous_gtids of binlog %s", name)
	}
	if _, err := gtid.Parse(*metadata.AddedGTIDs); err != nil {
		return errors.Wrapf(err, "parse added_gtids of binlog %s", name)
	}
	if missingGTIDs := target.Intersect(previous); !missingGTIDs.IsEmpty() {
		return errors.Wrapf(ErrTargetNotCovered, "the specified GTID set predates the binlog archive, which is missing %s", missingGTIDs)
	}
	return nil
}

func oldestBinlogName(ctx context.Context, st storage.Storage) (string, error) {
	obj, err := st.GetObject(ctx, binlogIndexName)
	if err != nil {
		return "", errors.Wrapf(err, "get %s", binlogIndexName)
	}
	defer obj.Close() //nolint:errcheck

	scanner := bufio.NewScanner(obj)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		name, ok := strings.CutPrefix(line, "./")
		if !ok || name == "" || strings.Contains(name, "/") {
			return "", errors.Errorf("binlog index entry %q has an invalid path", line)
		}
		return name, nil
	}
	if err := scanner.Err(); err != nil {
		return "", errors.Wrapf(err, "read %s", binlogIndexName)
	}

	return "", nil
}

func getBinlogMetadata(ctx context.Context, st storage.Storage, name string) (*archiveMetadata, error) {
	obj, err := st.GetObject(ctx, name+binlogMetadataExtension)
	if err != nil {
		return nil, errors.Wrap(err, "get object")
	}
	defer obj.Close() //nolint:errcheck

	entry := new(archiveMetadata)
	decoder := json.NewDecoder(obj)
	if err := decoder.Decode(entry); err != nil {
		return nil, errors.Wrap(err, "parse metadata")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse metadata: multiple JSON values")
		}
		return nil, errors.Wrap(err, "parse trailing metadata")
	}
	return entry, nil
}
