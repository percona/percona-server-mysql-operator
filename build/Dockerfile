FROM --platform=${BUILDPLATFORM} golang:1.24 AS go_builder
WORKDIR /go/src/github.com/percona/percona-server-mysql-operator

COPY go.mod go.sum ./
RUN go mod download

ARG GIT_COMMIT
ARG BUILD_TIME
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
    cmd/manager/main.go \
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
    -o build/_output/bin/peer-list \
    cmd/peer-list/main.go \
    && cp -r build/_output/bin/peer-list /usr/local/bin/peer-list
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
    -o build/_output/bin/orc-handler \
    cmd/orc-handler/main.go \
    && cp -r build/_output/bin/orc-handler /usr/local/bin/orc-handler
RUN GOOS=$GOOS GOARCH=$TARGETARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s" \
    -o build/_output/bin/mysql-state-monitor \
    cmd/mysql-state-monitor/main.go \
    && cp -r build/_output/bin/mysql-state-monitor /usr/local/bin/mysql-state-monitor

FROM redhat/ubi9-minimal AS ubi9
RUN microdnf -y update && microdnf clean all

RUN mkdir /opt/percona-server-mysql-operator

LABEL name="Percona Distribution for MySQL Operator" \
    vendor="Percona" \
    summary="Percona Distribution for MySQL Operator v1alpha1 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances" \
    description="Percona Distribution for MySQL Operator v1alpha1 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances in a Kubernetes-based environment, both on-premises or in the cloud" \
    maintainer="Percona Development <info@percona.com>"

COPY LICENSE /licenses/
COPY --from=go_builder /usr/local/bin/percona-server-mysql-operator /usr/local/bin/percona-server-mysql-operator
COPY --from=go_builder /usr/local/bin/bootstrap /opt/percona-server-mysql-operator/bootstrap
COPY --from=go_builder /usr/local/bin/healthcheck /opt/percona-server-mysql-operator/healthcheck
COPY --from=go_builder /usr/local/bin/sidecar /opt/percona-server-mysql-operator/sidecar
COPY --from=go_builder /usr/local/bin/peer-list /opt/percona-server-mysql-operator/peer-list
COPY --from=go_builder /usr/local/bin/orc-handler /opt/percona-server-mysql-operator/orc-handler
COPY --from=go_builder /usr/local/bin/mysql-state-monitor /opt/percona-server-mysql-operator/mysql-state-monitor
COPY build/ps-entrypoint.sh /opt/percona-server-mysql-operator/ps-entrypoint.sh
COPY build/ps-pre-stop.sh /opt/percona-server-mysql-operator/ps-pre-stop.sh
COPY build/heartbeat-entrypoint.sh /opt/percona-server-mysql-operator/heartbeat-entrypoint.sh
COPY build/router-entrypoint.sh /opt/percona-server-mysql-operator/router-entrypoint.sh
COPY build/router_readiness_check.sh /opt/percona-server-mysql-operator/router_readiness_check.sh
COPY build/router_startup_check.sh /opt/percona-server-mysql-operator/router_startup_check.sh
COPY build/orc-entrypoint.sh /opt/percona-server-mysql-operator/orc-entrypoint.sh
COPY build/orchestrator.conf.json /opt/percona-server-mysql-operator/orchestrator.conf.json
COPY build/ps-init-entrypoint.sh /opt/percona-server-mysql-operator/ps-init-entrypoint.sh
COPY build/run-backup.sh /opt/percona-server-mysql-operator/run-backup.sh
COPY build/run-restore.sh /opt/percona-server-mysql-operator/run-restore.sh
COPY build/haproxy-entrypoint.sh /opt/percona-server-mysql-operator/haproxy-entrypoint.sh
COPY build/haproxy_add_mysql_nodes.sh /opt/percona-server-mysql-operator/haproxy_add_mysql_nodes.sh
COPY build/haproxy_check_primary.sh /opt/percona-server-mysql-operator/haproxy_check_primary.sh
COPY build/haproxy_check_replicas.sh /opt/percona-server-mysql-operator/haproxy_check_replicas.sh
COPY build/haproxy_liveness_check.sh /opt/percona-server-mysql-operator/haproxy_liveness_check.sh
COPY build/haproxy_readiness_check.sh /opt/percona-server-mysql-operator/haproxy_readiness_check.sh
COPY build/haproxy.cfg /opt/percona-server-mysql-operator/haproxy.cfg
COPY build/haproxy-global.cfg /opt/percona-server-mysql-operator/haproxy-global.cfg
COPY build/pmm-prerun.sh /opt/percona-server-mysql-operator/pmm-prerun.sh
COPY build/binlog-server-entrypoint.sh /opt/percona-server-mysql-operator/binlog-server-entrypoint.sh

USER 2
