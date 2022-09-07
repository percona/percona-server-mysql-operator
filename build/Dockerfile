FROM golang:1.19.0 AS go_builder
WORKDIR /go/src/github.com/percona/percona-server-mysql-operator

COPY go.mod go.sum ./
RUN go mod download

ARG GIT_COMMIT
ARG BUILD_TIME
ARG BUILD_TIME
ARG GO_LDFLAGS
ARG GOOS=linux
ARG GOARCH=amd64
ARG CGO_ENABLED=0

COPY . .

RUN mkdir -p build/_output/bin \
    && GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
        -o build/_output/bin/percona-server-mysql-operator \
            cmd/manager/main.go \
    && cp -r build/_output/bin/percona-server-mysql-operator /usr/local/bin/percona-server-mysql-operator
RUN GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
        -o build/_output/bin/bootstrap \
            cmd/bootstrap/main.go \
    && cp -r build/_output/bin/bootstrap /usr/local/bin/bootstrap
RUN GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
        -o build/_output/bin/healthcheck \
            cmd/healthcheck/main.go \
    && cp -r build/_output/bin/healthcheck /usr/local/bin/healthcheck
RUN GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED GO_LDFLAGS=$GO_LDFLAGS \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.GitBranch=$GIT_BRANCH -X main.BuildTime=$BUILD_TIME" \
        -o build/_output/bin/sidecar \
            cmd/sidecar/main.go \
    && cp -r build/_output/bin/sidecar /usr/local/bin/sidecar

FROM redhat/ubi8-minimal AS ubi8

# check repository package signature in secure way
RUN export GNUPGHOME="$(mktemp -d)" \
        && microdnf install -y findutils \
        && gpg --keyserver keyserver.ubuntu.com --recv-keys 430BDF5C56E7C94E848EE60C1C4CBDCDCD2EFD2A \
        && gpg --export --armor 430BDF5C56E7C94E848EE60C1C4CBDCDCD2EFD2A > ${GNUPGHOME}/RPM-GPG-KEY-Percona \
        && rpmkeys --import ${GNUPGHOME}/RPM-GPG-KEY-Percona \
        && curl -Lf -o /tmp/percona-release.rpm https://repo.percona.com/yum/percona-release-latest.noarch.rpm \
        && rpmkeys --checksig /tmp/percona-release.rpm \
        && rpm -i /tmp/percona-release.rpm \
        && rm -rf "$GNUPGHOME" /tmp/percona-release.rpm \
        && rpm --import /etc/pki/rpm-gpg/PERCONA-PACKAGING-KEY \
        && percona-release setup pdps-8.0

RUN set -ex; \
    microdnf update; \
    microdnf install -y percona-mysql-shell; \
    microdnf clean all; \
    rm -rf /var/cache; \
    rm -rf /usr/lib/debug/usr/bin;

RUN mkdir /sbin/.mysqlsh && chown 2:2 /sbin/.mysqlsh
RUN mkdir /opt/percona

LABEL name="Percona Distribution for MySQL Operator" \
      vendor="Percona" \
      summary="Percona Distribution for MySQL Operator v1alpha1 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances" \
      description="Percona Distribution for MySQL Operator v1alpha1 contains everything you need to quickly and consistently deploy and scale Percona Server for MySQL instances in a Kubernetes-based environment, both on-premises or in the cloud" \
      maintainer="Percona Development <info@percona.com>"

COPY LICENSE /licenses/
COPY --from=go_builder /usr/local/bin/percona-server-mysql-operator /usr/local/bin/percona-server-mysql-operator
COPY --from=go_builder /usr/local/bin/bootstrap /opt/percona/bootstrap
COPY --from=go_builder /usr/local/bin/healthcheck /opt/percona/healthcheck
COPY --from=go_builder /usr/local/bin/sidecar /opt/percona/sidecar
COPY build/ps-entrypoint.sh /opt/percona/ps-entrypoint.sh
COPY build/heartbeat-entrypoint.sh /opt/percona/heartbeat-entrypoint.sh
COPY build/router-entrypoint.sh /opt/percona/router-entrypoint.sh
COPY build/orc-entrypoint.sh /opt/percona/orc-entrypoint.sh
COPY build/orchestrator.conf.json /opt/percona/orchestrator.conf.json
COPY build/ps-init-entrypoint.sh /opt/percona/ps-init-entrypoint.sh
COPY build/run-backup.sh /opt/percona/run-backup.sh
COPY build/run-restore.sh /opt/percona/run-restore.sh

USER 2
