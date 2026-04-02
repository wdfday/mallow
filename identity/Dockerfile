# Build context: repository root (go.work workspace)
# docker build -f identity/Dockerfile -t identity .
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy workspace manifests for dependency caching
COPY go.work go.work.sum ./
COPY pkg/go.mod pkg/go.sum ./pkg/
COPY identity/go.mod identity/go.sum ./identity/

# Rewrite go.work to only include modules present in this build stage,
# then download all external deps (layer is cached unless go.mod/go.sum change)
RUN printf 'go 1.26.0\n\nuse (\n\t./pkg\n\t./identity\n)\n' > go.work && \
    rm -f go.work.sum && \
    go mod download

# Copy source
COPY pkg/ ./pkg/
COPY identity/ ./identity/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" \
    -o identity ./identity/cmd/identity/

# ── Final stage ─────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/identity .

EXPOSE 8082
ENV PORT=8082
CMD ["./identity"]
