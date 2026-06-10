# =============================================================================
# Herald — multi-stage build with cargo-chef dep caching
#
# Layer strategy
# ─────────────────────────────────────────────────────────────────────────────
# 1. chef-prepare  : generates recipe.json from Cargo manifests only
#                    → cached as long as Cargo.toml / Cargo.lock don't change
# 2. chef-cook     : compiles ALL dependencies from recipe.json
#                    → the fat cached layer; only busts on dep changes
# 3. builder       : copies real src/, compiles only the delta (alm-herald)
#                    → fast: typically 30-60 s instead of 5 min
# 4. runtime       : debian-slim + binary only, non-root user
# =============================================================================

# ── cargo-chef planner ────────────────────────────────────────────────────────
FROM rust:1.88-slim AS chef

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config libssl-dev protobuf-compiler \
    && rm -rf /var/lib/apt/lists/* \
    && cargo install cargo-chef --locked

# alm-core/build.rs expects proto/ one level above the workspace root.
COPY --from=proto . /proto

WORKDIR /app

# ── Stage 1: plan (Cargo manifests only) ─────────────────────────────────────
FROM chef AS planner

COPY Cargo.toml Cargo.lock ./
COPY crates/ crates/

# alm-py needs Python/maturin — exclude before planning so it doesn't pull
# PyO3 into the dep graph.
RUN sed -i '/alm-py/d' Cargo.toml

RUN cargo chef prepare --recipe-path recipe.json

# ── Stage 2: cook (compile all deps from recipe, no src) ─────────────────────
FROM chef AS cooker

COPY --from=planner /app/recipe.json recipe.json
COPY --from=planner /app/Cargo.toml  Cargo.toml
COPY --from=planner /app/Cargo.lock  Cargo.lock

RUN cargo chef cook --release --recipe-path recipe.json

# ── Stage 3: build (only the delta — herald source changed) ──────────────────
FROM chef AS builder

# Bring in the pre-built dep artifacts.
COPY --from=cooker /app/target              target
COPY --from=cooker /app/Cargo.toml          Cargo.toml
COPY --from=cooker /app/Cargo.lock          Cargo.lock

COPY crates/ crates/
# alm-py excluded consistently
RUN sed -i '/alm-py/d' Cargo.toml

RUN cargo build --release --bin alm-herald

# ── Stage 4: runtime ──────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates libssl3 wget \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -ms /bin/sh herald
USER herald

COPY --from=builder /app/target/release/alm-herald /usr/local/bin/herald

ENV NATS_URL=nats://nats:4222
ENV HERALD_HTTP_ADDR=0.0.0.0:8090
ENV HERALD_DATA_DIR=/data
ENV RUST_LOG=alm_herald=info,alm_ledger=info

EXPOSE 8090

# Healthcheck defined in docker-compose.yml, not here

CMD ["herald"]
