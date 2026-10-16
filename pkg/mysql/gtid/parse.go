package gtid

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

const whitespaceChars = " \t\r\n\f\v"

func Parse(s string) (Set, error) {
	set := make(Set)
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.Trim(entry, whitespaceChars)
		if entry == "" {
			continue
		}
		if err := set.parseEntry(entry); err != nil {
			return nil, errors.Wrap(err, "parse entry")
		}
	}

	set.normalize()
	return set, nil
}

func (s Set) parseEntry(entry string) error {
	sourceField, fields, found := strings.Cut(entry, ":")
	if !found {
		return errors.Errorf("malformed GTID set entry %q", entry)
	}

	sourceUUID, err := parseSourceUUID(sourceField)
	if err != nil {
		return errors.Wrap(err, "parse source uuid")
	}

	currentSource := source{uuid: sourceUUID}
	tagNeedsInterval := false
	for field := range strings.SplitSeq(fields, ":") {
		field = strings.Trim(field, whitespaceChars)
		if field == "" || field[0] < '0' || field[0] > '9' {
			if tagNeedsInterval {
				return errors.Errorf("tag %q has no GTID interval", currentSource.tag)
			}
			if !validTag(field) {
				return errors.Errorf("malformed GTID tag %q", field)
			}
			currentSource.tag = strings.ToLower(field)
			tagNeedsInterval = true
			continue
		}

		interval, err := parseGTIDInterval(field)
		if err != nil {
			return errors.Wrap(err, "parse gtid interval")
		}
		s[currentSource] = append(s[currentSource], interval)
		tagNeedsInterval = false
	}
	if tagNeedsInterval {
		return errors.Errorf("tag %q has no GTID interval", currentSource.tag)
	}
	return nil
}

func parseSourceUUID(value string) (string, error) {
	value = strings.Trim(value, whitespaceChars)
	id, err := uuid.Parse(value)
	if err != nil {
		return "", errors.Wrapf(err, "malformed GTID source %q", value)
	}

	if id == uuid.Nil {
		return "", errors.Errorf("malformed GTID source %q", value)
	}
	return id.String(), nil
}

func parseGTIDInterval(s string) (interval, error) {
	start, end, ranged := strings.Cut(s, "-")
	first, err := parseGTIDNumber(start)
	if err != nil {
		return interval{}, errors.Wrapf(err, "malformed GTID interval %q", s)
	}
	if first == 0 {
		return interval{}, errors.Errorf("malformed GTID interval %q: transaction number must be positive", s)
	}
	if !ranged {
		return interval{start: first, end: first}, nil
	}

	last, err := parseGTIDNumber(end)
	if err != nil || last <= first {
		return interval{}, errors.Errorf("malformed GTID interval %q", s)
	}

	return interval{start: first, end: last}, nil
}

func parseGTIDNumber(s string) (int64, error) {
	s = strings.Trim(s, whitespaceChars)
	value, err := strconv.ParseUint(s, 10, 63)
	return int64(value), err
}

func validTag(tag string) bool {
	if len(tag) == 0 || len(tag) > 32 || !isTagStart(tag[0]) {
		return false
	}
	for i := 1; i < len(tag); i++ {
		if !isTagStart(tag[i]) && (tag[i] < '0' || tag[i] > '9') {
			return false
		}
	}
	return true
}

func isTagStart(character byte) bool {
	return character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}
