package gr

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-ini/ini"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
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

func setMultiPrimary(opts *createClusterOpts, myCnf *ini.Section) error {
	option := "group_replication_single_primary_mode"

	value, err := util.GetKeyValue(myCnf, option)
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

	value, err := util.GetKeyValue(myCnf, option)
	if err != nil {
		return errors.Wrapf(err, "get %s", option)
	}

	switch strings.ToUpper(value) {
	case "", "ON", "TRUE", "1":
		opts.paxosSingleLeader = true
	default:
		opts.paxosSingleLeader = false
	}

	return nil
}

func setCommunicationStack(opts *createClusterOpts, myCnf *ini.Section) error {
	option := "group_replication_communication_stack"

	value, err := util.GetKeyValue(myCnf, option)
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
