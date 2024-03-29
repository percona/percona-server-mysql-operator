# VERSION defines the project version for the bundle.
# Update this value when you upgrade the version of your project.
# To re-generate a bundle for another specific version without changing the standard setup, you can:
# - use the VERSION as arg of the bundle target (e.g make bundle VERSION=0.0.2)
# - use environment variables to overwrite this value (e.g export VERSION=0.0.2)
NAME ?= percona-server-mysql-operator
VERSION ?= $(shell git rev-parse --abbrev-ref HEAD | sed -e 's^/^-^g; s^[.]^-^g;' | tr '[:upper:]' '[:lower:]')
ROOT_REPO ?= ${PWD}

# CHANNELS define the bundle channels used in the bundle.
# Add a new line here if you would like to change its default config. (E.g CHANNELS = "candidate,fast,stable")
# To re-generate a bundle for other specific channels without changing the standard setup, you can:
# - use the CHANNELS as arg of the bundle target (e.g make bundle CHANNELS=candidate,fast,stable)
# - use environment variables to overwrite this value (e.g export CHANNELS="candidate,fast,stable")
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif

# DEFAULT_CHANNEL defines the default channel used in the bundle.
# Add a new line here if you would like to change its default config. (E.g DEFAULT_CHANNEL = "stable")
# To re-generate a bundle for any other default channel without changing the default setup, you can:
# - use the DEFAULT_CHANNEL as arg of the bundle target (e.g make bundle DEFAULT_CHANNEL=stable)
# - use environment variables to overwrite this value (e.g export DEFAULT_CHANNEL="stable")
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# IMAGE_TAG_BASE defines the docker.io namespace and part of the image name for remote images.
# This variable is used to construct full image tags for bundle and catalog images.
#
# For example, running 'make bundle-build bundle-push catalog-build catalog-push' will build and push both
# perconalab/percona-server-mysql-operator-bundle:v$VERSION and perconalab/percona-server-mysql-operator-catalog:v$VERSION.
IMAGE_TAG_BASE ?= perconalab/percona-server-mysql-operator

# BUNDLE_IMG defines the image:tag used for the bundle.
# You can use it as an arg. (E.g make bundle-build BUNDLE_IMG=<some-registry>/<project-name-bundle>:<tag>)
BUNDLE_IMG ?= $(IMAGE_TAG_BASE)-bundle:v$(VERSION)

# Image URL to use all building/pushing image targets
IMAGE ?= $(IMAGE_TAG_BASE):$(VERSION)

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.21

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

DEPLOYDIR = ./deploy

all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

generate: controller-gen
	$(CONTROLLER_GEN) crd:maxDescLen=0 rbac:roleName=$(NAME) webhook paths="./..." output:crd:artifacts:config=config/crd/bases  ## Generate WebhookConfiguration, Role and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) object:headerFile="LICENSE-HEADER" paths="./..." ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out

kuttl-shfmt:
	shfmt -bn -ci -s -w e2e-tests/functions
	find e2e-tests/tests/ -type f -not -name '*-assert.yaml' -name '*.yaml' | xargs ./e2e-tests/format

e2e-test: kuttl-shfmt
	ROOT_REPO=$(ROOT_REPO) kubectl kuttl test --config e2e-tests/kuttl.yaml

manifests: kustomize generate
	$(KUSTOMIZE) build config/crd/ > $(DEPLOYDIR)/crd.yaml
	echo "---" >> $(DEPLOYDIR)/crd.yaml
	$(KUSTOMIZE) build config/rbac/ | sed 's/ClusterRole/Role/g' > $(DEPLOYDIR)/rbac.yaml
	echo "---" >> $(DEPLOYDIR)/rbac.yaml
	cd config/manager && $(KUSTOMIZE) edit set image perconalab/percona-server-mysql-operator=$(IMAGE)
	$(KUSTOMIZE) build config/manager/ > $(DEPLOYDIR)/operator.yaml
	echo "---" >> $(DEPLOYDIR)/operator.yaml
	cat $(DEPLOYDIR)/crd.yaml $(DEPLOYDIR)/rbac.yaml $(DEPLOYDIR)/operator.yaml > $(DEPLOYDIR)/bundle.yaml

