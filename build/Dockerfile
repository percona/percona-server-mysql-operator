FROM --platform=${BUILDPLATFORM} golang:1.27 AS go_builder
WORKDIR /go/src/github.com/percona/percona-server-mysql-operator

COPY go.mod go.sum ./
RUN go mod download

ARG GIT_COMMIT
ARG GIT_BRANCH
ARG BUILD_TIME
ARG GO_LDFLAGS
ARG GOOS=linux
ARG TARGETARCH
ARG CGO_ENABLED=0

COPY . .

RUN mkdir -p build/_output/bin \
    && GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/percona-server-mysql-operator \
    ./cmd/manager/ \
    && cp -r build/_output/bin/percona-server-mysql-operator /usr/local/bin/percona-server-mysql-operator
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/bootstrap \
    ./cmd/bootstrap/ \
    && cp -r build/_output/bin/bootstrap /usr/local/bin/bootstrap
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/healthcheck \
    ./cmd/healthcheck/ \
    && cp -r build/_output/bin/healthcheck /usr/local/bin/healthcheck
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/sidecar \
    ./cmd/sidecar \
    && cp -r build/_output/bin/sidecar /usr/local/bin/sidecar
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/failover \
    ./cmd/failover/ \
    && cp -r build/_output/bin/failover /usr/local/bin/failover
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/peer-list \
    cmd/peer-list/main.go \
    && cp -r build/_output/bin/peer-list /usr/local/bin/peer-list
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/orc-handler \
    ./cmd/orc-handler/ \
    && cp -r build/_output/bin/orc-handler /usr/local/bin/orc-handler
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s" \
    -o build/_output/bin/mysql-state-monitor \
    cmd/mysql-state-monitor/main.go \
    && cp -r build/_output/bin/mysql-state-monitor /usr/local/bin/mysql-state-monitor
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/pitr \
    ./cmd/pitr/ \
    && cp -r build/_output/bin/pitr /usr/local/bin/pitr
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/clusterset-replica-manager \
    ./cmd/clusterset-replica-manager/ \
	&& cp -r build/_output/bin/clusterset-replica-manager /usr/local/bin/clusterset-replica-manager

# Assemble the operator helper tree here so the runtime image can copy it in one layer.
RUN mkdir -p /opt/percona-server-mysql-operator \
    && cp -a \
        /usr/local/bin/bootstrap \
        /usr/local/bin/healthcheck \
        /usr/local/bin/sidecar \
        /usr/local/bin/failover \
        /usr/local/bin/peer-list \
        /usr/local/bin/orc-handler \
        /usr/local/bin/mysql-state-monitor \
        /usr/local/bin/clusterset-replica-manager \
        /usr/local/bin/pitr \
        build/ps-entrypoint.sh \
        build/ps-pre-stop.sh \
        build/heartbeat-entrypoint.sh \
        build/router-entrypoint.sh \
        build/router_readiness_check.sh \
        build/router_startup_check.sh \
        build/orc-entrypoint.sh \
        build/orchestrator.conf.json \
        build/orc-add_mysql_nodes.sh \
        build/ps-init-entrypoint.sh \
        build/run-backup.sh \
        build/run-restore.sh \
        build/prepare-s3-certs.sh \
        build/haproxy-entrypoint.sh \
        build/haproxy_add_mysql_nodes.sh \
        build/haproxy_check_primary.sh \
        build/haproxy_check_replicas.sh \
        build/haproxy_liveness_check.sh \
        build/haproxy_readiness_check.sh \
        build/haproxy_startup_check.sh \
        build/haproxy.cfg \
        build/haproxy-global.cfg \
        build/pmm-prerun.sh \
        build/binlog-server-entrypoint.sh \
        build/run-pitr-restore.sh \
        build/run-prepare-restore.sh \
        build/lib \
        build/logcollector \
        /opt/percona-server-mysql-operator/

FROM redhat/ubi9-minimal AS ubi9
RUN microdnf -y update && microdnf clean all

LABEL name="Percona Distribution for MySQL Operator" \
    vendor="Percona" \
    summary="Percona Distribution for MySQL Operator v1 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances" \
    description="Percona Distribution for MySQL Operator v1 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances in a Kubernetes-based environment, both on-premises or in the cloud" \
    maintainer="Percona Development <info@percona.com>"

COPY LICENSE /licenses/
COPY --from=go_builder /usr/local/bin/percona-server-mysql-operator /usr/local/bin/percona-server-mysql-operator
COPY --from=go_builder /opt/percona-server-mysql-operator /opt/percona-server-mysql-operator

USER 2
