# Build context: repository root (go.work workspace)
# docker build -f investment/Dockerfile -t investment .
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy workspace manifests for dependency caching
COPY go.work go.work.sum ./
COPY pkg/go.mod pkg/go.sum ./pkg/
COPY investment/go.mod investment/go.sum ./investment/

# Rewrite go.work to only include modules present in this build stage,
# then download all external deps (layer is cached unless go.mod/go.sum change)
RUN printf 'go 1.26.0\n\nuse (\n\t./pkg\n\t./investment\n)\n' > go.work && \
    rm -f go.work.sum && \
    go mod download

# Copy source
COPY pkg/ ./pkg/
COPY investment/ ./investment/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" \
    -o investment ./investment/cmd/investment/

# ── Final stage ─────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/investment .

EXPOSE 8083
ENV PORT=8083
CMD ["./investment"]
