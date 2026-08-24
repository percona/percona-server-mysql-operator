package clusterset

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMySQLTimediffToDuration(t *testing.T) {
	testCases := []struct {
		desc     string
		timediff string
		expected time.Duration
	}{
		{
			desc:     "seconds only",
			timediff: "00:00:05",
			expected: 5 * time.Second,
		},
		{
			desc:     "hours, minutes and seconds",
			timediff: "01:02:03",
			expected: time.Hour + 2*time.Minute + 3*time.Second,
		},
		{
			desc:     "microsecond precision",
			timediff: "00:00:01.500000",
			expected: 1500 * time.Millisecond,
		},
		{
			desc:     "no lag",
			timediff: "00:00:00",
			expected: 0,
		},
		{
			desc:     "replica ahead of source reports a negative timediff",
			timediff: "-00:00:01",
			expected: -time.Second,
		},
		{
			desc:     "empty string",
			timediff: "",
			expected: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			assert.Equal(t, tc.expected, mysqlTimediffToDuration(tc.timediff))
		})
	}
}
