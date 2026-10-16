package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realBinlog is a replica's binary log testdata/genbinlog.sh generated: replicated
// transactions, one of its own, and the Stop event of a clean shutdown at stopAt.
type realBinlog struct {
	// The server version, and the variant's mysqld options if any.
	name   string
	gtids  string
	stopAt int
	// A transaction large enough for a three byte transaction length, unless
	// compressed.
	largeTxAt          int
	largeTxLen         int
	gtidsBeforeLargeTx string
}

var realBinlogs = []realBinlog{
	{
		name:               "8.0",
		gtids:              "c001fdc3-c64f-11f1-a729-a8a159366a35:1-8,c05793e4-c64f-11f1-9fa9-a8a159366a35:1",
		stopAt:             73621,
		largeTxAt:          2132,
		largeTxLen:         70280,
		gtidsBeforeLargeTx: "c001fdc3-c64f-11f1-a729-a8a159366a35:1-4",
	},
	{
		name:               "8.4",
		gtids:              "ee61b2b7-c64f-11f1-b6f6-a8a159366a35:1-8,ee942cf4-c64f-11f1-803e-a8a159366a35:1",
		stopAt:             73622,
		largeTxAt:          2133,
		largeTxLen:         70280,
		gtidsBeforeLargeTx: "ee61b2b7-c64f-11f1-b6f6-a8a159366a35:1-4",
	},
	{
		name:               "9.7",
		gtids:              "0c64a1ae-c650-11f1-adde-a8a159366a35:1-8,0c9c7315-c650-11f1-8d3a-a8a159366a35:1",
		stopAt:             73622,
		largeTxAt:          2133,
		largeTxLen:         70280,
		gtidsBeforeLargeTx: "0c64a1ae-c650-11f1-adde-a8a159366a35:1-4",
	},
	{
		name:               "8.0-compressed",
		gtids:              "d755feb4-c64f-11f1-a4d4-a8a159366a35:1-8,d7b095f6-c64f-11f1-931c-a8a159366a35:1",
		stopAt:             2258,
		largeTxAt:          1061,
		largeTxLen:         250,
		gtidsBeforeLargeTx: "d755feb4-c64f-11f1-a4d4-a8a159366a35:1-4",
	},
	{
		name:               "8.4-compressed",
		gtids:              "fd0c7c67-c64f-11f1-ba06-a8a159366a35:1-8,fd45305c-c64f-11f1-8814-a8a159366a35:1",
		stopAt:             2263,
		largeTxAt:          1063,
		largeTxLen:         255,
		gtidsBeforeLargeTx: "fd0c7c67-c64f-11f1-ba06-a8a159366a35:1-4",
	},
	{
		name:               "9.7-compressed",
		gtids:              "1ad10d99-c650-11f1-9c63-a8a159366a35:1-8,1b0b06a1-c650-11f1-b72e-a8a159366a35:1",
		stopAt:             2279,
		largeTxAt:          1066,
		largeTxLen:         258,
		gtidsBeforeLargeTx: "1ad10d99-c650-11f1-9c63-a8a159366a35:1-4",
	},
	{
		name:               "8.4-nochecksum",
		gtids:              "2891eee3-c650-11f1-9866-a8a159366a35:1-8,28cb8b39-c650-11f1-9e4f-a8a159366a35:1",
		stopAt:             73462,
		largeTxAt:          2073,
		largeTxLen:         70260,
		gtidsBeforeLargeTx: "2891eee3-c650-11f1-9866-a8a159366a35:1-4",
	},
	{
		name: "8.4-tagged",
		gtids: "11111111-1111-1111-1111-111111111111:billing:7," +
			"b1db5063-c6d0-11f1-b35a-a8a159366a35:1-8,b1db5063-c6d0-11f1-b35a-a8a159366a35:orders:1-2," +
			"b217802a-c6d0-11f1-a06f-a8a159366a35:1,b217802a-c6d0-11f1-a06f-a8a159366a35:local:1",
		stopAt:             144776,
		largeTxAt:          73632,
		largeTxLen:         70286,
		gtidsBeforeLargeTx: "b1db5063-c6d0-11f1-b35a-a8a159366a35:1-8,b1db5063-c6d0-11f1-b35a-a8a159366a35:orders:1",
	},
	{
		name: "9.7-tagged",
		gtids: "11111111-1111-1111-1111-111111111111:billing:7," +
			"c1311efe-c6d0-11f1-9549-a8a159366a35:1-8,c1311efe-c6d0-11f1-9549-a8a159366a35:orders:1-2," +
			"c16e1ec5-c6d0-11f1-832f-a8a159366a35:1,c16e1ec5-c6d0-11f1-832f-a8a159366a35:local:1",
		stopAt:             144776,
		largeTxAt:          73632,
		largeTxLen:         70286,
		gtidsBeforeLargeTx: "c1311efe-c6d0-11f1-9549-a8a159366a35:1-8,c1311efe-c6d0-11f1-9549-a8a159366a35:orders:1",
	},
}

