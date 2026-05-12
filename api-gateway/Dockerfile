# Build context: repository root (go.work workspace)
# docker build -f api-gateway/Dockerfile -t gateway .
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy workspace manifests for dependency caching
COPY go.work go.work.sum ./
COPY pkg/go.mod pkg/go.sum ./pkg/
COPY api-gateway/go.mod api-gateway/go.sum ./api-gateway/

# Slim go.work to only the two modules needed here, then download deps.
# Layer is cached unless go.mod / go.sum change.
RUN printf 'go 1.26.0\n\nuse (\n\t./pkg\n\t./api-gateway\n)\n' > go.work && \
    rm -f go.work.sum && \
    go mod download

# Copy source
COPY pkg/        ./pkg/
COPY api-gateway/ ./api-gateway/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" \
    -o gateway ./api-gateway/cmd/gateway/

# ── Runtime ───────────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/gateway .

EXPOSE 8080
ENV PORT=8080
CMD ["./gateway"]
