//! `POST /api/script/validate` — syntax + semantic lint for strategy scripts.
//!
//! Designed to power the Monaco editor's real-time red-underline feedback:
//!
//! ```text
//! POST /api/script/validate
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
//! 3. **Script syntax errors** — indicator declarations are stripped before the
//!    cleaned script is compiled; errors are mapped back to original line numbers.

use axum::{
    extract::State,
    response::Response,
    Json, Router,
};
use axum::routing::post;
use serde::{Deserialize, Serialize};
use utoipa::ToSchema;

use alm_strategy::{script_lint, LintDiagnostic, ScriptLintScope};

use super::types::ok;
use super::HttpState;

// ── Request / response types ──────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct ScriptValidateReq {
    /// Full strategy script to lint.
    pub script: String,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct ScriptValidateResp {
    /// List of diagnostics. Empty → script is clean.
    pub errors: Vec<LintDiagnostic>,
    /// Scope information for Monaco autocomplete / hover providers.
    pub scope: ScriptLintScope,
}

// ── Route ─────────────────────────────────────────────────────────────────────

pub fn routes() -> Router<HttpState> {
    Router::new().route("/api/v1/script/validate", post(validate_script))
}

// ── Handler ───────────────────────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/script/validate",
    request_body = ScriptValidateReq,
    responses(
        (status = 200, description = "Lint result — empty errors means clean script",
         body = ScriptValidateResp),
        (status = 400, description = "Request body malformed")
    ),
    tag = "backtest"
)]
pub async fn validate_script(
    State(_state): State<HttpState>,
    Json(req): Json<ScriptValidateReq>,
) -> Response {
    // Run lint synchronously — it's pure CPU with no I/O, typically < 1 ms.
    // Scripts that are very large could theoretically block, but script compile
    // is fast and lint scripts are tiny; no need for spawn_blocking here.
    let (errors, scope) = script_lint(&req.script);
    ok(ScriptValidateResp { errors, scope })
}
