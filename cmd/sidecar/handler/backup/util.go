package backup

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func envs(cfg xb.BackupConfig) []string {
	envs := os.Environ()
	if cfg.ContainerOptions != nil {
		for _, env := range cfg.ContainerOptions.Env {
			envs = append(envs, fmt.Sprintf("%s=%s", env.Name, env.Value))
		}
	}
	return envs
}

func sanitizeCmd(cmd *exec.Cmd) string {
	sensitiveFlags := regexp.MustCompile("--password=(.*)|--.*-access-key=(.*)|--.*secret-key=(.*)")
	c := []string{cmd.Path}

	for _, arg := range cmd.Args[1:] {
		c = append(c, sensitiveFlags.ReplaceAllString(arg, ""))
	}

	return strings.Join(c, " ")
}
