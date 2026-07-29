1. To generate bundle correctly please set env variables (default values for these variables you can check in makefile):
```bash
# operator version
export VERSION=1.0.0
# By default we use perconalab for tag owner. Please update this variable to use another repo
export IMAGE_TAG_OWNER=percona
# Min k8s version
export MIN_KUBE_VERSION=1.27.0
# Openshift versions:
export OPENSHIFT_VERSIONS="v4.16-v4.20"
```
2. Also it could be useful to check variable in makefile and update if you need something extra. For the most cases to update these variables is enough
3. Update spec.description in bundle.csv.yaml with features added in this release.
4. Run bundle generation:
```bash
# Generate all bundles (community and redhat):
make bundles
# Generate only specific bundle:
make bundles/community
```

The `redhat` bundle uses `distributions/redhat.sh` to resolve real image digests from the
Red Hat Catalog API (based on the versions in `e2e-tests/release_versions`) and populate
`spec.relatedImages` and the operator `containerImage` with `registry.connect.redhat.com/...@sha256:...`
references. If a digest can't be resolved (e.g. no network access), it falls back to a
`<DIGEST>` placeholder. To pin a digest manually, set `REDHAT_IMAGE_DIGEST_<NAME>`
(e.g. `REDHAT_IMAGE_DIGEST_OPERATOR=sha256:...`).
