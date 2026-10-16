package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
)

const (
	stopEventType          = 3
	gtidEventType          = 33
	anonymousGTIDEventType = 34
	taggedGTIDEventType    = 42

	// flags, sid, gno, lt_type, last_committed and sequence_number
	gtidFixedLen = 1 + 16 + 8 + 1 + 8 + 8

	commitTimestampLen = 7
	// Set on the immediate commit timestamp when the original one follows it.
	originalTimestampFlag = uint64(1) << 55

	// Far more than any GTID event needs, so a larger one is garbage.
	maxGTIDEventSize = 1 << 12

	serializationFormatVersion = 1
	uuidLen                    = 16
	maxTagLen                  = 32
)

// field ids of a Gtid_tagged_log_event, in the order MySQL writes them
const (
	taggedFlags = iota
	taggedUUID
	taggedGNO
	taggedTag
	taggedLastCommitted
	taggedSequenceNumber
	taggedImmediateCommitTimestamp
	taggedOriginalCommitTimestamp
	taggedTransactionLength
	taggedImmediateServerVersion
	taggedOriginalServerVersion
	taggedCommitGroupTicket
)

var errUnsupportedGTID = errors.New("unsupported GTID event")

type gtidEvent struct {
	// The UUID of the server that first committed the transaction.
	sid string
	// empty unless the transaction was committed with a tagged GTID
	tag string
	// The transaction's sequence number among those sharing its sid.
	gno uint64
	// The whole transaction's size in bytes, this event included.
	txLen uint64
}

// sidCache keeps the last server UUID it formatted, which a run of transactions
// mostly shares
type sidCache struct {
	uuid [uuidLen]byte
	sid  string
}

func (c *sidCache) format(uuid [uuidLen]byte) string {
	if c.sid == "" || uuid != c.uuid {
		c.uuid = uuid
		c.sid = fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
	}

	return c.sid
}

// parseGTIDEvent reads a Gtid_log_event, header included.
func parseGTIDEvent(e []byte, sids *sidCache) (gtidEvent, error) {
	b := e[eventHeaderLen:]
	if len(b) < gtidFixedLen+commitTimestampLen {
		return gtidEvent{}, fmt.Errorf("GTID event of %d byte(s) is too short", len(e))
	}
	ev := gtidEvent{
		sid: sids.format([uuidLen]byte(b[1:17])),
		gno: binary.LittleEndian.Uint64(b[17:25]),
	}
	if err := checkGNO(ev); err != nil {
		return gtidEvent{}, err
	}

	// parsing timestamps to find the position of transaction length
	p := gtidFixedLen
	immediate := uint64(0)
	for i := range commitTimestampLen {
		immediate |= uint64(b[p+i]) << (8 * i)
	}
	p += commitTimestampLen
	if immediate&originalTimestampFlag != 0 {
		p += commitTimestampLen
	}
	if p >= len(b) {
		return gtidEvent{}, fmt.Errorf("GTID event for %s:%d has no transaction length", ev.sid, ev.gno)
	}

	txLen, err := packedInt(b[p:])
	if err != nil {
		return gtidEvent{}, fmt.Errorf("transaction length of %s:%d: %w", ev.sid, ev.gno, err)
	}
	if txLen < uint64(len(e)) {
		return gtidEvent{}, fmt.Errorf("transaction %s:%d declares %d byte(s), less than its GTID event", ev.sid, ev.gno, txLen)
	}
	ev.txLen = txLen

	return ev, nil
}

// parseTaggedGTIDEvent reads a Gtid_tagged_log_event, header included
func parseTaggedGTIDEvent(e []byte, sids *sidCache) (gtidEvent, error) {
	b := e[eventHeaderLen:]

	d := varlenDecoder{b: b}
	if version := d.uint(); d.err == nil && version != serializationFormatVersion {
		return gtidEvent{}, fmt.Errorf("tagged GTID event has serialization version %d", version)
	}
	// the size covers the whole message but not the checksum
	size := d.uint()
	lastNonIgnorable := d.uint()
	if d.err != nil {
		return gtidEvent{}, fmt.Errorf("tagged GTID event header: %w", d.err)
	}
	if size < uint64(d.pos) || size > uint64(len(b)) {
		return gtidEvent{}, fmt.Errorf("tagged GTID event declares %d byte(s) in a %d byte body", size, len(b))
	}
	if lastNonIgnorable > taggedCommitGroupTicket+1 {
		return gtidEvent{}, fmt.Errorf("tagged GTID event needs unknown field %d", lastNonIgnorable-1)
	}
	d.b = b[:size]

	var ev gtidEvent
	var hasUUID, hasGNO, hasTxLen bool
	next := uint64(0)

	for d.pos < len(d.b) && d.err == nil {
		id := d.uint()
		if d.err != nil {
			break
		}
		if id < next {
			return gtidEvent{}, fmt.Errorf("tagged GTID event repeats or reorders field %d", id)
		}
		next = id + 1

		switch id {
		case taggedUUID:
			var uuid [uuidLen]byte
			for i := range uuid {
				v := d.uint()
				if v > math.MaxUint8 {
					return gtidEvent{}, fmt.Errorf("tagged GTID event has UUID byte %d", v)
				}
				uuid[i] = byte(v)
			}
			ev.sid = sids.format(uuid)
			hasUUID = true
		case taggedGNO:
			gno := d.int()
			if gno < 0 {
				return gtidEvent{}, fmt.Errorf("tagged GTID event has invalid GNO %d", gno)
			}
			ev.gno, hasGNO = uint64(gno), true
		case taggedTag:
			ev.tag = d.string(maxTagLen)
		case taggedLastCommitted, taggedSequenceNumber:
			d.int()
		case taggedTransactionLength:
			ev.txLen, hasTxLen = d.uint(), true
		case taggedFlags, taggedImmediateCommitTimestamp, taggedOriginalCommitTimestamp,
			taggedImmediateServerVersion, taggedOriginalServerVersion, taggedCommitGroupTicket:
			d.uint()
		default:
			// a newer server's ignorable field, which MySQL skips along with
			// whatever follows it
			d.pos = len(d.b)
		}
	}
	if d.err != nil {
		return gtidEvent{}, fmt.Errorf("tagged GTID event: %w", d.err)
	}

	if !hasUUID || !hasGNO || !hasTxLen {
		return gtidEvent{}, errors.New("tagged GTID event lacks its UUID, GNO or transaction length")
	}
	if err := checkGNO(ev); err != nil {
		return gtidEvent{}, err
	}
	if !validTag(ev.tag) {
		return gtidEvent{}, fmt.Errorf("GTID event for %s:%d has invalid tag %q", ev.sid, ev.gno, ev.tag)
	}
	if ev.txLen < uint64(len(e)) {
		return gtidEvent{}, fmt.Errorf("transaction %s declares %d byte(s), less than its GTID event", ev, ev.txLen)
	}

	return ev, nil
}

