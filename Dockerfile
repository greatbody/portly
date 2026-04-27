# syntax=docker/dockerfile:1.7

# ---- Build stage ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH

# Pure-Go build (modernc.org/sqlite is CGO-free) → static binary, runs on scratch/distroless.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/portly ./cmd/portly

# ---- Runtime stage ----
# distroless static: ~2MB, includes ca-certificates + tzdata + non-root user.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/portly /app/portly

# Default data dir; mount a volume here to persist the SQLite database.
VOLUME ["/data"]

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/app/portly"]
CMD ["-config", "/app/config.yaml"]
