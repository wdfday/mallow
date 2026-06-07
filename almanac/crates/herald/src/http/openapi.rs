//! OpenAPI spec generation and Swagger UI routes.

use axum::{routing::get, Router};
use utoipa::openapi::security::{HttpAuthScheme, HttpBuilder, SecurityScheme};
use utoipa::{Modify, OpenApi};
use utoipa_swagger_ui::SwaggerUi;

use alm_engine::types::{
    BacktestRequest, MonteCarloConfig,
    ScriptBacktestRequest, WalkForwardConfig,
};

use alm_strategy::{LintDiagnostic, ScriptLintScope, DeclaredIndicator};

use super::{backtest, data, script_validate, strategy, symbols, types, HttpState};

// ── Security modifier ─────────────────────────────────────────────────────────

/// Injects the `bearerAuth` security scheme and applies it globally to every
/// route in the spec. Herald sits behind api-gateway which validates the JWT;
/// the scheme here is purely documentary so Swagger UI shows the padlock and
/// the "Authorize" button lets testers paste a token.
struct BearerAuthAddon;

impl Modify for BearerAuthAddon {
    fn modify(&self, openapi: &mut utoipa::openapi::OpenApi) {
        let components = openapi.components.get_or_insert_with(Default::default);
        components.add_security_scheme(
            "bearerAuth",
            SecurityScheme::Http(
                HttpBuilder::new()
                    .scheme(HttpAuthScheme::Bearer)
                    .bearer_format("JWT")
                    .description(Some(
                        "JWT issued by the identity service. \
                         Pass as `Authorization: Bearer <token>`.",
                    ))
                    .build(),
            ),
        );
        // Apply to every operation by default.
        openapi.security = Some(vec![
            utoipa::openapi::security::SecurityRequirement::new::<&str, [&str; 0], &str>(
                "bearerAuth",
                [],
            ),
        ]);
    }
}

// ── OpenAPI document ──────────────────────────────────────────────────────────

#[derive(OpenApi)]
#[openapi(
    info(title = "Herald API", version = "0.1.0", description = "Signal engine HTTP API"),
    modifiers(&BearerAuthAddon),
    paths(
        symbols::list_symbols,
        symbols::list_indicators_catalog,
        data::unified::unified_data,
        backtest::list_strategies,
        backtest::run_backtest,
        backtest::run_backtest_script,
        script_validate::validate_script,
        strategy::list_strategies,
        strategy::list_my_strategies,
        strategy::create_strategy,
        strategy::get_strategy,
        strategy::list_strategy_chain,
        strategy::update_strategy,
        strategy::delete_strategy,
    ),
    components(schemas(
        // Live
        types::ErrorResponse,
        types::BarRecord,
        types::CandlesQuery,
        types::CandlesResult,
        types::UnifiedDataRequest,
        types::UnifiedDataResponse,
        // Backtest
        BacktestRequest,
        ScriptBacktestRequest,
        MonteCarloConfig,
        WalkForwardConfig,
        // Strategy store
        strategy::types::StrategySpec,
        strategy::types::Strategy,
        strategy::types::CreateStrategyReq,
        strategy::types::UpdateStrategyReq,
        // Script validate
        script_validate::ScriptValidateReq,
        script_validate::ScriptValidateResp,
        LintDiagnostic,
        ScriptLintScope,
        DeclaredIndicator,
    )),
    tags(
        (name = "live",     description = "Live ledger data"),
        (name = "backtest", description = "Backtest execution"),
        (name = "strategy", description = "Saved strategy versions"),
    )
)]
pub struct ApiDoc;

pub fn routes() -> Router<HttpState> {
    let api_doc = ApiDoc::openapi();
    let yaml_spec = serde_yaml::to_string(&api_doc).expect("openapi yaml serialize");

    Router::new()
        .merge(
            SwaggerUi::new("/swagger/herald")
                .url("/api-doc/openapi.json", api_doc),
        )
        .route(
            "/api-doc/openapi.yaml",
            get(move || {
                let spec = yaml_spec.clone();
                async move {
                    (
                        [(axum::http::header::CONTENT_TYPE, "application/yaml")],
                        spec,
                    )
                }
            }),
        )
}
