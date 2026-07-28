# C4 Model — Mallow

Real C4 tooling (Structurizr), not Mermaid-in-markdown — `workspace.dsl` is
the source of truth, rendered live by a small local server.

## Run it

```bash
cd docs/c4
docker compose up
```

Open **http://localhost:8079**. The server watches `workspace.dsl` and
reloads on save — edit the DSL, refresh the browser, see the updated
diagram. Port 8079 (not the default 8080) to avoid colliding with
api-gateway/8080, identity/8082, helm/8084, herald/8090 when this runs
alongside the main stack.

```bash
docker compose down    # stop when done
```

`workspace.json` and `.structurizr/` get generated on startup — gitignored,
not source, safe to delete anytime.

## What's in `workspace.dsl`

- **System Context** view — Mallow as one system, the Trader, and the
  external systems it talks to (Exchange APIs, Google OAuth2, Telegram).
- **Container** view — every deployable container inside Mallow
  (mallow-client, api-gateway, identity, helm, herald, hist-data, NATS,
  PostgreSQL, Redis) and how they actually talk to each other, per
  [CLAUDE.md](../../CLAUDE.md)'s Service Map and NATS subject table.

`thstrategist/` is deliberately not modeled — zero commits in git history
as of 2026-07-28, documented in CLAUDE.md as a planned Hub service but never
actually built (same reason `.github/workflows/ci.yml` excludes it).

## Note on the image

`structurizr/lite` is deprecated by the Structurizr team — the image now
just prints a migration notice and exits instead of serving anything.
`docker-compose.yml` here uses the replacement they point to,
`structurizr/structurizr local`, same volume-mount contract (mount the
directory containing `workspace.dsl` at `/usr/local/structurizr`).

## Keeping this in sync

This describes the system as it actually runs today, not the aspirational
architecture. When a service/container/relationship changes for real (new
service, dropped dependency, moved subject), update `workspace.dsl` in the
same PR — a stale C4 diagram is worse than no diagram, since it actively
misleads instead of just being silent.
