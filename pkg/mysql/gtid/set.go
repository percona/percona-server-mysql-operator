package gtid

import (
	"cmp"
	"math"
	"slices"
	"sort"
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

// Subtract returns the transactions in s that are absent from other.
func (s Set) Subtract(other Set) Set {
	result := make(Set)
	for source, intervals := range s {
		observed := other[source]
		i := 0
		for _, transaction := range intervals {
			start := transaction.start
			for i < len(observed) && observed[i].end < start {
				i++
			}
			covered := false
			for j := i; j < len(observed) && observed[j].start <= transaction.end; j++ {
				if observed[j].start > start {
					result[source] = append(result[source], interval{start: start, end: observed[j].start - 1})
				}
				if observed[j].end >= transaction.end {
					covered = true
					break
				}
				start = observed[j].end + 1
			}
			if !covered {
				result[source] = append(result[source], interval{start: start, end: transaction.end})
			}
		}
	}
	return result
}

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

// IsSubsetOf reports whether every transaction in s is present in other.
func (s Set) IsSubsetOf(other Set) bool {
	for source, intervals := range s {
		observed := other[source]
		i := 0
		for _, transaction := range intervals {
			for i < len(observed) && observed[i].end < transaction.start {
				i++
			}
			if i == len(observed) || observed[i].start > transaction.start || observed[i].end < transaction.end {
				return false
			}
		}
	}
	return true
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
	type entry struct {
		source    source
		intervals []interval
	}

	entries := make([]entry, 0, len(s))
	size := 0
	for source, intervals := range s {
		if len(intervals) == 0 {
			continue
		}
		entries = append(entries, entry{source: source, intervals: intervals})
		size += textLen(source, intervals)
	}
	// the commas between sources
	size += max(len(entries)-1, 0)

	slices.SortFunc(entries, func(a, b entry) int {
		if order := cmp.Compare(a.source.uuid, b.source.uuid); order != 0 {
			return order
		}
		return cmp.Compare(a.source.tag, b.source.tag)
	})

	var b strings.Builder
	b.Grow(size)

	var num [20]byte
	for i, entry := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(entry.source.uuid)
		if entry.source.tag != "" {
			b.WriteByte(':')
			b.WriteString(entry.source.tag)
		}
		for _, interval := range entry.intervals {
			b.WriteByte(':')
			b.Write(strconv.AppendInt(num[:0], interval.start, 10))
			if interval.start != interval.end {
				b.WriteByte('-')
				b.Write(strconv.AppendInt(num[:0], interval.end, 10))
			}
		}
	}
	return b.String()
}

// textLen is how many bytes String writes for source and its intervals
func textLen(source source, intervals []interval) int {
	n := len(source.uuid)
	if source.tag != "" {
		n += 1 + len(source.tag)
	}
	for _, interval := range intervals {
		n += 1 + digits(interval.start)
		if interval.start != interval.end {
			n += 1 + digits(interval.end)
		}
	}
	return n
}

// digits is how many decimal digits a positive n has
func digits(n int64) int {
	d := 1
	for n >= 10 {
		n /= 10
		d++
	}
	return d
}

// Add puts transaction gno of uuid and tag in s. uuid and tag must be in the
// lowercase form MySQL writes them and gno must be positive
func (s Set) Add(uuid, tag string, gno int64) {
	source := source{uuid: uuid, tag: tag}
	intervals := s[source]

	// the first interval gno falls in or touches, as s keeps them in order and
	// apart; the one before it ends short of gno-1
	i := sort.Search(len(intervals), func(i int) bool { return intervals[i].end >= gno-1 })
	if i == len(intervals) || intervals[i].start-1 > gno {
		s[source] = slices.Insert(intervals, i, interval{start: gno, end: gno})
		return
	}

	in := &intervals[i]
	switch {
	case gno < in.start:
		in.start = gno
	case gno > in.end:
		in.end = gno
		if i+1 < len(intervals) && intervals[i+1].start-1 == gno {
			in.end = intervals[i+1].end
			s[source] = slices.Delete(intervals, i+1, i+2)
		}
	}
}

// AddSet puts every GTID of other in s
func (s Set) AddSet(other Set) {
	for source, intervals := range other {
		if len(intervals) == 0 {
			continue
		}
		s[source] = normalizeIntervals(slices.Concat(s[source], intervals))
	}
}

// normalize puts each source's intervals in order and joins those that overlap or touch
func (s Set) normalize() {
	for source, intervals := range s {
		if len(intervals) == 0 {
			continue
		}
		s[source] = normalizeIntervals(intervals)
	}
}

func normalizeIntervals(intervals []interval) []interval {
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
	return merged
}
