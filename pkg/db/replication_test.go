package db

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandGTIDSet(t *testing.T) {
	const uuid = "3e11fa47-71ca-11e1-9e33-c80aa9429562"
	const uuid2 = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeffff"

	t.Run("single gtid", func(t *testing.T) {
		gtids, err := expandGTIDSet(uuid + ":42")
		require.NoError(t, err)
		assert.Equal(t, []string{uuid + ":42"}, gtids)
	})

	t.Run("interval", func(t *testing.T) {
		gtids, err := expandGTIDSet(uuid + ":1-3")
		require.NoError(t, err)
		assert.Equal(t, []string{uuid + ":1", uuid + ":2", uuid + ":3"}, gtids)
	})

	t.Run("multiple intervals and uuids", func(t *testing.T) {
		gtids, err := expandGTIDSet(uuid + ":1-2:5," + uuid2 + ":7")
		require.NoError(t, err)
		assert.Equal(t, []string{uuid + ":1", uuid + ":2", uuid + ":5", uuid2 + ":7"}, gtids)
	})

	t.Run("empty set", func(t *testing.T) {
		gtids, err := expandGTIDSet("")
		require.NoError(t, err)
		assert.Empty(t, gtids)
	})

	t.Run("malformed uuid", func(t *testing.T) {
		_, err := expandGTIDSet("not-a-uuid:1")
		assert.Error(t, err)
	})

	t.Run("malformed interval", func(t *testing.T) {
		_, err := expandGTIDSet(uuid + ":x-3")
		assert.Error(t, err)
	})

	t.Run("reversed interval", func(t *testing.T) {
		_, err := expandGTIDSet(uuid + ":5-3")
		assert.Error(t, err)
	})

	t.Run("over the injection cap", func(t *testing.T) {
		_, err := expandGTIDSet(fmt.Sprintf("%s:1-%d", uuid, maxInjectableGTIDs+1))
		assert.Error(t, err)
	})
}
