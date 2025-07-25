package version

import (
	_ "embed"
	"strings"
)

//go:generate sh -c "yq -i '.metadata.labels.\"app.kubernetes.io/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_perconaserverformysql.yaml"
//go:generate sh -c "yq -i '.metadata.labels.\"app.kubernetes.io/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_perconaserverformysqlbackups.yaml"
//go:generate sh -c "yq -i '.metadata.labels.\"app.kubernetes.io/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_perconaserverformysqlrestores.yaml"
//go:generate ./update-test-files.sh

//go:embed version.txt
var version string

func Version() string {
	return strings.TrimSpace(version)
}
