# syntax=docker/dockerfile:1

# Multi-stage build: static Go binaries, scratch final image.
# Build context must be the repository root:
#   docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(cat VERSION) -t mygrok:$(cat VERSION) .

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.0.0
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags "-s -w -X github.com/veloriba/mygrok/internal/version.Version=${VERSION}" \
    -o /out/mygrok cmd/client/main.go

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/mygrok /usr/local/bin/
ENV PATH=/usr/local/bin
USER 65532:65532

# No ENTRYPOINT: compose services pass the full command
# (e.g. ["mygrok", "http", "3000", "api", "--no-tui"]).
