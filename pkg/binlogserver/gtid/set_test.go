package gtid

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
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
	untaggedA := source{uuid: testUUIDA}
	taggedA := source{uuid: testUUIDA, tag: "blue"}
	sourceB := source{uuid: testUUIDB}

	tests := map[string]struct {
		left, right Set
		expected    Set
	}{
		"empty": {
			left:     Set{untaggedA: {{start: 1, end: 5}}},
			expected: Set{},
		},
		"different source": {
			left:     Set{untaggedA: {{start: 1, end: 5}}},
			right:    Set{sourceB: {{start: 1, end: 5}}},
			expected: Set{},
		},
		"different tag": {
			left:     Set{untaggedA: {{start: 1, end: 5}}},
			right:    Set{taggedA: {{start: 1, end: 5}}},
			expected: Set{},
		},
		"several intervals": {
			left: Set{untaggedA: {
				{start: 1, end: 5},
				{start: 8, end: 12},
			}},
			right:    Set{untaggedA: {{start: 3, end: 9}}},
			expected: Set{untaggedA: {{start: 3, end: 5}, {start: 8, end: 9}}},
		},
		"shared endpoint": {
			left:     Set{untaggedA: {{start: 1, end: 5}}},
			right:    Set{untaggedA: {{start: 5, end: 10}}},
			expected: Set{untaggedA: {{start: 5, end: 5}}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			leftBefore := tt.left.String()
			rightBefore := tt.right.String()

			assert.Equal(t, tt.expected, tt.left.Intersect(tt.right))
			assert.Equal(t, leftBefore, tt.left.String(), "left set was modified")
			assert.Equal(t, rightBefore, tt.right.String(), "right set was modified")
		})
	}
}

func TestSetIsEmpty(t *testing.T) {
	tests := map[string]struct {
		set      Set
		expected bool
	}{
		"nil":             {set: nil, expected: true},
		"empty":           {set: Set{}, expected: true},
		"empty intervals": {set: Set{{uuid: testUUIDA}: nil}, expected: true},
		"transaction":     {set: Set{{uuid: testUUIDA}: {{start: 1, end: 1}}}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.set.IsEmpty())
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
}
