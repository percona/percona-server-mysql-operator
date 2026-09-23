package ps

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

func TestHasWritablePrimary(t *testing.T) {
	tests := map[string]struct {
		primary  orchestrator.Instance
		writable bool
	}{
		"live and writable": {
			primary:  orchestrator.Instance{Alias: "cluster1-mysql-0", IsLastCheckValid: true},
			writable: true,
		},
		"read only": {
			primary: orchestrator.Instance{Alias: "cluster1-mysql-0", IsLastCheckValid: true, ReadOnly: true},
		},
		"dead but remembered as writable": {
			primary: orchestrator.Instance{Alias: "cluster1-mysql-0"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.writable, hasWritablePrimary(&tt.primary))
		})
	}
}

func TestStaleRecoveries(t *testing.T) {
	now := time.Unix(0, 1790100000000000000)
	uidAt := func(ago time.Duration, hash string) string {
		return fmt.Sprintf("%d:%s", now.Add(-ago).UnixNano(), hash)
	}

	finished := orchestrator.Recovery{UID: uidAt(time.Hour, "finished"), IsActive: true, IsSuccessful: true, RecoveryEndTimestamp: "2026-09-22 18:06:27"}
	abandoned := orchestrator.Recovery{UID: uidAt(time.Hour, "abandoned"), IsActive: true}
	fresh := orchestrator.Recovery{UID: uidAt(time.Minute, "fresh"), IsActive: true}
	expired := orchestrator.Recovery{UID: uidAt(8*time.Hour, "expired"), RecoveryEndTimestamp: "2026-09-22 10:06:27"}
	noTime := orchestrator.Recovery{UID: "garbage", IsActive: true}

	tests := map[string]struct {
		recs        []orchestrator.Recovery
		live        []string
		claimsKnown bool
		want        []orchestrator.Recovery
	}{
		"a finished recovery whose acknowledgement was lost": {
			recs: []orchestrator.Recovery{finished},
			want: []orchestrator.Recovery{finished},
		},
		"an abandoned recovery nobody holds a claim for": {
			recs:        []orchestrator.Recovery{abandoned},
			claimsKnown: true,
			want:        []orchestrator.Recovery{abandoned},
		},
		"a recovery whose hook is still in flight": {
			recs:        []orchestrator.Recovery{abandoned},
			live:        []string{abandoned.UID},
			claimsKnown: true,
		},
		"a recovery too young to have claimed its source": {
			recs:        []orchestrator.Recovery{fresh},
			claimsKnown: true,
		},
		"an unfinished recovery while the claims are unknown": {
			recs: []orchestrator.Recovery{abandoned, finished},
			want: []orchestrator.Recovery{finished},
		},
		"a recovery out of its active period blocks nothing": {
			recs:        []orchestrator.Recovery{expired},
			claimsKnown: true,
		},
		"a uid that says nothing about its age": {
			recs:        []orchestrator.Recovery{noTime},
			claimsKnown: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			live := make(map[string]bool)
			for _, uid := range tt.live {
				live[uid] = true
			}

			assert.Equal(t, tt.want, staleRecoveries(tt.recs, live, tt.claimsKnown, now))
		})
	}
}
