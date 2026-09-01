#!/usr/bin/env bash

# shellcheck disable=SC2016,SC2155,SC2154  # SC2154: repo_root/VERSION provided by generate.sh
set -euo pipefail

redhat_release="${VERSION}"
redhat_registry="registry.connect.redhat.com"
redhat_catalog_api="${REDHAT_CATALOG_API:-https://catalog.redhat.com/api/containers/v1}"
redhat_catalog_curl_timeout="${REDHAT_CATALOG_CURL_TIMEOUT:-20}"
redhat_operator_repository="percona/percona-server-mysql-operator"
redhat_containers_repository="percona/percona-server-mysql-operator-containers"
redhat_operator_tag="${REDHAT_OPERATOR_TAG:-${redhat_release}}"
redhat_related_images="[]"

required_vars=(
	VERSION
	repo_root
)

for var in "${required_vars[@]}"; do
	: "${!var:?Environment variable ${var} is required}"
done

digest_key() {
	printf '%s' "$1" \
		| sed -E 's/[^[:alnum:]]+/_/g' \
		| tr '[:lower:]' '[:upper:]'
}

catalog_digest() {
	local repository="$1"
	local tag="$2"
	local digest

	log "Resolving Red Hat digest for ${redhat_registry}/${repository}:${tag}"

	digest="$(
		curl -fsSL \
			--connect-timeout 5 \
			--max-time "${redhat_catalog_curl_timeout}" \
			"${redhat_catalog_api}/repositories/registry/${redhat_registry}/repository/${repository}/tag/${tag}" \
			| jq -er '.docker_image_digest // .data.docker_image_digest // .data[0].docker_image_digest' 2>/dev/null
	)" || digest="$(
		curl -fsSL \
			--connect-timeout 5 \
			--max-time "${redhat_catalog_curl_timeout}" \
			"${redhat_catalog_api}/repositories/registry/${redhat_registry}/repository/${repository}/images?page_size=500" \
			| jq -er \
				--arg tag "${tag}" \
				'first(.data[] | select(any(.repositories[]?.tags[]?; .name == $tag)) | .docker_image_digest)' \
				2>/dev/null
	)" || digest="<DIGEST>"

	if [[ -z ${digest} || ${digest} == "null" ]]; then
		digest="<DIGEST>"
	fi

	if [[ ${digest} == "<DIGEST>" ]]; then
		log "Unable to resolve digest for ${redhat_registry}/${repository}:${tag}; using <DIGEST> placeholder"
	elif [[ ${digest} != sha256:* ]]; then
		digest="sha256:${digest#sha256:}"
	fi

	printf '%s\n' "${digest}"
}

image_ref() {
	local name="$1"
	local repository="$2"
	local tag="$3"
	local digest_var="REDHAT_IMAGE_DIGEST_$(digest_key "${name}")"
	local digest="${!digest_var:-}"

	if [[ -z ${digest} ]]; then
		digest="$(catalog_digest "${repository}" "${tag}")"
	fi

	if [[ -z ${digest} ]]; then
		abort "empty Red Hat image digest for ${redhat_registry}/${repository}:${tag}"
	fi

	if [[ ${digest} != "<DIGEST>" && ${digest} != sha256:* ]]; then
		digest="sha256:${digest#sha256:}"
	fi

	printf '%s/%s@%s\n' "${redhat_registry}" "${repository}" "${digest}"
}

release_component_tag() {
	printf '%s-%s-%s\n' "${redhat_release}" "$1" "$2"
}

add_related_image() {
	local name="$1"
	local repository="$2"
	local tag="$3"
	local image

	image="$(image_ref "${name}" "${repository}" "${tag}")"

	log "Related image ${name}: ${image}"

	redhat_related_images="$(
		jq -c \
			--arg name "${name}" \
			--arg image "${image}" \
			'. + [{ name: $name, image: $image }]' \
			<<<"${redhat_related_images}"
	)"
}

