dc := "docker compose -f deployment/docker-compose.yml"

# List all recipes
default:
    @just --list

# ── Infra shortcuts ───────────────────────────────────────────────────────────

# Start infra only (nats + postgres + redis + nats-ui + cloudflared)
infra:
    {{dc}} --profile monitoring up -d nats postgres redis nats-ui cloudflared identity

# Start infra + monitoring stack (Grafana :3000, nats-ui :3001)
infra-mon:
    {{dc}} --profile monitoring up -d nats postgres redis nats-box nats-surveyor nats-ui prometheus loki tempo grafana
    @echo "Grafana:  http://localhost:3000  (admin / mallow)"
    @echo "NATS UI:  http://localhost:3001"

# ── Stack ─────────────────────────────────────────────────────────────────────

# Start full stack
up *args:
    {{dc}} up -d {{args}}

# Start full stack + monitoring
up-mon:
    {{dc}} --profile monitoring up -d

# Stop everything
down:
    {{dc}} down

# Stop + remove volumes (destructive — wipes all data)
down-v:
    {{dc}} down -v

# ── Operations ────────────────────────────────────────────────────────────────

# Show running containers
ps:
    {{dc}} ps

# Tail logs (pass service name: just logs gateway)
logs *args:
    {{dc}} logs -f {{args}}

# Build images (pass service name to build one: just build herald)
build *args:
    {{dc}} build {{args}}

# Pull latest base images
pull:
    {{dc}} pull

# Restart a service
restart service:
    {{dc}} restart {{service}}

# Open a shell in a running container
sh service:
    {{dc}} exec {{service}} sh
