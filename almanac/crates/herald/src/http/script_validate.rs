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

fn parse_timeframe(s: &str) -> Option<Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),  "M3"  => Some(Timeframe::M3),
        "M5"  => Some(Timeframe::M5),  "M15" => Some(Timeframe::M15),
        "M30" => Some(Timeframe::M30), "H1"  => Some(Timeframe::H1),
        "H2"  => Some(Timeframe::H2),  "H4"  => Some(Timeframe::H4),
        "H6"  => Some(Timeframe::H6),  "H12" => Some(Timeframe::H12),
        "D1"  => Some(Timeframe::D1),  "W1"  => Some(Timeframe::W1),
        _ => None,
    }
}
use serde::{Deserialize, Serialize};
use utoipa::ToSchema;

use alm_core::Timeframe;
use alm_strategy::{script_lint, LintDiagnostic, ScriptLintScope};

use super::types::ok;
use super::HttpState;

// ── Request / response types ──────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct ScriptValidateReq {
    /// Full strategy script to lint.
    pub script: String,
    /// Optional base timeframe (e.g. `"M1"`, `"H1"`). When provided, the linter
    /// will flag any HTF indicator declared on a TF smaller than the base TF.
    #[serde(default)]
    pub base_tf: Option<String>,
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
    let base_tf = req.base_tf.as_deref().and_then(parse_timeframe);
    let (errors, scope) = script_lint(&req.script, base_tf);
    ok(ScriptValidateResp { errors, scope })
}