func (b realBinlog) read(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", "replica-"+b.name+".binlog"))
	require.NoError(t, err)

	return string(content)
}

func TestParseGTIDEvent(t *testing.T) {
	content := realBinlogs[1].read(t)

	// Values as mysqlbinlog reports them for the events at these offsets.
	tests := map[string]struct {
		at   int
		want gtidEvent
	}{
		"a one byte transaction length": {
			at:   158,
			want: gtidEvent{sid: "ee61b2b7-c64f-11f1-b6f6-a8a159366a35", gno: 1, txLen: 183},
		},
		"a two byte transaction length": {
			at:   854,
			want: gtidEvent{sid: "ee61b2b7-c64f-11f1-b6f6-a8a159366a35", gno: 4, txLen: 1279},
		},
		"a three byte transaction length": {
			at:   2133,
			want: gtidEvent{sid: "ee61b2b7-c64f-11f1-b6f6-a8a159366a35", gno: 5, txLen: 70280},
		},
		"a transaction of the replica's own carries no original commit timestamp": {
			at:   73341,
			want: gtidEvent{sid: "ee942cf4-c64f-11f1-803e-a8a159366a35", gno: 1, txLen: 281},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseGTIDEvent([]byte(content[tt.at:eventEnd(content, tt.at)]), new(sidCache))

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("an event cut before its transaction length", func(t *testing.T) {
		_, err := parseGTIDEvent([]byte(content[158:158+eventHeaderLen+gtidFixedLen+commitTimestampLen]), new(sidCache))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no transaction length")
	})

	for _, gno := range []uint64{0, math.MaxInt64} {
		t.Run(fmt.Sprintf("an invalid GNO %d", gno), func(t *testing.T) {
			event := []byte(content[158:eventEnd(content, 158)])
			binary.LittleEndian.PutUint64(event[eventHeaderLen+17:], gno)

			_, err := parseGTIDEvent(event, new(sidCache))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid GNO")
		})
	}

	t.Run("an event too short for its fixed fields", func(t *testing.T) {
		_, err := parseGTIDEvent([]byte(content[158:158+eventHeaderLen+10]), new(sidCache))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "too short")
	})
}

// putVarlen encodes v the way mysql::serialization's write_varlen_bytes_unsigned does
func putVarlen(v uint64) []byte {
	n := (bits.Len64(v)*575)>>12 + 1
	out := []byte{byte(1<<(n-1)-1) | byte(v<<n)}
	rest := v >> (8 - n + (n+7)>>4)
	for i := 1; i < n; i++ {
		out = append(out, byte(rest>>(8*(i-1))))
	}

	return out
}

func putSignedVarlen(v int64) []byte {
	return putVarlen(uint64(v<<1) ^ uint64(v>>63))
}

// taggedField is one field of a Gtid_tagged_log_event, already encoded
type taggedField struct {
	id    uint64
	value []byte
}

// taggedEvent builds a Gtid_tagged_log_event with a checksum after its body
func taggedEvent(lastNonIgnorable uint64, fields ...taggedField) []byte {
	var body []byte
	for _, f := range fields {
		body = append(body, putVarlen(f.id)...)
		body = append(body, f.value...)
	}

	// the size counts the version, itself and the field id, a byte each in
	// these small messages
	msg := append(putVarlen(serializationFormatVersion), putVarlen(uint64(3+len(body)))...)
	msg = append(msg, putVarlen(lastNonIgnorable)...)
	msg = append(msg, body...)

	e := make([]byte, eventHeaderLen, eventHeaderLen+len(msg)+4)
	e[4] = taggedGTIDEventType
	e = append(e, msg...)
	e = append(e, 0xde, 0xad, 0xbe, 0xef)
	binary.LittleEndian.PutUint32(e[9:13], uint32(len(e)))

	return e
}

func uuidField(sid [16]byte) taggedField {
	var v []byte
	for _, b := range sid {
		v = append(v, putVarlen(uint64(b))...)
	}

	return taggedField{taggedUUID, v}
}

func tagField(tag string) taggedField {
	return taggedField{taggedTag, append(putVarlen(uint64(len(tag))), tag...)}
}

func TestParseTaggedGTIDEvent(t *testing.T) {
	content := realBinlogs[len(realBinlogs)-2].read(t)

	// values as mysqlbinlog reports them for the events at these offsets
	tests := map[string]struct {
		at   int
		want gtidEvent
	}{
		"a replicated transaction": {
			at:   73341,
			want: gtidEvent{sid: "b1db5063-c6d0-11f1-b35a-a8a159366a35", tag: "orders", gno: 1, txLen: 291},
		},
		"a three byte transaction length": {
			at:   73632,
			want: gtidEvent{sid: "b1db5063-c6d0-11f1-b35a-a8a159366a35", tag: "orders", gno: 2, txLen: 70286},
		},
		"an explicit GTID": {
			at:   143918,
			want: gtidEvent{sid: "11111111-1111-1111-1111-111111111111", tag: "billing", gno: 7, txLen: 286},
		},
		"a transaction of the replica's own carries no original commit timestamp": {
			at:   144485,
			want: gtidEvent{sid: "b217802a-c6d0-11f1-a06f-a8a159366a35", tag: "local", gno: 1, txLen: 291},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseTaggedGTIDEvent([]byte(content[tt.at:eventEnd(content, tt.at)]), new(sidCache))

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	sid := [16]byte{0xaa, 0xaa, 0xaa, 0xaa, 0xbb, 0xbb, 0xcc, 0xcc, 0xdd, 0xdd, 0xee, 0xee, 0xee, 0xee, 0xee, 0xff}
	const sidText = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeff"
	gno := func(n int64) taggedField { return taggedField{taggedGNO, putSignedVarlen(n)} }
	txLen := taggedField{taggedTransactionLength, putVarlen(1000)}

	t.Run("skips the fields it does not need and a newer server's ignorable ones", func(t *testing.T) {
		got, err := parseTaggedGTIDEvent(taggedEvent(0,
			taggedField{taggedFlags, putVarlen(1)}, uuidField(sid), gno(math.MaxInt64-1), tagField("_x9"),
			taggedField{taggedLastCommitted, putSignedVarlen(-1)}, taggedField{taggedSequenceNumber, putSignedVarlen(1 << 40)},
			taggedField{taggedImmediateCommitTimestamp, putVarlen(math.MaxUint64)}, txLen,
			taggedField{taggedCommitGroupTicket + 1, putVarlen(7)}), new(sidCache))

		require.NoError(t, err)
		assert.Equal(t, gtidEvent{sid: sidText, tag: "_x9", gno: math.MaxInt64 - 1, txLen: 1000}, got)
	})

	failures := map[string]struct {
		event []byte
		want  string
	}{
		"an unknown serialization version": {
			event: func() []byte {
				e := taggedEvent(0, uuidField(sid), gno(1), txLen)
				e[eventHeaderLen] = putVarlen(2)[0]
				return e
			}(),
			want: "serialization version 2",
		},
		"a size beyond the event": {
			event: func() []byte {
				e := taggedEvent(0, uuidField(sid), gno(1), txLen)
				e[eventHeaderLen+1] = putVarlen(120)[0]
				return e
			}(),
			want: "declares 120 byte(s)",
		},
		"an unknown field it may not ignore": {
			event: taggedEvent(taggedCommitGroupTicket+2, uuidField(sid), gno(1), txLen),
			want:  "unknown field 12",
		},
		"a field cut short": {
			event: taggedEvent(0, uuidField(sid), gno(1), taggedField{taggedTransactionLength, putVarlen(1 << 40)[:2]}),
			want:  "for a 6 byte integer",
		},
		"no transaction length": {
			event: taggedEvent(0, uuidField(sid), gno(1), tagField("a")),
			want:  "lacks its UUID, GNO or transaction length",
		},
		"reordered fields": {
			event: taggedEvent(0, gno(1), uuidField(sid), txLen),
			want:  "reorders field 1",
		},
		"a UUID byte out of range": {
			event: taggedEvent(0, taggedField{taggedUUID, putVarlen(256)}, gno(1), txLen),
			want:  "UUID byte 256",
		},
		"a negative GNO": {
			event: taggedEvent(0, uuidField(sid), gno(-1), txLen),
			want:  "invalid GNO -1",
		},
		"a zero GNO": {
			event: taggedEvent(0, uuidField(sid), gno(0), txLen),
			want:  "invalid GNO 0",
		},
		"a tag that is too long": {
			event: taggedEvent(0, uuidField(sid), gno(1), tagField(strings.Repeat("a", maxTagLen+1)), txLen),
			want:  "at most 32 allowed",
		},
		"a tag MySQL would not write": {
			event: taggedEvent(0, uuidField(sid), gno(1), tagField("9lives"), txLen),
			want:  `invalid tag "9lives"`,
		},
		"a transaction shorter than its GTID event": {
			event: taggedEvent(0, uuidField(sid), gno(1), tagField("a"), taggedField{taggedTransactionLength, putVarlen(10)}),
			want:  "declares 10 byte(s), less than its GTID event",
		},
	}

	for name, tt := range failures {
		t.Run(name, func(t *testing.T) {
			_, err := parseTaggedGTIDEvent(tt.event, new(sidCache))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestVarlenDecoder(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 1<<14 - 1, 1 << 14, 1<<56 - 1, 1 << 56, 1<<63 - 1, 1 << 63, math.MaxUint64} {
		t.Run(fmt.Sprintf("unsigned %d", v), func(t *testing.T) {
			d := varlenDecoder{b: putVarlen(v)}

			assert.Equal(t, v, d.uint())
			require.NoError(t, d.err)
			assert.Equal(t, len(d.b), d.pos)
		})
	}

	for _, v := range []int64{0, 1, -1, 63, -64, 64, math.MaxInt64, math.MinInt64} {
		t.Run(fmt.Sprintf("signed %d", v), func(t *testing.T) {
			d := varlenDecoder{b: putSignedVarlen(v)}

			assert.Equal(t, v, d.int())
			require.NoError(t, d.err)
		})
	}

	t.Run("matches the readme's example", func(t *testing.T) {
		// 65535, written most significant byte first in the readme
		d := varlenDecoder{b: []byte{0xfb, 0xff, 0x07}}

		assert.Equal(t, uint64(65535), d.uint())
	})

	t.Run("an error sticks", func(t *testing.T) {
		d := varlenDecoder{b: []byte{0x03}}

		assert.Zero(t, d.uint())
		require.Error(t, d.err)
		d.b, d.pos = []byte{0x02}, 0
		assert.Zero(t, d.uint())
	})
}

func TestPackedInt(t *testing.T) {
	tests := map[string]struct {
		in   []byte
		want uint64
	}{
		"one byte":    {in: []byte{250}, want: 250},
		"two bytes":   {in: []byte{252, 0xff, 0x04}, want: 1279},
		"three bytes": {in: []byte{253, 0x88, 0x12, 0x01}, want: 70280},
		"eight bytes": {in: []byte{254, 1, 0, 0, 0, 1, 0, 0, 0}, want: 1<<32 + 1},
		"trailing bytes are ignored": {
			in:   []byte{5, 0xff, 0xff},
			want: 5,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := packedInt(tt.in)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	failures := map[string][]byte{
		"no bytes":         nil,
		"the NULL marker":  {251},
		"an invalid first": {255},
		"cut short":        {253, 1, 2},
	}

	for name, in := range failures {
		t.Run(name, func(t *testing.T) {
			_, err := packedInt(in)

			require.Error(t, err)
		})
	}
}

func TestJoinGTIDSets(t *testing.T) {
	assert.Equal(t, "", joinGTIDSets("", " \n"))
	assert.Equal(t, "a:1-3,\nb:1,c:2", joinGTIDSets("a:1-3,\nb:1\n", "", "c:2"))
}

func TestSIDCache(t *testing.T) {
	a := [uuidLen]byte{0xee, 0x61, 0xb2, 0xb7, 0xc6, 0x4f, 0x11, 0xf1, 0xb6, 0xf6, 0xa8, 0xa1, 0x59, 0x36, 0x6a, 0x35}
	var zero [uuidLen]byte
	var sids sidCache

	assert.Equal(t, "ee61b2b7-c64f-11f1-b6f6-a8a159366a35", sids.format(a))
	assert.Equal(t, "00000000-0000-0000-0000-000000000000", sids.format(zero))
	assert.Equal(t, "ee61b2b7-c64f-11f1-b6f6-a8a159366a35", sids.format(a))
}
