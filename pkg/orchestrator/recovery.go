package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

// RecoveryClaimIdle is how long the pre-failover hook's claim on a source
// outlives its last heartbeat. A claim idle for longer belongs to a recovery
// whose hook is gone, and both the hook's gate and the operator treat that
// recovery as over.
const RecoveryClaimIdle = 5 * time.Minute

// Recovery is an entry of orchestrator's recovery audit.
type Recovery struct {
	UID      string `json:"UID"`
	Analysis struct {
		FailedKey   InstanceKey `json:"AnalyzedInstanceKey"`
		Analysis    string      `json:"Analysis"`
		CommandHint string      `json:"CommandHint"`
	} `json:"AnalysisEntry"`
	IsActive             bool   `json:"IsActive"`
	IsSuccessful         bool   `json:"IsSuccessful"`
	Acknowledged         bool   `json:"Acknowledged"`
	RecoveryEndTimestamp string `json:"RecoveryEndTimestamp"`
}

// Ended reports whether orchestrator got as far as resolving the recovery,
// promoted or not. A recovery abandoned by its pre-failover hook never does.
func (r Recovery) Ended() bool {
	return r.RecoveryEndTimestamp != ""
}

// Started returns when orchestrator registered the recovery. The uid carries
// it: the audit's own start timestamp is rewritten whenever an orchestrator
// rebuilds its database from the raft log.
func (r Recovery) Started() (time.Time, bool) {
	nanos, _, ok := strings.Cut(r.UID, ":")
	if !ok {
		return time.Time{}, false
	}

	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, false
	}

	return time.Unix(0, n), true
}

// UnacknowledgedRecoveries returns the cluster's recoveries nobody has
// acknowledged yet, newest first.
func UnacknowledgedRecoveries(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, clusterHint string) ([]Recovery, error) {
	var res, errb bytes.Buffer
	if err := exec(ctx, cliCmd, pod, unacknowledgedRecoveriesEndpoint(clusterHint), &res, &errb); err != nil {
		return nil, err
	}

	var recs []Recovery
	if err := json.Unmarshal(res.Bytes(), &recs); err == nil {
		return recs, nil
	}

	orcResp := new(orcResponse)
	if err := unmarshalOrcResponse(res.Bytes(), orcResp); err != nil {
		return nil, err
	}

	return nil, orcResp.Error()
}

// unacknowledgedRecoveriesEndpoint looks the cluster up by alias: its name is
// the primary's address, and a recovery keeps the name the cluster had when it
// started.
func unacknowledgedRecoveriesEndpoint(clusterHint string) string {
	return fmt.Sprintf("api/audit-recovery/alias/%s?unacknowledged=true", clusterHint)
}

// LiveClaims returns the recoveries whose pre-failover hook is still in flight
// on the orchestrator in pod.
func LiveClaims(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod) ([]string, error) {
	var outb, errb bytes.Buffer
	c := []string{handlerBinary, "claims"}
	if err := cliCmd.Exec(ctx, pod, AppName, c, nil, &outb, &errb, false); err != nil {
		return nil, errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", c, outb.String(), errb.String())
	}

	return parseClaims(outb.String()), nil
}

func parseClaims(out string) []string {
	var uids []string
	for line := range strings.SplitSeq(out, "\n") {
		if uid := strings.TrimSpace(line); uid != "" {
			uids = append(uids, uid)
		}
	}

	return uids
}
