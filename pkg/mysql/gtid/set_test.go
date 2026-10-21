package gtid

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUUIDA = "11111111-1111-1111-1111-111111111111"
	testUUIDB = "22222222-2222-2222-2222-222222222222"
)

func TestSetNormalize(t *testing.T) {
	sourceA := source{uuid: testUUIDA}
	sourceB := source{uuid: testUUIDB, tag: "blue"}
	original := []interval{
		{start: 8, end: 10},
		{start: 1, end: 5},
		{start: 4, end: 7},
		{start: 11, end: math.MaxInt64},
	}
	set := Set{
		sourceA: original,
		sourceB: nil,
	}

	set.normalize()

	assert.Equal(t, []interval{{start: 1, end: math.MaxInt64}}, set[sourceA])
	assert.Empty(t, set[sourceB])
	assert.Equal(t, []interval{
		{start: 8, end: 10},
		{start: 1, end: 5},
		{start: 4, end: 7},
		{start: 11, end: math.MaxInt64},
	}, original, "normalize must not modify an aliased interval slice")
}

func TestSetIntersect(t *testing.T) {
	tests := map[string]struct {
		left, right string
		expected    string
	}{
		"empty": {
			left:     testUUIDA + ":1-5",
			expected: "",
		},
		"different source": {
			left:     testUUIDA + ":1-5",
			right:    testUUIDB + ":1-5",
			expected: "",
		},
		"different tag": {
			left:     testUUIDA + ":1-5",
			right:    testUUIDA + ":blue:1-5",
			expected: "",
		},
		"several intervals": {
			left:     testUUIDA + ":1-5:8-12",
			right:    testUUIDA + ":3-9",
			expected: testUUIDA + ":3-5:8-9",
		},
		"shared endpoint": {
			left:     testUUIDA + ":1-5",
			right:    testUUIDA + ":5-10",
			expected: testUUIDA + ":5",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			left, err := Parse(tt.left)
			require.NoError(t, err)
			right, err := Parse(tt.right)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, left.Intersect(right).String())
			assert.Equal(t, tt.left, left.String(), "left set was modified")
			assert.Equal(t, tt.right, right.String(), "right set was modified")
		})
	}
}

