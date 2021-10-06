# Build the manager binary
FROM golang:1.17 as builder

ARG GIT_COMMIT
ARG BUILD_TIME

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.BuildTime=$BUILD_TIME" -a -o manager cmd/manager/main.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags "-w -s -X main.GitCommit=$GIT_COMMIT -X main.BuildTime=$BUILD_TIME" -a -o bootstrap cmd/bootstrap/main.go

FROM registry.access.redhat.com/ubi7/ubi-minimal AS ubi7
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/bootstrap .
COPY build/ps-entrypoint.sh /ps-entrypoint.sh
COPY build/ps-init-entrypoint.sh /ps-init-entrypoint.sh
USER 65532:65532

ENTRYPOINT ["/manager"]
