package orchestrator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryStarted(t *testing.T) {
	t.Run("reads the time orchestrator minted the uid at", func(t *testing.T) {
		r := Recovery{UID: "1790099186112781103:18f712a4f32216643037d562db8bb7c839f7665c039fa5968b2f21b8220abb1b"}

		started, ok := r.Started()

		require.True(t, ok)
		assert.Equal(t, time.Unix(0, 1790099186112781103), started)
	})

	t.Run("a uid without a timestamp", func(t *testing.T) {
		_, ok := Recovery{UID: "not-a-token"}.Started()

		assert.False(t, ok)
	})
}

func TestRecoveryDecodesAuditEntry(t *testing.T) {
	body := `[{"Id":5,"UID":"1790099186112781103:18f7","AnalysisEntry":{"AnalyzedInstanceKey":{"Hostname":"cluster1-mysql-1.cluster1-mysql.ps-18299","Port":3306},"Analysis":"DeadMaster","CommandHint":""},"IsActive":true,"IsSuccessful":true,"Acknowledged":false,"RecoveryEndTimestamp":"2026-09-22 18:06:27"}]`

	var recs []Recovery
	require.NoError(t, json.Unmarshal([]byte(body), &recs))

	require.Len(t, recs, 1)
	assert.Equal(t, "1790099186112781103:18f7", recs[0].UID)
	assert.Equal(t, "cluster1-mysql-1.cluster1-mysql.ps-18299", recs[0].Analysis.FailedKey.Hostname)
	assert.True(t, recs[0].IsActive)
	assert.True(t, recs[0].Ended())
}

func TestParseClaims(t *testing.T) {
	assert.Equal(t, []string{"1:a", "2:b"}, parseClaims("1:a\n\n2:b\n"))
	assert.Empty(t, parseClaims(""))
}

func TestUnacknowledgedRecoveriesEndpoint(t *testing.T) {
	assert.Equal(t, "api/audit-recovery/alias/cluster1.ps-18299?unacknowledged=true", unacknowledgedRecoveriesEndpoint("cluster1.ps-18299"))
}
