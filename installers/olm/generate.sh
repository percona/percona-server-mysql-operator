#!/usr/bin/env bash

# Install
# brew install gawk coreutils jq
for command in gawk gcsplit jq; do
	if ! command -v $command &>/dev/null; then
		echo "Error: $command is not installed. Please install it: brew install $command" >&2
		exit 1
	fi
done

set -eu

# sed -i behaves differently on macOS (BSD sed) and Linux (GNU sed):
#   BSD: sed -i '' 'script' file   (requires explicit empty-string backup suffix)
#   GNU: sed -i 'script' file      (no suffix argument)
sed_inplace() {
	if sed --version 2>/dev/null | grep -q GNU; then
		sed -i "$@"
	else
		sed -i '' "$@"
	fi
}

log() {
	echo >&2 "[olm] $*"
}

abort() {
	echo >&2 "$@"
	exit 1
}

DISTRIBUTION="$1"

cd "${BASH_SOURCE[0]%/*}"

repo_root="$(cd ../.. && pwd)"
bundle_directory="bundles/${DISTRIBUTION}"
project_directory="projects/${DISTRIBUTION}"

if [ -f "distributions/${DISTRIBUTION}.sh" ]; then
	# shellcheck source=/dev/null
	source "distributions/${DISTRIBUTION}.sh"
fi

# The 'operators.operatorframework.io.bundle.package.v1' package name for each
# bundle (updated for the 'redhat' bundle).
package_name='percona-server-mysql-operator'

# The project name used by operator-sdk for initial bundle generation.
project_name='percona-server-mysql-operator'

# The prefix for the 'clusterserviceversion.yaml' file.
# Per OLM guidance, the filename for the clusterserviceversion.yaml must be prefixed
# with the Operator's package name for the 'redhat' bundle.
# https://github.com/redhat-openshift-ecosystem/certification-releases/blob/main/4.9/ga/troubleshooting.md#get-supported-versions
file_name='percona-server-mysql-operator'

# Always generate both clusterPermissions (from cw-rbac.yaml) and permissions (from rbac.yaml).
# Parse deploy files directly into temporary files (don't modify config/ originals)
yq eval '. | select(.kind == "Deployment")' "../../deploy/operator.yaml" >operator_deployments.yaml
yq eval '. | select(.kind == "ServiceAccount")' "../../deploy/rbac.yaml" >operator_accounts.yaml
yq eval '. | select(.kind == "ClusterRole")' "../../deploy/cw-rbac.yaml" >operator_cluster_roles.yaml
yq eval '. | select(.kind == "Role")' "../../deploy/rbac.yaml" >operator_ns_roles.yaml

update_yaml_images() {
	local yaml_file="$1"

	if [ ! -f "$yaml_file" ]; then
		echo "Error: File '$yaml_file' does not exist."
		return 1
	fi

	local temp_file
	temp_file=$(mktemp)

	sed -E 's/(("image":|containerImage:|image:)[ ]*"?)([^"]+)("?)/\1docker.io\/\3\4/g' "$yaml_file" >"$temp_file"
	mv "$temp_file" "$yaml_file"

	echo "File '$yaml_file' updated successfully."
}

## Create the Operator SDK project (for bundle metadata only).

[ ! -d "${project_directory}" ] || rm -r "${project_directory}"
install -d "${project_directory}"
(
	cd "${project_directory}"
	operator-sdk init --fetch-deps='false' --project-name=${project_name}
)

# Recreate the OLM bundle.
[ ! -d "${bundle_directory}" ] || rm -r "${bundle_directory}"
install -d \
	"${bundle_directory}/manifests" \
	"${bundle_directory}/metadata"

# Render bundle annotations and strip comments.
# Per Red Hat we should not include the org.opencontainers annotations in the
# 'redhat' annotations.yaml file, so only add them for 'community'.
# - https://coreos.slack.com/team/UP1LZCC1Y

export package="${package_name}"
export package_channel="${PACKAGE_CHANNEL}"
export openshift_supported_versions="${OPENSHIFT_VERSIONS}"

yq eval '.annotations["operators.operatorframework.io.bundle.channels.v1"] = env(package_channel) |
         .annotations["operators.operatorframework.io.bundle.channel.default.v1"] = env(package_channel) |
         .annotations["com.redhat.openshift.versions"] = env(openshift_supported_versions)' \
	bundle.annotations.yaml >"${bundle_directory}/metadata/annotations.yaml"

