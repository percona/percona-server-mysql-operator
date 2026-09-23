package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveClaims(t *testing.T) {
	t.Run("nothing claimed yet", func(t *testing.T) {
		g := testGate(t)

		uids, err := g.liveClaims()

		require.NoError(t, err)
		assert.Empty(t, uids)
	})

	t.Run("lists the recovery holding each source", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))
		require.NoError(t, g.claim("cluster1-mysql-0.cluster1-mysql.ps-7382", "another"))

		uids, err := g.liveClaims()

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{uid, "another"}, uids)
	})

	t.Run("leaves out a claim gone idle", func(t *testing.T) {
		g := testGate(t)
		g.claimIdle = time.Millisecond
		require.NoError(t, g.claim(source, uid))

		time.Sleep(10 * time.Millisecond)

		uids, err := g.liveClaims()

		require.NoError(t, err)
		assert.Empty(t, uids, "the gate itself takes an idle claim for a recovery whose hook is gone")
	})

	t.Run("leaves out a released claim", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))
		finish(g, source, uid)

		uids, err := g.liveClaims()

		require.NoError(t, err)
		assert.Empty(t, uids)
	})
}
