# syntax=docker/dockerfile:1.7
# BuildKit syntax directive enables --mount=type=cache below. Falls back
# gracefully on docker 23+ (which uses BuildKit by default); older docker
# users should `export DOCKER_BUILDKIT=1`.
FROM golang:1.25.11-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY pkg/ pkg/
COPY room-service/ room-service/
# Cache mounts share the Go module + build cache across every service's
# Dockerfile so 11 separate `make build-test-images` runs reuse a single
# pkg/mod tree (~500 MB) instead of redownloading per service.
# -ldflags='-s -w' strips the symbol table + DWARF (~25% smaller binary).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags='-s -w' -o /room-service ./room-service/

# Distroless static is ~2 MB (vs alpine's ~7 MB), ships ca-certs + tzdata
# + a nonroot user (UID 65532), and contains no shell — every CGO_ENABLED=0
# Go binary works here.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /room-service /room-service
USER nonroot:nonroot
ENTRYPOINT ["/room-service"]
