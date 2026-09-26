package gtid

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
)

type interval struct {
	start int64
	end   int64
}

func (i interval) intersect(other interval) (interval, bool) {
	start := max(i.start, other.start)
	end := min(i.end, other.end)
	if start > end {
		return interval{}, false
	}
	return interval{start: start, end: end}, true
}

type source struct {
	uuid string
	tag  string
}

type Set map[source][]interval

// Intersect returns the transactions present in both sets.
func (s Set) Intersect(other Set) Set {
	result := make(Set)
	for source, leftIntervals := range s {
		rightIntervals := other[source]
		leftIndex, rightIndex := 0, 0
		for leftIndex < len(leftIntervals) && rightIndex < len(rightIntervals) {
			left := leftIntervals[leftIndex]
			right := rightIntervals[rightIndex]
			if overlap, ok := left.intersect(right); ok {
				result[source] = append(result[source], overlap)
			}

			if left.end <= right.end {
				leftIndex++
			}
			if right.end <= left.end {
				rightIndex++
			}
		}
	}
	return result
}

func (s Set) IsEmpty() bool {
	for _, intervals := range s {
		if len(intervals) > 0 {
			return false
		}
	}
	return true
}

func (s Set) String() string {
	sources := make([]source, 0, len(s))
	for source, intervals := range s {
		if len(intervals) == 0 {
			continue
		}
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(a, b source) int {
		if order := cmp.Compare(a.uuid, b.uuid); order != 0 {
			return order
		}
		return cmp.Compare(a.tag, b.tag)
	})

	entries := make([]string, 0, len(sources))
	for _, source := range sources {
		var entry strings.Builder
		entry.WriteString(source.uuid)
		if source.tag != "" {
			entry.WriteByte(':')
			entry.WriteString(source.tag)
		}
		for _, interval := range s[source] {
			entry.WriteByte(':')
			entry.WriteString(strconv.FormatInt(interval.start, 10))
			if interval.start != interval.end {
				entry.WriteByte('-')
				entry.WriteString(strconv.FormatInt(interval.end, 10))
			}
		}
		entries = append(entries, entry.String())
	}
	return strings.Join(entries, ",")
}

// normalize puts each source's intervals in order and joins those that overlap or touch
func (s Set) normalize() {
	for source, intervals := range s {
		if len(intervals) == 0 {
			continue
		}

		sorted := slices.Clone(intervals)
		slices.SortFunc(sorted, func(a, b interval) int { return cmp.Compare(a.start, b.start) })

		merged := sorted[:1]
		for _, interval := range sorted[1:] {
			last := &merged[len(merged)-1]
			if last.end == math.MaxInt64 || interval.start <= last.end+1 {
				if interval.end > last.end {
					last.end = interval.end
				}
				continue
			}
			merged = append(merged, interval)
		}
		s[source] = merged
	}
}
