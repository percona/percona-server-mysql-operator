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
	MinTimestamp  string  `json:"min_timestamp"`
	MaxTimestamp  string  `json:"max_timestamp"`
}

// ValidateTarget rejects targets outside the archive's bounds or containing unknown source UUIDs.
func ValidateTarget(ctx context.Context, st storage.Storage, restore *apiv1.PerconaServerMySQLRestore) error {
	subcommand, targetArg, err := SearchArgs(restore)
	if err != nil {
		return errors.Wrap(ErrInvalidTarget, err.Error())
	}
	names, err := binlogNames(ctx, st)
	if err != nil {
		return errors.Wrap(err, "read binlog index")
	}
	if len(names) == 0 {
		return errors.New("binlog archive is empty; cannot validate PITR target")
	}
	switch subcommand {
	case SearchByTimestampCommand:
		return validateTimestampTarget(ctx, st, names, targetArg)
	case SearchByGTIDCommand:
		return validateGTIDTarget(ctx, st, names, targetArg)
	default:
		return errors.Wrapf(ErrInvalidTarget, "unknown search command %s", subcommand)
	}
}

// validateTimestampTarget checks the oldest and latest archived timestamps.
func validateTimestampTarget(ctx context.Context, st storage.Storage, names []string, targetArg string) error {
	target, err := time.Parse(binlogTimestampLayout, targetArg)
	if err != nil {
		return errors.Wrapf(ErrInvalidTarget, "parse pitr target %s: %v", targetArg, err)
	}

	name := names[0]
	metadata, err := getBinlogMetadata(ctx, st, name)
	if err != nil {
		return errors.Wrapf(err, "read metadata of binlog %s", name)
	}
	if metadata.MinTimestamp == "" {
		return errors.Errorf("binlog %s has no min_timestamp", name)
	}
	oldest, err := time.Parse(binlogTimestampLayout, metadata.MinTimestamp)
	if err != nil {
		return errors.Wrapf(err, "parse min_timestamp %s", metadata.MinTimestamp)
	}
	if target.Before(oldest) {
		return errors.Wrapf(ErrTargetNotCovered,
			"timestamp is too old, the archive starts at %s", metadata.MinTimestamp)
	}
	latestName := names[len(names)-1]
	// Read the archive tail only when the oldest binlog cannot prove coverage.
	if latestName != name {
		latest, err := time.Parse(binlogTimestampLayout, metadata.MaxTimestamp)
		if err != nil {
			return errors.Wrap(err, "parse max_timestamp")
		}
		if !latest.Before(oldest) && !target.After(latest) {
			return nil
		}
		metadata, err = getBinlogMetadata(ctx, st, latestName)
		if err != nil {
			return errors.Wrapf(err, "read metadata of binlog %s", latestName)
		}
	}
	if metadata.MaxTimestamp == "" {
		return errors.Errorf("binlog %s has no max_timestamp", latestName)
	}
	latest, err := time.Parse(binlogTimestampLayout, metadata.MaxTimestamp)
	if err != nil {
		return errors.Wrapf(err, "parse max_timestamp %s", metadata.MaxTimestamp)
	}
	if latest.Before(oldest) {
		return errors.New("binlog archive has inconsistent timestamp bounds")
	}
	if target.After(latest) {
		return errors.Wrapf(ErrTargetNotCovered, "timestamp is too new, the archive ends at %s", metadata.MaxTimestamp)
	}
	return nil
}

func validateGTIDTarget(ctx context.Context, st storage.Storage, names []string, targetArg string) error {
	target, err := gtid.Parse(targetArg)
	if err != nil {
		return errors.Wrapf(ErrInvalidTarget, "parse pitr target: %v", err)
	}
	readMetadata := func(name string) (gtid.Set, gtid.Set, error) {
		metadata, err := getBinlogMetadata(ctx, st, name)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "read metadata of binlog %s", name)
		}
		if metadata.PreviousGTIDs == nil || metadata.AddedGTIDs == nil {
			return nil, nil, errors.Errorf("binlog %s has incomplete GTID metadata", name)
		}
		previous, err := gtid.Parse(*metadata.PreviousGTIDs)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "parse previous_gtids of binlog %s", name)
		}
		added, err := gtid.Parse(*metadata.AddedGTIDs)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "parse added_gtids of binlog %s", name)
		}
		if previous.IsEmpty() && added.IsEmpty() {
			return nil, nil, errors.Errorf("binlog %s has empty GTID metadata", name)
		}
		return previous, added, nil
	}

	previous, added, err := readMetadata(names[0])
	if err != nil {
		return err
	}
	if missing := target.Intersect(previous); !missing.IsEmpty() {
		return errors.Wrapf(ErrTargetNotCovered, "the specified GTID set predates the binlog archive, which is missing %s", missing)
	}
	if target.IsSubsetOf(added) {
		return nil
	}

	latestName := names[len(names)-1]
	if latestName != names[0] {
		previous, added, err = readMetadata(latestName)
		if err != nil {
			return err
		}
	}
	if missing := target.Subtract(previous).Subtract(added); !missing.IsEmpty() {
		return errors.Wrapf(ErrTargetNotCovered, "the specified GTID set exceeds the binlog archive, which is missing %s", missing)
	}
	return nil
}

func binlogNames(ctx context.Context, st storage.Storage) ([]string, error) {
	obj, err := st.GetObject(ctx, binlogIndexName)
	if err != nil {
		return nil, errors.Wrapf(err, "get %s", binlogIndexName)
	}
	defer obj.Close() //nolint:errcheck
	var names []string
	scanner := bufio.NewScanner(obj)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		name, ok := strings.CutPrefix(line, "./")
		if !ok || name == "" || strings.Contains(name, "/") {
			return nil, errors.Errorf("binlog index entry %q has an invalid path", line)
		}
		names = append(names, name)
	}
	return names, errors.Wrap(scanner.Err(), "read binlog index")
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
