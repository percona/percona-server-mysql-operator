package main

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	restclient "k8s.io/client-go/rest"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

// orcAPI answers the orchestrator API calls the hook makes through exec,
// recording the endpoints in the order they were called
type orcAPI struct {
	calls  []string
	failed map[string]bool
	bodies map[string]any
}

var endpointRe = regexp.MustCompile(`localhost:\d+/([^"]+)`)

func (o *orcAPI) Exec(_ context.Context, _ *corev1.Pod, _ string, command []string, _ io.Reader, stdout, _ io.Writer, _ bool) error {
	endpoint := endpointRe.FindStringSubmatch(command[len(command)-1])[1]
	o.calls = append(o.calls, endpoint)

	var resp any = map[string]string{"Code": "OK"}
	if body, ok := o.bodies[endpoint]; ok {
		resp = body
	}
	if o.failed[endpoint] {
		resp = map[string]string{"Code": "ERROR", "Message": "Cannot deduce instance"}
	}

	return json.NewEncoder(stdout).Encode(resp)
}

func (o *orcAPI) REST() restclient.Interface { return nil }
func (o *orcAPI) Config() *restclient.Config { return nil }

func replica(host, master string, execPos int64) *orchestrator.Instance {
	return &orchestrator.Instance{
		Key:                   orchestrator.InstanceKey{Hostname: host, Port: 3306},
		MasterKey:             orchestrator.InstanceKey{Hostname: master, Port: 3306},
		ExecBinlogCoordinates: orchestrator.BinlogCoordinates{LogFile: "binlog.000002", LogPos: execPos},
	}
}

func TestSplitCandidate(t *testing.T) {
	instances := []*orchestrator.Instance{
		replica("mysql-1", "", 0),
		replica("mysql-0", "mysql-1", 200),
		replica("mysql-2", "mysql-1", 100),
		replica("mysql-3", "mysql-0", 300),
	}
	allStarted := map[string]bool{"mysql-0": true, "mysql-2": true}

	tests := map[string]struct {
		target    string
		started   map[string]bool
		candidate string
		others    []string
	}{
		"most up to date replica of the source": {
			started:   allStarted,
			candidate: "mysql-0",
			others:    []string{"mysql-2"},
		},
		// a pod that has not passed its startup probe may be killed by it, or
		// may be crash looping, so it is no candidate but still gets banned
		"most up to date replica that has started": {
			started:   map[string]bool{"mysql-2": true},
			candidate: "mysql-2",
			others:    []string{"mysql-0"},
		},
		"explicit target": {
			target:    "mysql-2",
			started:   allStarted,
			candidate: "mysql-2",
			others:    []string{"mysql-0"},
		},
		"explicit target is taken as given": {
			target:    "mysql-2",
			candidate: "mysql-2",
			others:    []string{"mysql-0"},
		},
		"explicit target orchestrator does not list": {
			target:    "mysql-4",
			started:   allStarted,
			candidate: "mysql-4",
			others:    []string{"mysql-0", "mysql-2"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			candidate, others, err := chooseCandidate(instances, "mysql-1", tt.target, tt.started)
			require.NoError(t, err)
			require.NotNil(t, candidate)
			assert.Equal(t, tt.candidate, candidate.Hostname)

			hosts := make([]string, 0, len(others))
			for _, other := range others {
				hosts = append(hosts, other.Hostname)
			}
			assert.ElementsMatch(t, tt.others, hosts)
		})
	}

	t.Run("source without replicas", func(t *testing.T) {
		candidate, others, err := chooseCandidate(instances, "mysql-2", "", allStarted)
		require.NoError(t, err)
		assert.Nil(t, candidate)
		assert.Empty(t, others)
	})

	// exiting 0 would let orchestrator promote a replica nobody prepared
	t.Run("no replica has started", func(t *testing.T) {
		_, _, err := chooseCandidate(instances, "mysql-1", "", nil)
		require.ErrorIs(t, err, errNoStartedReplica)
	})
}

func TestMySQLStarted(t *testing.T) {
	started := func(name string, v *bool) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, Started: v}
	}
	yes, no := true, false

	tests := map[string]struct {
		statuses []corev1.ContainerStatus
		want     bool
	}{
		"started":                      {statuses: []corev1.ContainerStatus{started("xtrabackup", &yes), started("mysql", &yes)}, want: true},
		"in its startup probe":         {statuses: []corev1.ContainerStatus{started("mysql", &no)}},
		"started unknown":              {statuses: []corev1.ContainerStatus{started("mysql", nil)}},
		"only a sidecar started":       {statuses: []corev1.ContainerStatus{started("xtrabackup", &yes)}},
		"no container status reported": {},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: tt.statuses}}
			assert.Equal(t, tt.want, mysqlStarted(pod))
		})
	}
}

func TestRegisterCandidate(t *testing.T) {
	candidate := orchestrator.InstanceKey{Hostname: "mysql-0", Port: 3306}
	others := []orchestrator.InstanceKey{{Hostname: "mysql-2", Port: 3306}, {Hostname: "mysql-3", Port: 3306}}

	t.Run("bans the other replicas", func(t *testing.T) {
		api := &orcAPI{}
		require.NoError(t, registerCandidate(t.Context(), api, &corev1.Pod{}, candidate, others, orchestrator.PromotionRuleMustNot))

		assert.Equal(t, []string{
			"api/register-candidate/mysql-2/3306/must_not",
			"api/register-candidate/mysql-3/3306/must_not",
			"api/register-candidate/mysql-0/3306/prefer",
		}, api.calls)
	})

	// orchestrator would promote a replica the hook failed to ban
	t.Run("fails when a replica cannot be banned", func(t *testing.T) {
		api := &orcAPI{failed: map[string]bool{"api/register-candidate/mysql-2/3306/must_not": true}}
		require.Error(t, registerCandidate(t.Context(), api, &corev1.Pod{}, candidate, others, orchestrator.PromotionRuleMustNot))

		assert.Equal(t, []string{"api/register-candidate/mysql-2/3306/must_not"}, api.calls)
	})
}

func TestClearCandidates(t *testing.T) {
	cr := &apiv1.PerconaServerMySQL{Name: "cluster1", Namespace: "ps"}
	cr.Spec.MySQL.Size = 3

	// a host orchestrator never discovered has no rule to clear
	api := &orcAPI{failed: map[string]bool{"api/register-candidate/cluster1-mysql-1.cluster1-mysql.ps/3306/neutral": true}}
	clearCandidates(t.Context(), api, &corev1.Pod{}, cr)

	assert.Equal(t, []string{
		"api/register-candidate/cluster1-mysql-0.cluster1-mysql.ps/3306/neutral",
		"api/register-candidate/cluster1-mysql-1.cluster1-mysql.ps/3306/neutral",
		"api/register-candidate/cluster1-mysql-2.cluster1-mysql.ps/3306/neutral",
	}, api.calls)
}
