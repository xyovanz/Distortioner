FROM golang:1.22-bullseye AS build
WORKDIR /go/src/distortioner
COPY app .
RUN go test ./...
RUN go build

FROM ghcr.io/graynk/ffmpegim AS release

# Absolute path: base image WORKDIR is /tmp/workdir; relative "app" became
# /tmp/workdir/app and broke the documented -v …:/app/data mount.
WORKDIR /app
COPY --from=build /go/src/distortioner/distortioner distortioner

ENTRYPOINT ["/app/distortioner"]