if [ "${DISTRIBUTION}" == 'community' ]; then
	# community-operators
	yq eval --inplace '
	.annotations["operators.operatorframework.io.bundle.package.v1"] = "percona-server-mysql-operator" |
    .annotations["org.opencontainers.image.authors"] = "info@percona.com" |
    .annotations["org.opencontainers.image.url"] = "https://percona.com" |
     .annotations["org.opencontainers.image.vendor"] = "Percona"' \
		"${bundle_directory}/metadata/annotations.yaml"

# certified-operators
elif [ "${DISTRIBUTION}" == 'redhat' ]; then
	yq eval --inplace '
    .annotations["operators.operatorframework.io.bundle.package.v1"] = "percona-server-mysql-operator-certified" ' \
		"${bundle_directory}/metadata/annotations.yaml"

fi

labels=$(yq eval -r '.annotations | to_entries | map("LABEL " + .key + "=" + (.value | tojson)) | join("\n")' \
	"${bundle_directory}/metadata/annotations.yaml")

labels="${labels}
LABEL com.redhat.delivery.backport=true
LABEL com.redhat.delivery.operator.bundle=true"

LABELS="${labels}" envsubst <bundle.Dockerfile >"${bundle_directory}/Dockerfile"

awk '{gsub(/^[ \t]+/, "    "); print}' "${bundle_directory}/Dockerfile" >"${bundle_directory}/Dockerfile.new" && mv "${bundle_directory}/Dockerfile.new" "${bundle_directory}/Dockerfile"

# Include CRDs as manifests.
crd_names=$(yq eval -o=tsv '.metadata.name' ../../deploy/crd.yaml)

gawk -v names="${crd_names}" -v bundle_directory="${bundle_directory}" '
BEGIN {
    split(names, name_array, " ");
    idx=1;
}
/apiVersion: apiextensions.k8s.io\/v1/ {
    if (idx in name_array) {
        current_file = bundle_directory "/manifests/" name_array[idx] ".crd.yaml";
        idx++;
    } else {
        current_file = bundle_directory "/unnamed_" idx ".yaml";
        idx++;
    }
}
{
    if (current_file != "") {
        print > current_file;
    }
}
' ../../deploy/crd.yaml

while IFS= read -r f; do
	sed_inplace '1s/^/---\n/; ${/^---$/d;}' "$f"
done < <(find "${bundle_directory}/manifests" -type f -name "*.crd.yaml")

dump() { yq --color-output; }

# The first command render yaml correctly and the second extract data.

yq eval -i '[.]' operator_deployments.yaml && yq eval 'length == 1' operator_deployments.yaml --exit-status >/dev/null || abort "too many deployments!" $'\n'"$(yq eval . operator_deployments.yaml)"

yq eval -i '[.]' operator_accounts.yaml && yq eval 'length == 1' operator_accounts.yaml --exit-status >/dev/null || abort "too many service accounts!" $'\n'"$(yq eval . operator_accounts.yaml)"

# Wrap roles into arrays
yq eval-all '[.]' operator_cluster_roles.yaml >operator_cluster_roles_arr.yaml && mv operator_cluster_roles_arr.yaml operator_cluster_roles.yaml
yq eval-all '[.]' operator_ns_roles.yaml >operator_ns_roles_arr.yaml && mv operator_ns_roles_arr.yaml operator_ns_roles.yaml

# Render bundle CSV and strip comments.
export stem=$(yq -r '.projectName' "${project_directory}/PROJECT")
export version="${VERSION}"
export timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
export name="${stem}.v${VERSION}"
export name_certified="${stem}-certified.v${VERSION}"
export skip_range="<v${VERSION}"
export containerImage=$(yq eval '.[0].spec.template.spec.containers[0].image' operator_deployments.yaml)
export deployment=$(yq eval operator_deployments.yaml)
export account=$(yq eval '.[0] | .metadata.name' operator_accounts.yaml)
export clusterRules=$(yq eval '[.[] | {"serviceAccountName": strenv(account), "rules": .rules}]' operator_cluster_roles.yaml)
export nsRules=$(yq eval '[.[] | {"serviceAccountName": strenv(account), "rules": .rules}]' operator_ns_roles.yaml)

if [ "${DISTRIBUTION}" == "redhat" ]; then
	redhat_images="$(build_redhat_related_images)"
	# Rendered as YAML (not compact JSON) so `spec.relatedImages` lists each
	# image on its own line instead of a single flow-style line.
	export relatedImages=$(jq -c '.relatedImages' <<<"${redhat_images}" | yq -o=yaml -P '.')
	export redhatOperatorImage=$(jq -r '.operatorImage' <<<"${redhat_images}")
