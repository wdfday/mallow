#!/usr/bin/env bash
# gen-specs.sh — Generate OpenAPI/Swagger specs for all services and collect
# them under mallow/specs/.
#
# Usage:
#   ./gen-specs.sh            # generate all specs
#   ./gen-specs.sh herald     # generate only herald
#   ./gen-specs.sh identity helm gateway
#
# Output: specs/{service}.yaml
#
# Requirements (all optional per-service):
#   Go services   — go (in PATH), swaggo/swag pulled via go run
#   herald        — cargo (in PATH), run from almanac/
#   just          — optional; the swag commands are inlined so just is not needed

set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
SPECS_DIR="$ROOT/specs"

mkdir -p "$SPECS_DIR"

# ─── colour helpers ───────────────────────────────────────────────────────────
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓${NC} $*"; }
warn() { echo -e "${YELLOW}⚠${NC}  $*"; }
fail() { echo -e "${RED}✗${NC} $*"; }

# ─── per-service generators ───────────────────────────────────────────────────

gen_identity() {
    echo "→ identity"
    (
        cd "$ROOT/identity"
        go run github.com/swaggo/swag/cmd/swag@v1.8.12 init \
            -g cmd/identity/swagger.go -o docs \
            --parseInternal --parseDependency --parseDepth 3 \
            --quiet 2>/dev/null || \
        go run github.com/swaggo/swag/cmd/swag@v1.8.12 init \
            -g cmd/identity/swagger.go -o docs \
            --parseInternal --parseDependency --parseDepth 3
    )
    cp "$ROOT/identity/docs/swagger.yaml" "$SPECS_DIR/identity.yaml"
    ok "identity → specs/identity.yaml"
}

gen_helm() {
    echo "→ helm"
    (
        cd "$ROOT/helm"
        go run github.com/swaggo/swag/cmd/swag@v1.16.4 init \
            -g cmd/helm/swagger.go -o docs \
            --parseInternal --parseDependency --parseDepth 3 \
            --quiet 2>/dev/null || \
        go run github.com/swaggo/swag/cmd/swag@v1.16.4 init \
            -g cmd/helm/swagger.go -o docs \
            --parseInternal --parseDependency --parseDepth 3
    )
    cp "$ROOT/helm/docs/swagger.yaml" "$SPECS_DIR/helm.yaml"
    ok "helm → specs/helm.yaml"
}

gen_gateway() {
    echo "→ api-gateway"
    (
        cd "$ROOT/api-gateway"
        go run github.com/swaggo/swag/cmd/swag@latest init \
            -g cmd/gateway/swagger.go --output docs \
            --quiet 2>/dev/null || \
        go run github.com/swaggo/swag/cmd/swag@latest init \
            -g cmd/gateway/swagger.go --output docs
    )
    cp "$ROOT/api-gateway/docs/swagger.yaml" "$SPECS_DIR/gateway.yaml"
    ok "gateway → specs/gateway.yaml"
}

gen_herald() {
    echo "→ herald (Rust / utoipa)"
    (
        cd "$ROOT/almanac"
        cargo run --bin gen-openapi --quiet -- yaml > "$SPECS_DIR/herald.yaml" 2>/dev/null || \
        cargo run --bin gen-openapi -- yaml > "$SPECS_DIR/herald.yaml"
    )
    ok "herald → specs/herald.yaml"
}

# ─── dispatch ─────────────────────────────────────────────────────────────────

ALL=(identity helm gateway herald)

run_service() {
    local svc=$1
    case "$svc" in
        identity)   gen_identity   ;;
        helm)       gen_helm       ;;
        gateway)    gen_gateway    ;;
        herald)     gen_herald     ;;
        *)
            fail "unknown service: $svc (valid: ${ALL[*]})"
            return 1
            ;;
    esac
}

if [[ $# -eq 0 ]]; then
    targets=("${ALL[@]}")
else
    targets=("$@")
fi

FAILED=()
for svc in "${targets[@]}"; do
    if ! run_service "$svc"; then
        FAILED+=("$svc")
        fail "$svc failed"
    fi
done

echo ""
if [[ ${#FAILED[@]} -eq 0 ]]; then
    ok "All specs written to $SPECS_DIR/"
    ls -lh "$SPECS_DIR"/*.yaml 2>/dev/null
else
    warn "Failed: ${FAILED[*]}"
    exit 1
fi
