# Mallow

A polyglot algorithmic trading system: event-driven backtesting and live
signal generation for technical-analysis-based buy/sell strategies, with a
signal-following execution layer that routes approved signals to real
exchange accounts.

## Stack

| Layer | Tech |
|---|---|
| Backtesting / signal engine | Rust (`almanac/` workspace — `alm-core`, `alm-indicator`, `alm-strategy`, `alm-engine`, `alm-herald`, plus PyO3/WASM bindings) |
| Trade execution, identity, edge | Go — `helm`, `identity`, `api-gateway`, `hist-data` (each its own `go.mod`, independently deployable; linked via `go.work` for local dev) |
| Web UI | Next.js + Tailwind + shadcn/ui — `mallow-client` |
| Message broker | NATS + JetStream (pub/sub for bars/signals, durable streams for fills/positions/equity) |
| Persistence | PostgreSQL (one logical DB per service) + Redis (sessions, rate-limit, token blacklist) |
| Orchestration | Docker Compose — `deployment/docker-compose.yml` (dev) + `.staging.yml`/`.prod.yml`/`.trace.yml` overlays |
| Observability | Grafana, Prometheus, Loki, Tempo, Pyroscope (`just up-mon`) |

## Architecture

- **[docs/c4/](docs/c4/)** — C4 model: System Context + Container diagrams for the whole system.
- **[CLAUDE.md](CLAUDE.md)** — full reference: service map, NATS subjects, protobuf schema, environment variables, per-service internals. This is the primary architecture doc; the C4 diagrams are a visual entry point into it, not a replacement.
- **[docs/helm-c4.md](docs/helm-c4.md)** — helm service deep-dive (its own Context/Container/Component levels).

## Quick start

```bash
just infra      # nats + postgres + redis + identity + herald
just up         # full stack: gateway, identity, herald, helm
just up-mon     # full stack + monitoring (Grafana at :3000)
just down       # stop
```

Each service also has its own `justfile` (`identity/`, `helm/`, `api-gateway/`, `hist-data/`, `almanac/`) for build/test/swagger/docker recipes scoped to that service. `just --list` (root or per-service) shows what's available.

## Tests

```bash
just infra           # local dev stack, if a service's tests need it
cd identity && just test    # per Go service
cd almanac && cargo test    # Rust workspace
cd almanac/crates/alm-py && just test   # Python bindings + named-strategy/Rhai-script parity suite
```

## CI/CD

- `.github/workflows/ci.yml` — build+test per service (Go matrix, Rust workspace, alm-py), swagger drift check, `docker compose config` validation across all four compose tiers.
- `.github/workflows/benchmark.yml` — tracks the Rhai-script-vs-hardcoded-Rust performance benchmark over time (`gh-pages`), since engine changes there can trade correctness for a real perf cost that should stay visible.

## License

[GPLv3](LICENSE).