else
	export relatedImages='[]'
fi

examples_json=$(jq -n "[
  $(yq eval -o=json ../../deploy/cr.yaml),
  $(yq eval -o=json ../../deploy/backup/backup.yaml),
  $(yq eval -o=json ../../deploy/backup/restore.yaml)
]")

if [ "${DISTRIBUTION}" == "redhat" ]; then
	examples_json="$(rewrite_cr_example_images "${examples_json}" "${redhat_images}")"
fi

export examples="${examples_json}"

yq eval '
  .metadata.annotations["alm-examples"] = strenv(examples) |
  .metadata.annotations["alm-examples"] style="literal" |
  .metadata.annotations["containerImage"] = env(containerImage) |
  .metadata.annotations["olm.skipRange"] = env(skip_range) |
  .metadata.annotations["createdAt"] = strenv(timestamp) |
  .metadata.name = env(name) |
  .spec.install.spec.clusterPermissions = env(clusterRules) |
  .spec.install.spec.permissions = env(nsRules) |
  .spec.install.spec.deployments = [( env(deployment) | .[] |{ "name": .metadata.name, "spec": .spec} )] |
  .spec.version = env(version)' bundle.csv.yaml >"${bundle_directory}/manifests/${file_name}.v${VERSION}.clusterserviceversion.yaml"

# Patch WATCH_NAMESPACE to use OLM targetNamespaces annotation
yq eval --inplace '
  (.spec.install.spec.deployments[].spec.template.spec.containers[].env[] | select(.name == "WATCH_NAMESPACE")) |=
  {"name": "WATCH_NAMESPACE", "valueFrom": {"fieldRef": {"fieldPath": "metadata.annotations['"'"'olm.targetNamespaces'"'"']"}}}
' "${bundle_directory}/manifests/${file_name}.v${VERSION}.clusterserviceversion.yaml"

# Add OpenShift feature annotations to all distributions.
# 'disconnected' defaults to "false" for community; overridden to "true" for redhat below.
yq eval --inplace '
    .metadata.annotations["features.operators.openshift.io/disconnected"] = "false" |
    .metadata.annotations["features.operators.openshift.io/fips-compliant"] = "false" |
    .metadata.annotations["features.operators.openshift.io/proxy-aware"] = "false" |
    .metadata.annotations["features.operators.openshift.io/tls-profiles"] = "false" |
    .metadata.annotations["features.operators.openshift.io/token-auth-aws"] = "false" |
    .metadata.annotations["features.operators.openshift.io/token-auth-azure"] = "false" |
    .metadata.annotations["features.operators.openshift.io/token-auth-gcp"] = "false" |
    .metadata.annotations["features.operators.openshift.io/cnf"] = "false" |
    .metadata.annotations["features.operators.openshift.io/cni"] = "false" |
    .metadata.annotations["features.operators.openshift.io/csi"] = "false"' \
	"${bundle_directory}/manifests/${file_name}.v${VERSION}.clusterserviceversion.yaml"

if [ "${DISTRIBUTION}" == "community" ]; then
	update_yaml_images "bundles/$DISTRIBUTION/manifests/${file_name}.v${VERSION}.clusterserviceversion.yaml"
elif [ "${DISTRIBUTION}" == "redhat" ]; then
	yq eval --inplace '
        .metadata.annotations["features.operators.openshift.io/disconnected"] = "true" |
        .spec.relatedImages = env(relatedImages) |
        .metadata.annotations.certified = "true" |
        .metadata.annotations["containerImage"] = env(redhatOperatorImage) |
        .spec.install.spec.deployments[].spec.template.spec.containers[].image = env(redhatOperatorImage) |
        .metadata.name = strenv(name_certified)' \
		"${bundle_directory}/manifests/${file_name}.v${VERSION}.clusterserviceversion.yaml"
fi

# Delete comments
sed_inplace '/^[[:space:]]*# [^#]/d' "${bundle_directory}/manifests/${file_name}.v${VERSION}.clusterserviceversion.yaml"

# Lint the bundle YAML files.
yamllint -d '{extends: default, rules: {line-length: disable, indentation: disable}}' bundles/"$DISTRIBUTION"

if >/dev/null command -v tree; then tree -C "${bundle_directory}"; fi