func TestSetIsSubsetOf(t *testing.T) {
	t.Run("nil sets", func(t *testing.T) {
		assert.True(t, Set(nil).IsSubsetOf(nil))
	})
	t.Run("empty source intervals", func(t *testing.T) {
		assert.True(t, Set{{uuid: testUUIDA}: nil}.IsSubsetOf(nil))
	})
	t.Run("nil other", func(t *testing.T) {
		set, err := Parse(testUUIDA + ":1-10")
		require.NoError(t, err)
		assert.False(t, set.IsSubsetOf(nil))
	})
	t.Run("empty other intervals", func(t *testing.T) {
		set, err := Parse(testUUIDA + ":1-10")
		require.NoError(t, err)
		assert.False(t, set.IsSubsetOf(Set{{uuid: testUUIDA}: nil}))
	})

	tests := map[string]struct {
		set, other string
		expected   bool
	}{
		"empty sets": {set: "", other: "", expected: true},
		"empty subset": {
			other:    testUUIDA + ":1-10",
			expected: true,
		},
		"empty other": {
			set:   testUUIDA + ":1-10",
			other: "",
		},
		"equal intervals": {
			set:      testUUIDA + ":1-10",
			other:    testUUIDA + ":1-10",
			expected: true,
		},
		"contained interval": {
			set:      testUUIDA + ":3-7",
			other:    testUUIDA + ":1-10",
			expected: true,
		},
		"single transactions at endpoints": {
			set:      testUUIDA + ":1:10",
			other:    testUUIDA + ":1-10",
			expected: true,
		},
		"starts before observed": {
			set:   testUUIDA + ":1-5",
			other: testUUIDA + ":2-10",
		},
		"ends after observed": {
			set:   testUUIDA + ":5-11",
			other: testUUIDA + ":1-10",
		},
		"before observed": {
			set:   testUUIDA + ":1-5",
			other: testUUIDA + ":10-20",
		},
		"after observed": {
			set:   testUUIDA + ":11-15",
			other: testUUIDA + ":1-10",
		},
		"spans a gap": {
			set:   testUUIDA + ":3-12",
			other: testUUIDA + ":1-5:10-15",
		},
		"transaction in a gap": {
			set:   testUUIDA + ":7",
			other: testUUIDA + ":1-5:10-15",
		},
		"one observed interval covers several intervals": {
			set:      testUUIDA + ":2-4:7-9",
			other:    testUUIDA + ":1-10",
			expected: true,
		},
		"skip observed intervals": {
			set:      testUUIDA + ":5-7:20-25",
			other:    testUUIDA + ":1-2:5-10:12-15:20-30",
			expected: true,
		},
		"later interval missing": {
			set:   testUUIDA + ":1-5:10-15",
			other: testUUIDA + ":1-5",
		},
		"different source": {
			set:   testUUIDA + ":1-10",
			other: testUUIDB + ":1-10",
		},
		"different tag": {
			set:   testUUIDA + ":blue:1-10",
			other: testUUIDA + ":1-10",
		},
		"multiple sources and tags": {
			set:      testUUIDA + ":10-15," + testUUIDA + ":blue:1-5," + testUUIDB + ":3-7",
			other:    testUUIDA + ":10-20," + testUUIDA + ":blue:1-10," + testUUIDB + ":1-10",
			expected: true,
		},
		"one source incomplete": {
			set:   testUUIDA + ":1-10," + testUUIDB + ":1-10",
			other: testUUIDA + ":1-10," + testUUIDB + ":1-5",
		},
		"tags checked independently": {
			set:   testUUIDA + ":blue:1-10",
			other: testUUIDA + ":1-20," + testUUIDA + ":blue:1-5",
		},
		"extra source in other": {
			set:      testUUIDA + ":1-10",
			other:    testUUIDA + ":1-10," + testUUIDB + ":1-10",
			expected: true,
		},
		"maximum endpoint": {
			set:      testUUIDA + ":9223372036854775807",
			other:    testUUIDA + ":1-9223372036854775807",
			expected: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			set, err := Parse(tt.set)
			require.NoError(t, err)
			other, err := Parse(tt.other)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, set.IsSubsetOf(other))
			assert.Equal(t, tt.set, set.String(), "set was modified")
			assert.Equal(t, tt.other, other.String(), "other set was modified")
		})
	}
}

func TestSetIsEmpty(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.True(t, Set(nil).IsEmpty())
	})
	t.Run("empty intervals", func(t *testing.T) {
		assert.True(t, Set{{uuid: testUUIDA}: nil}.IsEmpty())
	})

	tests := map[string]struct {
		set      string
		expected bool
	}{
		"empty":       {set: "", expected: true},
		"transaction": {set: testUUIDA + ":1"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			set, err := Parse(tt.set)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, set.IsEmpty())
		})
	}
}

func TestSetString(t *testing.T) {
	set := Set{
		{uuid: testUUIDB}:               {{start: 3, end: 3}},
		{uuid: testUUIDA, tag: "blue"}:  {{start: 4, end: 7}},
		{uuid: testUUIDA}:               {{start: 1, end: 1}, {start: 9, end: 10}},
		{uuid: testUUIDA, tag: "empty"}: nil,
	}

	assert.Equal(t,
		testUUIDA+":1:9-10,"+testUUIDA+":blue:4-7,"+testUUIDB+":3",
		set.String(),
	)

	t.Run("allocates the string exactly once", func(t *testing.T) {
		for _, base := range []int64{1, 123456789, 100000000000000000} {
			set := largeSet(base)

			var text string
			allocs := testing.AllocsPerRun(10, func() { text = set.String() })

			// the sorted sources and the string
			assert.Equal(t, float64(2), allocs, "base %d", base)
			assert.NotEmpty(t, text)
		}
	})
}

func TestTextLen(t *testing.T) {
	for _, intervals := range [][]interval{
		{{start: 1, end: 1}},
		{{start: 9, end: 10}, {start: 99, end: 100}},
		{{start: 123456789, end: 123457000}, {start: 999999999, end: 999999999}},
		{{start: 1, end: math.MaxInt64}},
	} {
		for _, source := range []source{{uuid: testUUIDA}, {uuid: testUUIDA, tag: "blue"}} {
			text := Set{source: intervals}.String()

			assert.Equal(t, len(text), textLen(source, intervals), text)
		}
	}
}

