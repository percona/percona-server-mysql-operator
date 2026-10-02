package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunFinishRejectsBadInput(t *testing.T) {
	err := runFinish(t.Context(), []string{"-uid", uid})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source flag should not be empty")
}

func TestFinish(t *testing.T) {
	t.Run("frees the source for the next recovery", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, uid))

		finish(g, source, uid)

		require.NoError(t, g.claim(source, "another"))
	})

	t.Run("leaves a claim held by another recovery alone", func(t *testing.T) {
		g := testGate(t)
		require.NoError(t, g.claim(source, "another"))

		finish(g, source, uid)

		require.ErrorIs(t, g.claim(source, uid), errClaimed)
	})

	t.Run("is harmless for a source nobody claimed", func(t *testing.T) {
		g := testGate(t)

		finish(g, source, uid)
	})
}
