//! `POST /api/rhai/validate` — syntax + semantic lint for Rhai strategy scripts.
//!
//! Designed to power the Monaco editor's real-time red-underline feedback:
//!
//! ```text
//! POST /api/rhai/validate
//! { "script": "let ema9 = indi.ema(9);\nif close[0] > ema9[0] { entry = true; }" }
//!
//! → 200 OK
//! {
//!   "errors": [
//!     { "line": 1, "col": 1,
//!       "message": "Wrong indicator prefix 'indi.'; use 'ind.ema(' instead",
//!       "severity": "error" }
//!   ],
//!   "scope": {
//!     "indicators": [],
//!     "bar_fields":  ["open","high","low","close","volume"],
//!     "output_vars": ["long","short","exit","tp","sl","strength","is_offset","reason"],
//!     "functions":   ["cross_above","cross_below",...]
//!   }
//! }
//! ```
//!
//! Checks performed:
//!
//! 1. **Wrong prefix** — `indi.ema`, `indicators.rsi`, etc. (must be `ind.TYPE`)
//! 2. **Unknown type** — `ind.mma(9)` flags `mma` as unknown with a fuzzy
//!    "did you mean `ema`?" suggestion (Levenshtein ≤ 2).
//! 3. **Rhai syntax errors** — indicator declarations are stripped before the
//!    cleaned script is compiled; errors are mapped back to original line numbers.

use axum::{
    extract::State,
    http::StatusCode,
    response::{IntoResponse, Response},
    Json, Router,
};
use axum::routing::post;
use serde::{Deserialize, Serialize};
use utoipa::ToSchema;

use alm_strategy::{rhai_lint, LintDiagnostic, RhaiLintScope};

use super::HttpState;

// ── Request / response types ──────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct RhaiValidateReq {
    /// Full Rhai strategy script to lint.
    pub script: String,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct RhaiValidateResp {
    /// List of diagnostics. Empty → script is clean.
    pub errors: Vec<LintDiagnostic>,
    /// Scope information for Monaco autocomplete / hover providers.
    pub scope: RhaiLintScope,
}

// ── Route ─────────────────────────────────────────────────────────────────────

pub fn routes() -> Router<HttpState> {
    Router::new().route("/api/v1/rhai/validate", post(validate_rhai))
}

// ── Handler ───────────────────────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/rhai/validate",
    request_body = RhaiValidateReq,
    responses(
        (status = 200, description = "Lint result — empty errors means clean script",
         body = RhaiValidateResp),
        (status = 400, description = "Request body malformed")
    ),
    tag = "backtest"
)]
pub async fn validate_rhai(
    State(_state): State<HttpState>,
    Json(req): Json<RhaiValidateReq>,
) -> Response {
    // Run lint synchronously — it's pure CPU with no I/O, typically < 1 ms.
    // Scripts that are very large could theoretically block, but Rhai compile
    // is fast and lint scripts are tiny; no need for spawn_blocking here.
    let (errors, scope) = rhai_lint(&req.script);
    (StatusCode::OK, Json(RhaiValidateResp { errors, scope })).into_response()
}
