package innodbcluster

import (
	"fmt"

	"github.com/pkg/errors"
)

var ErrMemberNotFound error = errors.New("member not found")

type ClusterStatus string

const (
	ClusterStatusOK                   ClusterStatus = "OK"
	ClusterStatusOKPartial            ClusterStatus = "OK_PARTIAL"
	ClusterStatusOKNoTolerance        ClusterStatus = "OK_NO_TOLERANCE"
	ClusterStatusOKNoTolerancePartial ClusterStatus = "OK_NO_TOLERANCE_PARTIAL"
	ClusterStatusNoQuorum             ClusterStatus = "NO_QUORUM"
	ClusterStatusOffline              ClusterStatus = "OFFLINE"
	ClusterStatusError                ClusterStatus = "ERROR"
	ClusterStatusUnreachable          ClusterStatus = "UNREACHABLE"
	ClusterStatusUnknown              ClusterStatus = "UNKNOWN"
	ClusterStatusFenced               ClusterStatus = "FENCED_WRITES"
)

type MemberState string

const (
	MemberStateOnline      MemberState = "ONLINE"
	MemberStateOffline     MemberState = "OFFLINE"
	MemberStateRecovering  MemberState = "RECOVERING"
	MemberStateUnreachable MemberState = "UNREACHABLE"
	MemberStateError       MemberState = "ERROR"
	MemberStateMissing     MemberState = "(MISSING)"
)

type Member struct {
	Address        string      `json:"address"`
	MemberState    MemberState `json:"status"`
	InstanceErrors []string    `json:"instanceErrors"`
}

type Status struct {
	ClusterName       string `json:"clusterName"`
	DefaultReplicaSet struct {
		Primary    string            `json:"primary"`
		SSL        string            `json:"ssl"`
		Status     ClusterStatus     `json:"status"`
		StatusText string            `json:"statusText"`
		Topology   map[string]Member `json:"topology"`
	} `json:"defaultReplicaSet"`
}

func (s Status) String() string {
	status := fmt.Sprintf(`
ClusterName: %s
Status: %s
StatusText: %s
SSL: %s
Primary: %s
Topology:
	`,
		s.ClusterName,
		s.DefaultReplicaSet.Status,
		s.DefaultReplicaSet.StatusText,
		s.DefaultReplicaSet.SSL,
		s.DefaultReplicaSet.Primary,
	)

	i := 0
	for _, member := range s.DefaultReplicaSet.Topology {
		status += fmt.Sprintf(`
	Member %d
	Address: %s
	State: %s
	Errors: %v

		`, i, member.Address, member.MemberState, member.InstanceErrors)
		i++
	}

	return status
}