gen-versionservice-client: swagger
	rm pkg/version/service/version.swagger.yaml
	curl https://raw.githubusercontent.com/Percona-Lab/percona-version-service/main/api/version.swagger.yaml --output pkg/version/service/version.swagger.yaml
	rm -rf pkg/version/service/client
	swagger generate client -f pkg/version/service/version.swagger.yaml -c pkg/version/service/client -m pkg/version/service/client/models

##@ Build

.PHONY: build
build: generate ## Build docker image with the manager.
	ROOT_REPO=$(ROOT_REPO) VERSION=$(VERSION) IMAGE=$(IMAGE) $(ROOT_REPO)/e2e-tests/build

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/manager/main.go

##@ Deployment

install: manifests ## Install CRDs, rbac
	kubectl apply --server-side -f $(DEPLOYDIR)/crd.yaml
	kubectl apply -f $(DEPLOYDIR)/rbac.yaml

uninstall: manifests ## Uninstall CRDs, rbac
	kubectl delete -f $(DEPLOYDIR)/crd.yaml
	kubectl delete -f $(DEPLOYDIR)/rbac.yaml

deploy: manifests ## Deploy operator
	yq eval \
		'(select(documentIndex==1).spec.template.spec.containers[] | select(.name=="manager").env[] | select(.name=="LOG_LEVEL").value) = "DEBUG"' \
		$(DEPLOYDIR)/operator.yaml \
		| kubectl apply -f -

undeploy: manifests ## Undeploy operator
	kubectl delete -f $(DEPLOYDIR)/operator.yaml


CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0)

KUSTOMIZE = $(shell pwd)/bin/kustomize
kustomize: ## Download kustomize locally if necessary.
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v4@v4.5.3)

ENVTEST = $(shell pwd)/bin/setup-envtest
envtest: ## Download envtest-setup locally if necessary.
	$(call go-get-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest@latest)

SWAGGER = $(shell pwd)/bin/swagger
swagger: ## Download swagger locally if necessary.
	$(call go-get-tool,$(SWAGGER),github.com/go-swagger/go-swagger/cmd/swagger@latest)

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef

.PHONY: bundle
bundle: manifests kustomize ## Generate bundle manifests and metadata, then validate generated files.
	operator-sdk generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMAGE)
	$(KUSTOMIZE) build config/manifests | operator-sdk generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	operator-sdk bundle validate ./bundle

.PHONY: bundle-build
bundle-build: ## Build the bundle image.
	docker build -f bundle.Dockerfile -t $(BUNDLE_IMG) .

.PHONY: bundle-push
bundle-push: ## Push the bundle image.
	$(MAKE) docker-push IMAGE=$(BUNDLE_IMG)

.PHONY: opm
OPM = ./bin/opm
opm: ## Download opm locally if necessary.
ifeq (,$(wildcard $(OPM)))
ifeq (,$(shell which opm 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPM)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPM) https://github.com/operator-framework/operator-registry/releases/download/v1.15.1/$${OS}-$${ARCH}-opm ;\
	chmod +x $(OPM) ;\
	}
else
OPM = $(shell which opm)
endif
endif

# A comma-separated list of bundle images (e.g. make catalog-build BUNDLE_IMGS=example.com/operator-bundle:v0.1.0,example.com/operator-bundle:v0.2.0).
# These images MUST exist in a registry and be pull-able.
BUNDLE_IMGS ?= $(BUNDLE_IMG)

# The image tag given to the resulting catalog image (e.g. make catalog-build CATALOG_IMG=example.com/operator-catalog:v0.2.0).
CATALOG_IMG ?= $(IMAGE_TAG_BASE)-catalog:v$(VERSION)

# Set CATALOG_BASE_IMG to an existing catalog image tag to add $BUNDLE_IMGS to that image.
ifneq ($(origin CATALOG_BASE_IMG), undefined)
FROM_INDEX_OPT := --from-index $(CATALOG_BASE_IMG)
endif

