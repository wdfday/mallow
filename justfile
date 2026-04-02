dc := "docker compose -f deployment/docker-compose.yml"

# Infra only (nats + postgres + redis + nats-ui)
infra:
    {{dc}} up -d nats postgres redis nats-ui

# Full stack
up *args:
    {{dc}} up -d {{args}}

# Infra + monitoring stack only (Grafana at :3000)
up-mon:
    {{dc}} --profile monitoring up -d nats postgres redis nats-box nats-surveyor prometheus grafana
    @echo "Grafana: http://localhost:3000  (admin / mallow)"

# Stop everything
down:
    {{dc}} down

# Stop + remove volumes
down-v:
    {{dc}} down -v

# Logs (pass service name: just logs gateway)
logs *args:
    {{dc}} logs -f {{args}}

# Build images
build *args:
    {{dc}} build {{args}}

# Pull latest images
pull:
    {{dc}} pull

# Show status
ps:
    {{dc}} ps

# Restart a service
restart service:
    {{dc}} restart {{service}}

# Open a shell in a container
sh service:
    {{dc}} exec {{service}} sh
