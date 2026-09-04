FROM golang:1.22-bullseye AS build
WORKDIR /go/src/distortioner
COPY app .
RUN go test ./...
RUN go build

FROM ghcr.io/graynk/ffmpegim AS release

# Absolute path: base image WORKDIR is /tmp/workdir; relative "app" became
# /tmp/workdir/app and broke the documented -v …:/app/data mount.
WORKDIR /app

# git + docker CLI so admin /update can fetch and rebuild via mounted docker.sock
USER root
RUN apt-get update \
  && apt-get install -y --no-install-recommends git docker.io ca-certificates curl \
  && rm -rf /var/lib/apt/lists/*

COPY --from=build /go/src/distortioner/distortioner distortioner

ENTRYPOINT ["/app/distortioner"]
