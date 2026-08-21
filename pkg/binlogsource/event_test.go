package binlogsource

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The header and body fakeRotateEvent reports have to say the same thing as the
// bytes it puts on the wire, since it builds them separately.
func TestFakeRotateEventDecodesToWhatItSays(t *testing.T) {
	ev := fakeRotateEvent(999, "/var/lib/mysql/binlog.000003", 4, true)

	require.Len(t, ev.RawData, replication.EventHeaderSize+rotateHeaderLen+
		len("binlog.000003")+replication.BinlogChecksumLength)

	decoded := new(replication.EventHeader)
	require.NoError(t, decoded.Decode(ev.RawData))
	assert.Equal(t, ev.Header, decoded)

	assert.Zero(t, decoded.Timestamp, "a zero timestamp is what tells the replica the event is artificial")
	assert.Equal(t, replication.ROTATE_EVENT, decoded.EventType)
	assert.Equal(t, uint32(999), decoded.ServerID)
	assert.Equal(t, uint32(len(ev.RawData)), decoded.EventSize)
	assert.Zero(t, decoded.LogPos, "log_pos zero keeps the replica from advancing the source position")
	assert.Equal(t, replication.LOG_EVENT_ARTIFICIAL_F, decoded.Flags)

	rotate := new(replication.RotateEvent)
	body := ev.RawData[replication.EventHeaderSize : len(ev.RawData)-replication.BinlogChecksumLength]
	require.NoError(t, rotate.Decode(body))
	assert.Equal(t, ev.Event, rotate)

	assert.Equal(t, uint64(4), rotate.Position)
	// Only the base name travels: the replica has its own data directory.
	assert.Equal(t, "binlog.000003", string(rotate.NextLogName))

	// The trailer covers the header too, not just the body.
	covered := ev.RawData[:len(ev.RawData)-replication.BinlogChecksumLength]
	want := make([]byte, replication.BinlogChecksumLength)
	binary.LittleEndian.PutUint32(want, crc32.ChecksumIEEE(covered))
	assert.Equal(t, want, ev.RawData[len(covered):])
}

func TestFakeRotateEventOmitsTheChecksumWhenTheLogHasNone(t *testing.T) {
	ev := fakeRotateEvent(1, "binlog.000001", 4, false)

	assert.Len(t, ev.RawData, replication.EventHeaderSize+rotateHeaderLen+len("binlog.000001"))

	decoded := new(replication.EventHeader)
	require.NoError(t, decoded.Decode(ev.RawData))
	assert.Equal(t, uint32(len(ev.RawData)), decoded.EventSize)
}
