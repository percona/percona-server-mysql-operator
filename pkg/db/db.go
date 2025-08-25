package db

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

var sensitiveRegexp = regexp.MustCompile(":.*@")

type db struct {
	client clientcmd.Client
	pod    *corev1.Pod
	user   apiv1.SystemUser
	pass   string
	host   string
}

func newDB(pod *corev1.Pod, cliCmd clientcmd.Client, user apiv1.SystemUser, pass, host string) *db {
	return &db{client: cliCmd, pod: pod, user: user, pass: pass, host: host}
}

func (d *db) exec(ctx context.Context, stm string, stdout, stderr *bytes.Buffer) error {
	cmd := []string{"mysql", "--database", "performance_schema", fmt.Sprintf("-p%s", d.pass), "-u", string(d.user), "-h", d.host, "-e", stm}

	err := d.client.Exec(ctx, d.pod, "mysql", cmd, nil, stdout, stderr, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(stdout.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(stderr.String(), ":*****@")
		return errors.Wrapf(err, "stdout: %s, stderr: %s", sout, serr)
	}

	if strings.Contains(stderr.String(), "ERROR") {
		return fmt.Errorf("sql error: %s", stderr)
	}

	return nil
}