// checkGNO rejects a GNO outside the range MySQL accepts
func checkGNO(ev gtidEvent) error {
	if ev.gno < 1 || ev.gno >= math.MaxInt64 {
		return fmt.Errorf("GTID event for %s has invalid GNO %d", ev.sid, ev.gno)
	}

	return nil
}

// validTag reports whether tag is empty or one MySQL would have written:
// lowercase letters, digits and underscores, not starting with a digit
func validTag(tag string) bool {
	for i, c := range tag {
		letter := c >= 'a' && c <= 'z' || c == '_'
		digit := i > 0 && c >= '0' && c <= '9'
		if !letter && !digit {
			return false
		}
	}

	return true
}

func (ev gtidEvent) String() string {
	if ev.tag == "" {
		return fmt.Sprintf("%s:%d", ev.sid, ev.gno)
	}

	return fmt.Sprintf("%s:%s:%d", ev.sid, ev.tag, ev.gno)
}

// varlenDecoder reads mysql::serialization values. the first error sticks and
// every later read returns zero
type varlenDecoder struct {
	b   []byte
	pos int
	err error
}

// uint decodes a variable-length unsigned integer: the trailing ones of the
// first byte count the bytes that follow it
func (d *varlenDecoder) uint() uint64 {
	if d.err != nil {
		return 0
	}
	if d.pos >= len(d.b) {
		d.err = errors.New("no bytes for an integer")
		return 0
	}

	first := d.b[d.pos]
	n := bits.TrailingZeros8(^first) + 1
	if d.pos+n > len(d.b) {
		d.err = fmt.Errorf("%d byte(s) for a %d byte integer", len(d.b)-d.pos, n)
		return 0
	}

	rest := uint64(0)
	for i := 1; i < n; i++ {
		// little-endian
		rest |= uint64(d.b[d.pos+i]) << (8 * (i - 1))
	}
	// the first byte holds the lowest 8-n bits, none at all when n is 9
	shift := max(8-n, 0)

	d.pos += n
	return uint64(first)>>n | rest<<shift
}

// int decodes a variable-length signed integer, which keeps its sign in the
// lowest bit
func (d *varlenDecoder) int() int64 {
	u := d.uint()
	return int64(u>>1) ^ -int64(u&1)
}

// string decodes a length followed by that many bytes
func (d *varlenDecoder) string(maxLen uint64) string {
	n := d.uint()
	if d.err != nil {
		return ""
	}
	if n > maxLen || n > uint64(len(d.b)-d.pos) {
		d.err = fmt.Errorf("string of %d byte(s) with %d left, at most %d allowed", n, len(d.b)-d.pos, maxLen)
		return ""
	}

	s := string(d.b[d.pos : d.pos+int(n)])
	d.pos += int(n)
	return s
}

// packedInt decodes a length-encoded integer.
func packedInt(b []byte) (uint64, error) {
	if len(b) == 0 {
		return 0, errors.New("no bytes")
	}

	var n int
	switch first := b[0]; {
	case first < 251:
		return uint64(first), nil
	case first == 252:
		n = 2
	case first == 253:
		n = 3
	case first == 254:
		n = 8
	default:
		return 0, fmt.Errorf("invalid first byte %#x", first)
	}

	if len(b) < 1+n {
		return 0, fmt.Errorf("%d byte(s) for a %d byte integer", len(b)-1, n)
	}

	v := uint64(0)
	for i := range n {
		// little-endian, shifting left
		v |= uint64(b[1+i]) << (8 * i)
	}

	return v, nil
}

// joinGTIDSets unions GTID sets in text form, leaving the merging to MySQL.
func joinGTIDSets(sets ...string) string {
	parts := make([]string, 0, len(sets))
	for _, s := range sets {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}

	return strings.Join(parts, ",")
}
