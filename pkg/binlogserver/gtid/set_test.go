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

func TestSetSubtract(t *testing.T) {
	sourceA := source{uuid: testUUIDA}
	taggedA := source{uuid: testUUIDA, tag: "blue"}
	sourceB := source{uuid: testUUIDB}
	tests := map[string]struct {
		target, observed, expected Set
	}{
		"empty target":             {target: nil, observed: Set{sourceA: {{start: 1, end: 10}}}, expected: Set{}},
		"empty observed":           {target: Set{sourceA: {{start: 1, end: 10}}}, expected: Set{sourceA: {{start: 1, end: 10}}}},
		"holes":                    {target: Set{sourceA: {{start: 1, end: 35}}}, observed: Set{sourceA: {{start: 1, end: 10}, {start: 20, end: 30}}}, expected: Set{sourceA: {{start: 11, end: 19}, {start: 31, end: 35}}}},
		"before observed":          {target: Set{sourceA: {{start: 1, end: 10}}}, observed: Set{sourceA: {{start: 20, end: 30}}}, expected: Set{sourceA: {{start: 1, end: 10}}}},
		"observed spans intervals": {target: Set{sourceA: {{start: 1, end: 5}, {start: 10, end: 15}}}, observed: Set{sourceA: {{start: 3, end: 12}}}, expected: Set{sourceA: {{start: 1, end: 2}, {start: 13, end: 15}}}},
		"maximum endpoint":         {target: Set{sourceA: {{start: 1, end: math.MaxInt64}}}, observed: Set{sourceA: {{start: 2, end: math.MaxInt64}}}, expected: Set{sourceA: {{start: 1, end: 1}}}},
		"at boundary":              {target: Set{sourceA: {{start: 1, end: 10}}}, observed: Set{sourceA: {{start: 1, end: 10}}}, expected: Set{}},
		"past boundary":            {target: Set{sourceA: {{start: 5, end: 15}}}, observed: Set{sourceA: {{start: 1, end: 10}}}, expected: Set{sourceA: {{start: 11, end: 15}}}},
		"multiple intervals":       {target: Set{sourceA: {{start: 5, end: 15}, {start: 20, end: 25}}}, observed: Set{sourceA: {{start: 1, end: 10}}}, expected: Set{sourceA: {{start: 11, end: 15}, {start: 20, end: 25}}}},
		"independent sources":      {target: Set{sourceA: {{start: 1, end: 10}}, sourceB: {{start: 1, end: 10}}}, observed: Set{sourceA: {{start: 1, end: 10}}, sourceB: {{start: 1, end: 5}}}, expected: Set{sourceB: {{start: 6, end: 10}}}},
		"independent tags":         {target: Set{taggedA: {{start: 1, end: 10}}}, observed: Set{sourceA: {{start: 1, end: 20}}, taggedA: {{start: 1, end: 5}}}, expected: Set{taggedA: {{start: 6, end: 10}}}},
		"unknown source":           {target: Set{sourceB: {{start: 1, end: 10}}}, observed: Set{sourceA: {{start: 1, end: 20}}}, expected: Set{sourceB: {{start: 1, end: 10}}}},
		"maximum transaction":      {target: Set{sourceA: {{start: 1, end: math.MaxInt64}}}, observed: Set{sourceA: {{start: 1, end: math.MaxInt64}}}, expected: Set{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			before := tt.target.String()
			observedBefore := tt.observed.String()
			assert.Equal(t, tt.expected, tt.target.Subtract(tt.observed))
			assert.Equal(t, before, tt.target.String())
			assert.Equal(t, observedBefore, tt.observed.String())
		})
	}
}
