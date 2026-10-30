FROM golang:1.22-bullseye AS build
WORKDIR /go/src/distortioner
# Dependencies first, so /update only re-downloads modules and recompiles the cgo sqlite driver
# when go.mod/go.sum change, not on every code change.
COPY app/go.mod app/go.sum ./
RUN go mod download && go build github.com/mattn/go-sqlite3
COPY app .
RUN go test ./...
RUN go build -trimpath -ldflags="-s -w"

# Only the docker CLI is needed for /update (it talks to the host daemon via the mounted socket);
# the docker.io package would also install a daemon, containerd and runc.
FROM docker:27.3.1-cli AS dockercli

FROM ghcr.io/graynk/ffmpegim AS release

# Absolute path: base image WORKDIR is /tmp/workdir; relative "app" became
# /tmp/workdir/app and broke the documented -v …:/app/data mount.
WORKDIR /app

# git + docker CLI so admin /update can fetch and rebuild via mounted docker.sock
USER root
RUN apt-get update \
  && apt-get install -y --no-install-recommends git ca-certificates curl \
  && rm -rf /var/lib/apt/lists/*
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker

COPY --from=build /go/src/distortioner/distortioner distortioner

ENTRYPOINT ["/app/distortioner"]
