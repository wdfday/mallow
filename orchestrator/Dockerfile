# Build context: repository root (go.work workspace)
# docker build -f orchestrator/Dockerfile -t orchestrator .
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy workspace manifests for dependency caching
COPY go.work go.work.sum ./
COPY pkg/go.mod pkg/go.sum ./pkg/
COPY orchestrator/go.mod orchestrator/go.sum ./orchestrator/

# Rewrite go.work to only include modules present in this build stage,
# then download all external deps (layer is cached unless go.mod/go.sum change)
RUN printf 'go 1.26.0\n\nuse (\n\t./pkg\n\t./orchestrator\n)\n' > go.work && \
    rm -f go.work.sum && \
    go mod download

# Copy source
COPY pkg/ ./pkg/
COPY orchestrator/ ./orchestrator/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" \
    -o orchestrator ./orchestrator/cmd/orchestrator/

# ── Final stage ──────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/orchestrator .

ENV NATS_URL=nats://nats:4222
EXPOSE 9090
CMD ["./orchestrator"]