// largeSet has 10 sources of 10k intervals each, two numbers from base up
func largeSet(base int64) Set {
	set := make(Set)
	for i := range 10 {
		uuid := fmt.Sprintf("%08d-1111-1111-1111-111111111111", i)
		intervals := make([]interval, 0, 10000)
		for j := range int64(10000) {
			start := base + j*10
			intervals = append(intervals, interval{start: start, end: start + j%2})
		}
		set[source{uuid: uuid}] = intervals
	}
	return set
}

func BenchmarkSetString(b *testing.B) {
	for _, base := range []int64{1, 123456789} {
		set := largeSet(base)
		b.Run(fmt.Sprintf("from %d", base), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = set.String()
			}
		})
	}
}

func TestSetAdd(t *testing.T) {
	type gtid struct {
		uuid, tag string
		gno       int64
	}

	tests := map[string]struct {
		add      []gtid
		expected string
	}{
		"empty": {
			expected: "",
		},
		"consecutive gnos make a range": {
			add:      []gtid{{testUUIDA, "", 5}, {testUUIDA, "", 6}, {testUUIDA, "", 7}},
			expected: testUUIDA + ":5-7",
		},
		"gaps split ranges": {
			add:      []gtid{{testUUIDA, "", 1}, {testUUIDA, "", 2}, {testUUIDA, "", 4}, {testUUIDA, "", 6}, {testUUIDA, "", 7}},
			expected: testUUIDA + ":1-2:4:6-7",
		},
		"out of order and repeated gnos": {
			add:      []gtid{{testUUIDA, "", 3}, {testUUIDA, "", 1}, {testUUIDA, "", 2}, {testUUIDA, "", 2}},
			expected: testUUIDA + ":1-3",
		},
		"gno filling a gap joins its neighbours": {
			add:      []gtid{{testUUIDA, "", 1}, {testUUIDA, "", 3}, {testUUIDA, "", 2}},
			expected: testUUIDA + ":1-3",
		},
		"gno just before a range extends it": {
			add:      []gtid{{testUUIDA, "", 5}, {testUUIDA, "", 9}, {testUUIDA, "", 8}, {testUUIDA, "", 4}},
			expected: testUUIDA + ":4-5:8-9",
		},
		"gno inside a range changes nothing": {
			add:      []gtid{{testUUIDA, "", 1}, {testUUIDA, "", 2}, {testUUIDA, "", 3}, {testUUIDA, "", 2}},
			expected: testUUIDA + ":1-3",
		},
		"gno before every range": {
			add:      []gtid{{testUUIDA, "", 7}, {testUUIDA, "", 9}, {testUUIDA, "", 2}},
			expected: testUUIDA + ":2:7:9",
		},
		"gno between ranges it does not touch": {
			add:      []gtid{{testUUIDA, "", 1}, {testUUIDA, "", 9}, {testUUIDA, "", 5}},
			expected: testUUIDA + ":1:5:9",
		},
		"each source and tag has its own ranges": {
			add: []gtid{
				{testUUIDB, "", 1},
				{testUUIDA, "", 1},
				{testUUIDA, "x", 1},
				{testUUIDA, "", 2},
				{testUUIDA, "y", 5},
				{testUUIDA, "x", 2},
			},
			expected: testUUIDA + ":1-2," + testUUIDA + ":x:1-2," + testUUIDA + ":y:5," + testUUIDB + ":1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			set := make(Set)
			for _, g := range tt.add {
				set.Add(g.uuid, g.tag, g.gno)
			}

			assert.Equal(t, tt.expected, set.String())
		})
	}

	t.Run("extends a parsed set", func(t *testing.T) {
		set, err := Parse(testUUIDA + ":1-3:7")
		require.NoError(t, err)

		set.Add(testUUIDA, "", 4)

		assert.Equal(t, testUUIDA+":1-4:7", set.String())
	})

	t.Run("a range that ends at the largest GNO", func(t *testing.T) {
		set, err := Parse(testUUIDA + ":5-9223372036854775807")
		require.NoError(t, err)

		set.Add(testUUIDA, "", 7)
		set.Add(testUUIDA, "", 4)
		set.Add(testUUIDA, "", 1)

		assert.Equal(t, testUUIDA+":1:4-9223372036854775807", set.String())
	})

	t.Run("matches normalizing every gno at once", func(t *testing.T) {
		r := rand.New(rand.NewPCG(1, 2))
		for range 200 {
			set := make(Set)
			var all []interval
			for range r.IntN(50) {
				gno := r.Int64N(40) + 1
				set.Add(testUUIDA, "", gno)
				all = append(all, interval{start: gno, end: gno})
			}

			want := make(Set)
			if len(all) > 0 {
				want[source{uuid: testUUIDA}] = normalizeIntervals(all)
			}
			require.Equal(t, want.String(), set.String())
		}
	})
}

