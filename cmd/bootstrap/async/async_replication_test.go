package async

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	donorUUID = "9b25b15c-b021-11f1-9f18-ae185d900229"
	selfUUID  = "b7b097e0-b021-11f1-b470-525fbee38b8f"
)

type fakeSubtractor struct {
	t       *testing.T
	results map[[2]string]string
	calls   [][2]string
}

func (f *fakeSubtractor) GTIDSubtract(_ context.Context, set, other string) (string, error) {
	f.calls = append(f.calls, [2]string{set, other})

	res, ok := f.results[[2]string{set, other}]
	if !ok {
		f.t.Fatalf("unexpected GTID_SUBTRACT(%q, %q)", set, other)
	}

	return res, nil
}

func TestCloneRequired(t *testing.T) {
	tests := map[string]struct {
		local           string
		primaryExecuted string
		primaryPurged   string
		results         map[[2]string]string
		want            bool
		wantAhead       string
	}{
		"a replica that is only behind replicates": {
			local:           donorUUID + ":1-100",
			primaryExecuted: donorUUID + ":1-200",
			primaryPurged:   donorUUID + ":1-18",
			results: map[[2]string]string{
				{donorUUID + ":1-100", donorUUID + ":1-200"}: "",
				{donorUUID + ":1-18", donorUUID + ":1-100"}:  "",
			},
			want: false,
		},
		"an empty data directory clones from a primary that has purged": {
			local:           "",
			primaryExecuted: donorUUID + ":1-200",
			primaryPurged:   donorUUID + ":1-18",
			results: map[[2]string]string{
				{"", donorUUID + ":1-200"}: "",
				{donorUUID + ":1-18", ""}:  donorUUID + ":1-18",
			},
			want: true,
		},
		"an empty data directory replicates from a primary that has not purged": {
			local:           "",
			primaryExecuted: donorUUID + ":1-200",
			primaryPurged:   "",
			results: map[[2]string]string{
				{"", donorUUID + ":1-200"}: "",
				{"", ""}:                   "",
			},
			want: false,
		},
		"a fresh cluster that has executed nothing replicates": {
			local:           "",
			primaryExecuted: "",
			primaryPurged:   "",
			results: map[[2]string]string{
				{"", ""}: "",
			},
			want: false,
		},
		"a primary that purged past us clones": {
			local:           donorUUID + ":1-100",
			primaryExecuted: donorUUID + ":1-200",
			primaryPurged:   donorUUID + ":1-150",
			results: map[[2]string]string{
				{donorUUID + ":1-100", donorUUID + ":1-200"}: "",
				{donorUUID + ":1-150", donorUUID + ":1-100"}: donorUUID + ":101-150",
			},
			want: true,
		},
		"a primary that purged everything clones": {
			local:           donorUUID + ":1-40",
			primaryExecuted: donorUUID + ":1-100",
			primaryPurged:   donorUUID + ":1-100",
			results: map[[2]string]string{
				{donorUUID + ":1-40", donorUUID + ":1-100"}: "",
				{donorUUID + ":1-100", donorUUID + ":1-40"}: donorUUID + ":41-100",
			},
			want: true,
		},
		"being ahead of the primary refuses the clone": {
			local:           selfUUID + ":1-123853",
			primaryExecuted: selfUUID + ":1-50146",
			primaryPurged:   selfUUID + ":1-18",
			results: map[[2]string]string{
				{selfUUID + ":1-123853", selfUUID + ":1-50146"}: selfUUID + ":50147-123853",
			},
			wantAhead: selfUUID + ":50147-123853",
		},
		"being ahead wins over the primary having purged past us": {
			local:           selfUUID + ":1-123853",
			primaryExecuted: selfUUID + ":1-50146",
			primaryPurged:   selfUUID + ":1-50146",
			results: map[[2]string]string{
				{selfUUID + ":1-123853", selfUUID + ":1-50146"}: selfUUID + ":50147-123853",
			},
			wantAhead: selfUUID + ":50147-123853",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := &fakeSubtractor{t: t, results: tt.results}

			got, err := cloneRequired(t.Context(), s, tt.local, tt.primaryExecuted, tt.primaryPurged)

			if tt.wantAhead != "" {
				require.ErrorIs(t, err, errAheadOfPrimary)
				assert.Contains(t, err.Error(), tt.wantAhead)
				assert.False(t, got, "a refused clone must not also report that a clone is required")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCloneRequiredComparesLocalAgainstPrimary(t *testing.T) {
	s := &fakeSubtractor{
		t: t,
		results: map[[2]string]string{
			{selfUUID + ":1-100", selfUUID + ":1-200"}: "",
			{selfUUID + ":1-18", selfUUID + ":1-100"}:  "",
		},
	}

	_, err := cloneRequired(t.Context(), s, selfUUID+":1-100", selfUUID+":1-200", selfUUID+":1-18")
	require.NoError(t, err)

	require.NotEmpty(t, s.calls)
	assert.Equal(t, [2]string{selfUUID + ":1-100", selfUUID + ":1-200"}, s.calls[0])
}

func TestDonorCoversPurged(t *testing.T) {
	tests := map[string]struct {
		donorExecuted string
		primaryPurged string
		results       map[[2]string]string
		want          bool
	}{
		"a donor holding the primary's purged history can be cloned from": {
			donorExecuted: donorUUID + ":1-150",
			primaryPurged: donorUUID + ":1-100",
			results: map[[2]string]string{
				{donorUUID + ":1-100", donorUUID + ":1-150"}: "",
			},
			want: true,
		},
		"a lagging donor behind the primary's purged history can't": {
			donorExecuted: donorUUID + ":1-50",
			primaryPurged: donorUUID + ":1-100",
			results: map[[2]string]string{
				{donorUUID + ":1-100", donorUUID + ":1-50"}: donorUUID + ":51-100",
			},
			want: false,
		},
		"any donor will do when the primary has purged nothing": {
			donorExecuted: donorUUID + ":1-50",
			primaryPurged: "",
			results: map[[2]string]string{
				{"", donorUUID + ":1-50"}: "",
			},
			want: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := &fakeSubtractor{t: t, results: tt.results}

			got, err := donorCoversPurged(t.Context(), s, tt.donorExecuted, tt.primaryPurged)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

const (
	host0 = "cluster1-mysql-0.cluster1-mysql.ps-7382"
	host1 = "cluster1-mysql-1.cluster1-mysql.ps-7382"
)

func TestElectPrimary(t *testing.T) {
	behind := donorUUID + ":1-8411," + selfUUID + ":1-46241"
	ahead := donorUUID + ":1-8411," + selfUUID + ":1-123853"

	tests := map[string]struct {
		gtids   map[string]string
		results map[[2]string]string
		want    string
		wantErr bool
	}{
		"the peer holding every transaction is the primary": {
			gtids: map[string]string{host0: behind, host1: ahead},
			results: map[[2]string]string{
				{ahead, behind}: selfUUID + ":46242-123853",
				{behind, ahead}: "",
			},
			want: host1,
		},
		"peers holding the same transactions leave the choice open": {
			gtids: map[string]string{host0: behind, host1: behind},
			results: map[[2]string]string{
				{behind, behind}: "",
			},
			want: "",
		},
		"a fresh cluster leaves the choice open": {
			gtids: map[string]string{host0: "", host1: ""},
			results: map[[2]string]string{
				{"", ""}: "",
			},
			want: "",
		},
		"a single peer is the primary": {
			gtids:   map[string]string{host0: ahead},
			results: map[[2]string]string{},
			want:    host0,
		},
		"no peers leaves the choice open": {
			gtids:   map[string]string{},
			results: map[[2]string]string{},
			want:    "",
		},
		"diverged peers are an error": {
			gtids: map[string]string{host0: "aaaaaaaa-0000-0000-0000-000000000000:1-5", host1: "bbbbbbbb-0000-0000-0000-000000000000:1-3"},
			results: map[[2]string]string{
				{"bbbbbbbb-0000-0000-0000-000000000000:1-3", "aaaaaaaa-0000-0000-0000-000000000000:1-5"}: "bbbbbbbb-0000-0000-0000-000000000000:1-3",
				{"aaaaaaaa-0000-0000-0000-000000000000:1-5", "bbbbbbbb-0000-0000-0000-000000000000:1-3"}: "aaaaaaaa-0000-0000-0000-000000000000:1-5",
			},
			wantErr: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := &fakeSubtractor{t: t, results: tt.results}

			got, err := electPrimary(t.Context(), s, tt.gtids)

			if tt.wantErr {
				require.ErrorIs(t, err, errDivergedPeers)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOrderDonors(t *testing.T) {
	const (
		host2  = "cluster1-mysql-2.cluster1-mysql.ps-7382"
		behind = selfUUID + ":1-10"
		mid    = selfUUID + ":1-20"
		ahead  = selfUUID + ":1-30"
	)

	tests := map[string]struct {
		replicas []string
		fqdn     string
		gtids    map[string]string
		results  map[[2]string]string
		want     []string
	}{
		"the replica holding the most transactions comes first": {
			replicas: []string{host0, host1, host2},
			fqdn:     host0,
			gtids:    map[string]string{host0: behind, host1: mid, host2: ahead},
			results: map[[2]string]string{
				{behind, behind}: "",
				{behind, mid}:    "",
				{behind, ahead}:  "",
				{mid, behind}:    selfUUID + ":11-20",
				{ahead, mid}:     selfUUID + ":21-30",
				{mid, ahead}:     "",
			},
			want: []string{host2, host1, host0},
		},
		"a replica behind us is not a donor": {
			replicas: []string{host0, host1, host2},
			fqdn:     host0,
			gtids:    map[string]string{host0: mid, host1: behind, host2: ahead},
			results: map[[2]string]string{
				{mid, mid}:    "",
				{mid, behind}: selfUUID + ":11-20",
				{mid, ahead}:  "",
				{ahead, mid}:  selfUUID + ":21-30",
			},
			want: []string{host2, host0},
		},
		"replicas holding the same transactions keep their order": {
			replicas: []string{host0, host1},
			fqdn:     host0,
			gtids:    map[string]string{host0: mid, host1: mid},
			results: map[[2]string]string{
				{mid, mid}: "",
			},
			want: []string{host0, host1},
		},
		"no replicas": {
			replicas: []string{},
			fqdn:     host0,
			gtids:    map[string]string{},
			results:  map[[2]string]string{},
			want:     []string{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := &fakeSubtractor{t: t, results: tt.results}

			got, err := orderDonors(t.Context(), s, tt.replicas, tt.fqdn, tt.gtids)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
