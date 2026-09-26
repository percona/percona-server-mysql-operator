package gtid

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGTIDSet(t *testing.T) {
	const (
		uuidA = "11111111-1111-1111-1111-111111111111"
		uuidB = "22222222-2222-2222-2222-222222222222"
	)
	tests := map[string]struct {
		in          string
		expected    string
		expectedErr string
	}{
		// Valid sets.
		"empty":                       {in: "", expected: ""},
		"single transaction":          {in: uuidA + ":7", expected: uuidA + ":7"},
		"interval":                    {in: uuidA + ":1-5", expected: uuidA + ":1-5"},
		"several intervals":           {in: uuidA + ":1-5:8:11-12", expected: uuidA + ":1-5:8:11-12"},
		"several sources":             {in: uuidB + ":1-2," + uuidA + ":1", expected: uuidA + ":1," + uuidB + ":1-2"},
		"tagged":                      {in: uuidA + ":blue:1-5:8", expected: uuidA + ":blue:1-5:8"},
		"several tags":                {in: uuidA + ":1-2," + uuidA + ":blue:3-4," + uuidA + ":green:5", expected: uuidA + ":1-2," + uuidA + ":blue:3-4," + uuidA + ":green:5"},
		"several tags in entry":       {in: uuidA + ":blue:1-2:green:3-4", expected: uuidA + ":blue:1-2," + uuidA + ":green:3-4"},
		"tag after untagged interval": {in: uuidA + ":1-2:blue:3-4", expected: uuidA + ":1-2," + uuidA + ":blue:3-4"},

		// Input forms and normalization.
		"case is not relevant":     {in: strings.ToUpper(uuidA) + ":1-5", expected: uuidA + ":1-5"},
		"noncanonical UUID":        {in: strings.ReplaceAll(uuidA, "-", "") + ":1", expected: uuidA + ":1"},
		"unsorted intervals":       {in: uuidA + ":8:1-5", expected: uuidA + ":1-5:8"},
		"touching intervals":       {in: uuidA + ":1-5:6-9", expected: uuidA + ":1-9"},
		"overlapping":              {in: uuidA + ":1-5:3-9", expected: uuidA + ":1-9"},
		"tag normalized":           {in: uuidA + ":Domain_1:1-5", expected: uuidA + ":domain_1:1-5"},
		"server wrapped":           {in: uuidA + ":1-5,\n" + uuidB + ":1-2", expected: uuidA + ":1-5," + uuidB + ":1-2"},
		"whitespace around tokens": {in: " \t" + uuidA + " : blue : 1 - 5 \r\n", expected: uuidA + ":blue:1-5"},
		"redundant commas":         {in: ",," + uuidA + ":1,,", expected: uuidA + ":1"},

		// Invalid sets.
		"no interval":                          {in: uuidA, expectedErr: "malformed GTID set entry"},
		"tag without interval":                 {in: uuidA + ":blue", expectedErr: "has no GTID interval"},
		"backwards interval":                   {in: uuidA + ":5-1", expectedErr: "malformed GTID interval"},
		"invalid UUID":                         {in: "uuid-a:1", expectedErr: "malformed GTID source"},
		"zero UUID":                            {in: "00000000-0000-0000-0000-000000000000:1", expectedErr: "malformed GTID source"},
		"transaction zero":                     {in: uuidA + ":0", expectedErr: "must be positive"},
		"signed range end":                     {in: uuidA + ":1-+2", expectedErr: "malformed GTID interval"},
		"invalid tag start":                    {in: uuidA + ":1blue:1", expectedErr: "malformed GTID interval"},
		"invalid tag character":                {in: uuidA + ":bl-ue:1", expectedErr: "malformed GTID tag"},
		"shell metacharacters in tag":          {in: uuidA + ":blue$(id):1", expectedErr: "malformed GTID tag"},
		"tag too long":                         {in: uuidA + ":" + strings.Repeat("a", 33) + ":1", expectedErr: "malformed GTID tag"},
		"empty tag":                            {in: uuidA + "::1", expectedErr: "malformed GTID tag"},
		"tag without interval before next tag": {in: uuidA + ":blue:green:3-4", expectedErr: "has no GTID interval"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			set, err := Parse(tt.in)
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, set.String())
		})
	}
}

func TestNormalizeIntervalsAtMaxTransactionNumber(t *testing.T) {
	const uuidA = "11111111-1111-1111-1111-111111111111"
	set, err := Parse(uuidA + ":1-" + strconv.FormatInt(math.MaxInt64, 10) + ":2")
	require.NoError(t, err)
	assert.Equal(t, uuidA+":1-"+strconv.FormatInt(math.MaxInt64, 10), set.String())
}

func TestGTIDSetIntersection(t *testing.T) {
	const (
		uuidA = "11111111-1111-1111-1111-111111111111"
		uuidB = "22222222-2222-2222-2222-222222222222"
	)
	tests := map[string]struct {
		a, b     string
		expected string
	}{
		"disjoint sources": {
			a: uuidA + ":1-5", b: uuidB + ":1-5",
			expected: "",
		},
		"identical": {
			a: uuidA + ":1-5", b: uuidA + ":1-5",
			expected: uuidA + ":1-5",
		},
		"b inside a": {
			a: uuidA + ":1-10", b: uuidA + ":4-6",
			expected: uuidA + ":4-6",
		},
		"b overlaps the start of a": {
			a: uuidA + ":5-10", b: uuidA + ":1-6",
			expected: uuidA + ":5-6",
		},
		"b overlaps the end of a": {
			a: uuidA + ":1-6", b: uuidA + ":5-10",
			expected: uuidA + ":5-6",
		},
		"b covers a": {
			a: uuidA + ":4-6", b: uuidA + ":1-10",
			expected: uuidA + ":4-6",
		},
		"empty b": {
			a: uuidA + ":1-5", b: "",
			expected: "",
		},
		"several intervals": {
			a: uuidA + ":1-5:8-12", b: uuidA + ":3-9",
			expected: uuidA + ":3-5:8-9",
		},
		"same tag": {
			a: uuidA + ":blue:1-10", b: uuidA + ":blue:4-6",
			expected: uuidA + ":blue:4-6",
		},
		"different tags": {
			a: uuidA + ":blue:1-5", b: uuidA + ":green:1-5",
			expected: "",
		},
		"tag case is not relevant": {
			a: uuidA + ":BLUE:1-10", b: uuidA + ":blue:4-6",
			expected: uuidA + ":blue:4-6",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			a, err := Parse(tt.a)
			require.NoError(t, err)
			b, err := Parse(tt.b)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, a.Intersect(b).String())
		})
	}
}

func TestGTIDSetIntersectionDoesNotMutate(t *testing.T) {
	const (
		uuidA = "11111111-1111-1111-1111-111111111111"
		uuidB = "22222222-2222-2222-2222-222222222222"
	)
	a, err := Parse(uuidA + ":1-10")
	require.NoError(t, err)
	b, err := Parse(uuidA + ":4-6," + uuidB + ":1")
	require.NoError(t, err)

	a.Intersect(b)

	assert.Equal(t, uuidA+":1-10", a.String())
	assert.Equal(t, uuidA+":4-6,"+uuidB+":1", b.String())
}
