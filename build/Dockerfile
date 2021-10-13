FROM golang:1.17 AS go_builder
WORKDIR /go/src/github.com/percona/percona-server-mysql-operator

COPY . .

ARG GIT_COMMIT
ARG BUILD_TIME
ARG BUILD_TIME
ARG GO_LDFLAGS
ARG GOOS=linux
ARG GOARCH=amd64
ARG CGO_ENABLED=0

RUN mkdir -p build/_output/bin \
    && GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -mod=vendor -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
        -o build/_output/bin/percona-server-mysql-operator \
            cmd/manager/main.go \
    && cp -r build/_output/bin/percona-server-mysql-operator /usr/local/bin/percona-server-mysql-operator
RUN GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -mod=vendor -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
        -o build/_output/bin/bootstrap \
            cmd/bootstrap/main.go \
    && cp -r build/_output/bin/bootstrap /usr/local/bin/bootstrap

FROM registry.access.redhat.com/ubi7/ubi-minimal AS ubi7
RUN microdnf update && microdnf clean all

LABEL name="Percona Distribution for MySQL Operator" \
      vendor="Percona" \
      summary="Percona Distribution for MySQL Operator v2 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances" \
      description="Percona Distribution for MySQL Operator v2 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances in a Kubernetes-based environment, both on-premises or in the cloud" \
      maintainer="Percona Development <info@percona.com>"

COPY LICENSE /licenses/
COPY --from=go_builder /usr/local/bin/percona-server-mysql-operator /usr/local/bin/percona-server-mysql-operator
COPY --from=go_builder /usr/local/bin/bootstrap /bootstrap
COPY build/ps-entrypoint.sh /ps-entrypoint.sh
COPY build/ps-init-entrypoint.sh /ps-init-entrypoint.sh

USER 2