related_image_by_name() {
	local name="$1"

	jq --raw-output \
		--arg name "${name}" \
		'map(select(.name == $name)) | last.image // ""' \
		<<<"${redhat_related_images}"
}

image_version() {
	printf '%s\n' "${1##*:}"
}

mysql_major_version() {
	local image
	image="$(yq eval '.spec.mysql.image' "${repo_root}/deploy/cr.yaml")"
	grep -oE '[0-9]+\.[0-9]+' <<<"$(image_version "${image}")" | tail -n1
}

build_redhat_related_images() {
	local mysql_major

	log "Building Red Hat related images from release versions"

	# shellcheck source=/dev/null
	source "${repo_root}/e2e-tests/release_versions"

	add_related_image "mysql8.4" "${redhat_containers_repository}" \
		"$(release_component_tag ps "$(image_version "${IMAGE_MYSQL84}")")"
	add_related_image "mysql8.0" "${redhat_containers_repository}" \
		"$(release_component_tag ps "$(image_version "${IMAGE_MYSQL80}")")"

	add_related_image "router8.4" "${redhat_containers_repository}" \
		"$(release_component_tag router "$(image_version "${IMAGE_ROUTER84}")")"
	add_related_image "router8.0" "${redhat_containers_repository}" \
		"$(release_component_tag router "$(image_version "${IMAGE_ROUTER80}")")"

	add_related_image "backup8.4" "${redhat_containers_repository}" \
		"$(release_component_tag backup "$(image_version "${IMAGE_BACKUP84}")")"
	add_related_image "backup8.0" "${redhat_containers_repository}" \
		"$(release_component_tag backup "$(image_version "${IMAGE_BACKUP80}")")"

	add_related_image "toolkit" "${redhat_containers_repository}" "${redhat_release}-toolkit"
	add_related_image "pmm3" "${redhat_containers_repository}" "${redhat_release}-pmm3"
	add_related_image "haproxy" "${redhat_containers_repository}" "${redhat_release}-haproxy"
	add_related_image "orchestrator" "${redhat_containers_repository}" "${redhat_release}-orchestrator"
	add_related_image "binlog-server" "${redhat_containers_repository}" "${redhat_release}-binlog-server"
	add_related_image "operator" "${redhat_operator_repository}" "${redhat_operator_tag}"

	mysql_major="$(mysql_major_version)"

	jq -nc \
		--arg operator_image "$(related_image_by_name operator)" \
		--arg mysql_image "$(related_image_by_name "mysql${mysql_major}")" \
		--arg router_image "$(related_image_by_name "router${mysql_major}")" \
		--arg backup_image "$(related_image_by_name "backup${mysql_major}")" \
		--arg haproxy_image "$(related_image_by_name haproxy)" \
		--arg orchestrator_image "$(related_image_by_name orchestrator)" \
		--arg pmm_image "$(related_image_by_name pmm3)" \
		--arg toolkit_image "$(related_image_by_name toolkit)" \
		--argjson related_images "${redhat_related_images}" \
		'{
			operatorImage: $operator_image,
			mysqlImage: $mysql_image,
			routerImage: $router_image,
			backupImage: $backup_image,
			haproxyImage: $haproxy_image,
			orchestratorImage: $orchestrator_image,
			pmmImage: $pmm_image,
			toolkitImage: $toolkit_image,
			relatedImages: $related_images
		}'
}

rewrite_cr_example_images() {
	local examples_json="$1"
	local images_json="$2"

	jq \
		--argjson images "${images_json}" \
		'map(
			if .kind == "PerconaServerMySQL" then
				.spec.mysql.image = $images.mysqlImage
				| .spec.proxy.haproxy.image = $images.haproxyImage
				| .spec.proxy.router.image = $images.routerImage
				| .spec.orchestrator.image = $images.orchestratorImage
				| .spec.pmm.image = $images.pmmImage
				| .spec.backup.image = $images.backupImage
				| .spec.toolkit.image = $images.toolkitImage
			else
				.
			end
		)' \
		<<<"${examples_json}"
}