# Build a catalog image by adding bundle images to an empty catalog using the operator package manager tool, 'opm'.
# This recipe invokes 'opm' in 'semver' bundle add mode. For more information on add modes, see:
# https://github.com/operator-framework/community-operators/blob/7f1438c/docs/packaging-operator.md#updating-your-existing-operator
.PHONY: catalog-build
catalog-build: opm ## Build a catalog image.
	$(OPM) index add --container-tool docker --mode semver --tag $(CATALOG_IMG) --bundles $(BUNDLE_IMGS) $(FROM_INDEX_OPT)

# Push the catalog image.
.PHONY: catalog-push
catalog-push: ## Push a catalog image.
	$(MAKE) docker-push IMG=$(CATALOG_IMG)

# Prepare release
CERT_MANAGER_VER := $(shell grep -Eo "cert-manager v.*" go.mod|grep -Eo "[0-9]+\.[0-9]+\.[0-9]+")
release: manifests
	sed -i "/CERT_MANAGER_VER/s/CERT_MANAGER_VER=\".*/CERT_MANAGER_VER=\"$(CERT_MANAGER_VER)\"/" e2e-tests/vars.sh
	sed -i -e "/^  mysql:/,/^    image:/{s/image: .*/image: percona\/percona-server:@@SET_TAG@@/}" \
		-e "/^    haproxy:/,/^      image:/{s/image: .*/image: percona\/haproxy:@@SET_TAG@@/}" \
		-e "/^    router:/,/^      image:/{s/image: .*/image: percona\/percona-mysql-router:@@SET_TAG@@/}" \
		-e "/^  orchestrator:/,/^    image:/{s/image: .*/image: percona\/percona-orchestrator:@@SET_TAG@@/}" \
		-e "/^  backup:/,/^    image:/{s/image: .*/image: percona\/percona-xtrabackup:@@SET_TAG@@/}" \
		-e "/^  toolkit:/,/^    image:/{s/image: .*/image: percona\/percona-toolkit:@@SET_TAG@@/}" \
		-e "/^  pmm:/,/^    image:/{s/image: .*/image: percona\/pmm-client:@@SET_TAG@@/}" deploy/cr.yaml

# Prepare main branch after release
MAJOR_VER := $(shell grep "Version =" pkg/version/version.go|grep -Eo "[0-9]+\.[0-9]+\.[0-9]+"|cut -d'.' -f1)
MINOR_VER := $(shell grep "Version =" pkg/version/version.go|grep -Eo "[0-9]+\.[0-9]+\.[0-9]+"|cut -d'.' -f2)
PATCH_VER := $(shell grep "Version =" pkg/version/version.go|grep -Eo "[0-9]+\.[0-9]+\.[0-9]+"|cut -d'.' -f3)
NEXT_VER ?= $(MAJOR_VER).$$(($(MINOR_VER) + 1)).$(PATCH_VER)
after-release: manifests
	sed -i "/const Version = \"/s/Version = \".*/Version = \"$(NEXT_VER)\"/" pkg/version/version.go
	sed -i -e "/^spec:/,/^  crVersion:/{s/crVersion: .*/crVersion: $(NEXT_VER)/}" \
		-e "/^  mysql:/,/^    image:/{s/image: .*/image: perconalab\/percona-server-mysql-operator:main-psmysql/}" \
		-e "/^    haproxy:/,/^      image:/{s/image: .*/image: perconalab\/percona-server-mysql-operator:main-haproxy/}" \
		-e "/^    router:/,/^      image:/{s/image: .*/image: perconalab\/percona-server-mysql-operator:main-router/}" \
		-e "/^  orchestrator:/,/^    image:/{s/image: .*/image: perconalab\/percona-server-mysql-operator:main-orchestrator/}" \
		-e "/^  backup:/,/^    image:/{s/image: .*/image: perconalab\/percona-server-mysql-operator:main-backup/}" \
		-e "/^  toolkit:/,/^    image:/{s/image: .*/image: perconalab\/percona-server-mysql-operator:main-toolkit/}" \
		-e "s/initImage: .*/initImage: perconalab\/percona-server-mysql-operator:$(NEXT_VER)/g" \
		-e "/^  pmm:/,/^    image:/{s/image: .*/image: perconalab\/pmm-client:dev-latest/}" deploy/cr.yaml
