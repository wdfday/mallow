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

# ── Herald dev (local, hot-fix) ───────────────────────────────────────────────

herald-dev:
    cd almanac && \
      NATS_URL="nats://signal_engine:signal-engine-dev@localhost:4222" \
      HERALD_DATA_DIR="{{justfile_directory()}}/data" \
      HERALD_HTTP_ADDR="0.0.0.0:8090" \
      HERALD_WARM_BARS=500 \
      HERALD_SYMBOLS_FILE="{{justfile_directory()}}/deployment/symbols.yaml" \
      HERALD_DATABASE_URL="postgres://mallow:mallow-dev@localhost:5432/herald?sslmode=disable" \
      RUST_LOG="herald=info,alm_engine=info,alm_strategy=info,tower_http=warn" \
      cargo run --bin alm-herald

herald-debug:
    cd almanac && \
      NATS_URL="nats://signal_engine:signal-engine-dev@localhost:4222" \
      HERALD_DATA_DIR="{{justfile_directory()}}/data" \
      HERALD_HTTP_ADDR="0.0.0.0:8090" \
      HERALD_WARM_BARS=500 \
      HERALD_SYMBOLS_FILE="{{justfile_directory()}}/deployment/symbols.yaml" \
      HERALD_DATABASE_URL="postgres://mallow:mallow-dev@localhost:5432/herald?sslmode=disable" \
      RUST_LOG="herald=debug,alm_engine=debug,alm_strategy=debug,alm_ledger=debug,tower_http=debug" \
      cargo run --bin alm-herald

# ── WASM ──────────────────────────────────────────────────────────────────────

# Build alm-wasm (bundler target) and sync artifacts into mallow-client/vendor/alm-wasm
# so Vercel can resolve the local link: dep when mallow-client is deployed from its own repo.
build-wasm:
    cd almanac/crates/alm-wasm && wasm-pack build --target bundler
    rm -f almanac/crates/alm-wasm/pkg/.gitignore
    cp -r almanac/crates/alm-wasm/pkg/. mallow-client/vendor/alm-wasm/

# Copy already-built pkg/ artifacts into mallow-client/vendor/alm-wasm (no recompile).
# Use after build-wasm when you only changed non-Rust files and want a fast sync.
sync-wasm:
    @test -f almanac/crates/alm-wasm/pkg/alm_wasm_bg.wasm || \
      (echo "pkg/ not found — run 'just build-wasm' first" && exit 1)
    rm -f almanac/crates/alm-wasm/pkg/.gitignore
    cp -r almanac/crates/alm-wasm/pkg/. mallow-client/vendor/alm-wasm/
    @echo "synced pkg/ → mallow-client/vendor/alm-wasm/"

# ── Specs ─────────────────────────────────────────────────────────────────────

# Generate OpenAPI/Swagger specs for all (or specific) services → specs/
# Examples: just gen-specs   |   just gen-specs herald   |   just gen-specs identity helm
gen-specs *services:
    @bash gen-specs.sh {{services}}

# ── Thesis ────────────────────────────────────────────────────────────────────

# Compile LaTeX thesis → Thesis/DoAn.pdf
thesis:
    @bash Thesis/compile.sh

# Compile and open PDF
thesis-open:
    @bash Thesis/compile.sh --open
