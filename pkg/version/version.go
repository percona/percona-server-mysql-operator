package version

import _ "embed"

//go:generate sh -c "yq -i '.metadata.labels.\"mysql.percona.com/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_perconaserverformysql.yaml"
//go:generate sh -c "yq -i '.metadata.labels.\"mysql.percona.com/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_perconaserverformysqlbackups.yaml"
//go:generate sh -c "yq -i '.metadata.labels.\"mysql.percona.com/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_perconaserverformysqlrestores.yaml"

//go:embed version.txt
var Version string
