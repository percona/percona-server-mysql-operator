package util

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type EnvConfig struct {
	WatchNamespace    string        `envconfig:"WATCH_NAMESPACE" required:"true"`
	OperatorNamespace *string       `envconfig:"OPERATOR_NAMESPACE"`
	LeaderElection    *bool         `envconfig:"PSO_LEADER_ELECTION_ENABLED"`
	LeaderElectionID  string        `envconfig:"PSO_LEADER_ELECTION_LEASE_NAME"`
	LeaseDuration     time.Duration `envconfig:"PSO_LEADER_ELECTION_LEASE_DURATION" default:"60s"`
	RenewDeadline     time.Duration `envconfig:"PSO_LEADER_ELECTION_RENEW_DEADLINE" default:"40s"`
	RetryPeriod       time.Duration `envconfig:"PSO_LEADER_ELECTION_RETRY_PERIOD" default:"10s"`
	Workers           *int          `envconfig:"MAX_CONCURRENT_RECONCILES"`
}

func GetEnvConfig() (EnvConfig, error) {
	var ec EnvConfig
	if err := envconfig.Process("", &ec); err != nil {
		return ec, err
	}
	return ec, nil
}