func TestSetAddSet(t *testing.T) {
	set, err := Parse(testUUIDA + ":1-3:7," + testUUIDB + ":5")
	require.NoError(t, err)
	other, err := Parse(testUUIDA + ":4-5:9," + testUUIDA + ":x:1")
	require.NoError(t, err)

	set.AddSet(other)

	assert.Equal(t, testUUIDA+":1-5:7:9,"+testUUIDA+":x:1,"+testUUIDB+":5", set.String())
	assert.Equal(t, testUUIDA+":4-5:9,"+testUUIDA+":x:1", other.String())
}

func TestSetSubtract(t *testing.T) {
	tests := map[string]struct {
		target, observed, expected string
	}{
		"empty target": {
			observed: testUUIDA + ":1-10",
		},
		"empty observed": {
			target:   testUUIDA + ":1-10",
			expected: testUUIDA + ":1-10",
		},
		"holes": {
			target:   testUUIDA + ":1-35",
			observed: testUUIDA + ":1-10:20-30",
			expected: testUUIDA + ":11-19:31-35",
		},
		"before observed": {
			target:   testUUIDA + ":1-10",
			observed: testUUIDA + ":20-30",
			expected: testUUIDA + ":1-10",
		},
		"observed spans intervals": {
			target:   testUUIDA + ":1-5:10-15",
			observed: testUUIDA + ":3-12",
			expected: testUUIDA + ":1-2:13-15",
		},
		"maximum endpoint": {
			target:   testUUIDA + ":1-9223372036854775807",
			observed: testUUIDA + ":2-9223372036854775807",
			expected: testUUIDA + ":1",
		},
		"at boundary": {
			target:   testUUIDA + ":1-10",
			observed: testUUIDA + ":1-10",
		},
		"past boundary": {
			target:   testUUIDA + ":5-15",
			observed: testUUIDA + ":1-10",
			expected: testUUIDA + ":11-15",
		},
		"multiple intervals": {
			target:   testUUIDA + ":5-15:20-25",
			observed: testUUIDA + ":1-10",
			expected: testUUIDA + ":11-15:20-25",
		},
		"independent sources": {
			target:   testUUIDA + ":1-10," + testUUIDB + ":1-10",
			observed: testUUIDA + ":1-10," + testUUIDB + ":1-5",
			expected: testUUIDB + ":6-10",
		},
		"independent tags": {
			target:   testUUIDA + ":blue:1-10",
			observed: testUUIDA + ":1-20," + testUUIDA + ":blue:1-5",
			expected: testUUIDA + ":blue:6-10",
		},
		"unknown source": {
			target:   testUUIDB + ":1-10",
			observed: testUUIDA + ":1-20",
			expected: testUUIDB + ":1-10",
		},
		"maximum transaction": {
			target:   testUUIDA + ":1-9223372036854775807",
			observed: testUUIDA + ":1-9223372036854775807",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			target, err := Parse(tt.target)
			require.NoError(t, err)
			observed, err := Parse(tt.observed)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, target.Subtract(observed).String())
			assert.Equal(t, tt.target, target.String(), "target set was modified")
			assert.Equal(t, tt.observed, observed.String(), "observed set was modified")
		})
	}
}
