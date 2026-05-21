dc := "docker compose -f deployment/docker-compose.yml"

# List all recipes
default:
    @just --list

# ── Infra ─────────────────────────────────────────────────────────────────────

# Start backing services only: nats + postgres + redis + identity + herald
infra:
    {{dc}} up -d nats postgres redis identity herald

# Start backing services + cloudflared tunnel
infra-tunnel:
    {{dc}} up -d nats postgres redis identity herald cloudflared

# ── Full stack ────────────────────────────────────────────────────────────────

# Start full trading stack (no strategist, no monitoring)
# Pass --build to force rebuild: just up --build
up *args:
    {{dc}} up -d {{args}}

# Start full stack + monitoring (Grafana :3000, Prometheus, Loki, Tempo, Pyroscope)
up-mon *args:
    {{dc}} --profile monitoring up -d {{args}}
    @echo "Grafana:  http://localhost:3000  (admin / mallow)"

# Stop all containers
down:
    {{dc}} down

# Stop + remove volumes (destructive — wipes all data)
down-v:
    {{dc}} down -v

# ── Operations ────────────────────────────────────────────────────────────────

# Show running containers
ps:
    {{dc}} ps

# Tail logs (pass service name to filter: just logs herald)
logs *args:
    {{dc}} logs -f {{args}}

# Build images (pass service name to build one: just build herald)
build *args:
    {{dc}} build {{args}}

# Build herald image with per-step timing output (cold build ~5-15 min, warm ~30-60 s)
build-herald:
    @echo "=== herald build started at $(date) ==="
    time {{dc}} build --progress=plain herald 2>&1
    @echo "=== herald build finished at $(date) ==="

# Build herald image with no cache (force cold build for accurate baseline timing)
build-herald-nc:
    @echo "=== herald cold build started at $(date) ==="
    time {{dc}} build --no-cache --progress=plain herald 2>&1
    @echo "=== herald cold build finished at $(date) ==="

# Build helm image with per-step timing output
build-helm:
    @echo "=== helm build started at $(date) ==="
    time {{dc}} build --progress=plain helm 2>&1
    @echo "=== helm build finished at $(date) ==="

# Build helm image with no cache
build-helm-nc:
    @echo "=== helm cold build started at $(date) ==="
    time {{dc}} build --no-cache --progress=plain helm 2>&1
    @echo "=== helm cold build finished at $(date) ==="

# Pull latest base images
pull:
    {{dc}} pull

# Restart a service
restart service:
    {{dc}} restart {{service}}

# Open a shell in a running container
sh service:
    {{dc}} exec {{service}} sh

# ── Specs ─────────────────────────────────────────────────────────────────────

# Generate OpenAPI/Swagger specs for all (or specific) services → specs/
# Examples: just gen-specs   |   just gen-specs herald   |   just gen-specs identity helm
gen-specs *services:
    @bash gen-specs.sh {{services}}
