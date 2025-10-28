package gr

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/go-ini/ini"
	"github.com/pkg/errors"
)

// these are options that mysql-shell overwrites
// without taking my.cnf into consideration
type createClusterOpts struct {
	force              bool
	multiPrimary       bool
	paxosSingleLeader  bool
	communicationStack string
}

func (o *createClusterOpts) String() string {
	return fmt.Sprintf(
		`{"force": %s, "multiPrimary": %s, "paxosSingleLeader": %s, "communicationStack": "%s"}`,
		strconv.FormatBool(o.force), strconv.FormatBool(o.multiPrimary),
		strconv.FormatBool(o.paxosSingleLeader), o.communicationStack,
	)
}

const customMyCnfPath = "/etc/mysql/config/my-config.cnf"

func parseMyCnf(myCnfFile io.ReadCloser) (*ini.Section, error) {
	myCnf, err := ini.LoadSources(ini.LoadOptions{AllowBooleanKeys: true}, myCnfFile)
	if err != nil {
		return nil, errors.Wrapf(err, "load %s", customMyCnfPath)
	}

	section, err := myCnf.GetSection("")
	if myCnf.HasSection("mysqld") {
		section, err = myCnf.GetSection("mysqld")
	}
	if err != nil {
		return nil, errors.Wrap(err, "get section")
	}

	return section, nil
}

func getKeyValue(myCnf *ini.Section, option string) (string, error) {
	var key *ini.Key
	var err error

	if myCnf.HasKey(option) {
		key, err = myCnf.GetKey(option)
	} else if myCnf.HasKey("loose_" + option) {
		key, err = myCnf.GetKey("loose_" + option)
	}
	if err != nil {
		return "", errors.Wrapf(err, "get %s", option)
	}

	if key == nil {
		return "", nil
	}

	return key.Value(), nil
}

func setMultiPrimary(opts *createClusterOpts, myCnf *ini.Section) error {
	option := "group_replication_single_primary_mode"

	value, err := getKeyValue(myCnf, option)
	if err != nil {
		return errors.Wrapf(err, "get %s", option)
	}

	switch strings.ToUpper(value) {
	case "OFF", "FALSE", "0":
		opts.force = true
		opts.multiPrimary = true
	default:
		opts.multiPrimary = false
	}

	return nil
}

func setPaxosSingleLeader(opts *createClusterOpts, myCnf *ini.Section) error {
	option := "group_replication_paxos_single_leader"

	value, err := getKeyValue(myCnf, option)
	if err != nil {
		return errors.Wrapf(err, "get %s", option)
	}

	switch strings.ToUpper(value) {
	case "ON", "TRUE", "1":
		opts.paxosSingleLeader = true
	default:
		opts.paxosSingleLeader = false
	}

	return nil
}

func setCommunicationStack(opts *createClusterOpts, myCnf *ini.Section) error {
	option := "group_replication_communication_stack"

	value, err := getKeyValue(myCnf, option)
	if err != nil {
		return errors.Wrapf(err, "get %s", option)
	}

	switch {
	case value != "":
		opts.communicationStack = value
	default:
		opts.communicationStack = "MYSQL"
	}

	return nil
}

func getCreateClusterOpts(myCnf *ini.Section) (*createClusterOpts, error) {
	// defaults
	opts := &createClusterOpts{
		multiPrimary:       false,
		paxosSingleLeader:  true,
		communicationStack: "MYSQL",
	}

	if myCnf == nil {
		return opts, nil
	}

	if err := setMultiPrimary(opts, myCnf); err != nil {
		return opts, errors.Wrap(err, "multiPrimary")
	}

	if err := setPaxosSingleLeader(opts, myCnf); err != nil {
		return opts, errors.Wrap(err, "paxosSingleLeader")
	}

	if err := setCommunicationStack(opts, myCnf); err != nil {
		return opts, errors.Wrap(err, "communicationStack")
	}

	return opts, nil
}
