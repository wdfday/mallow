//! OpenAPI spec generation and Swagger UI routes.

use axum::{routing::get, Router};
use utoipa::OpenApi;
use utoipa_swagger_ui::SwaggerUi;

use super::{backtest, data, sse, store, symbols, types, watch, HttpState};

#[derive(OpenApi)]
#[openapi(
    info(title = "Herald API", version = "0.1.0", description = "Signal engine HTTP API"),
    paths(
        symbols::list_symbols,
        symbols::list_indicators_catalog,
        data::ohlcv::get_data,
        data::ohlcv::get_latest,
        data::unified::unified_data,
        backtest::list_strategies,
        backtest::run_backtest,
        backtest::run_backtest_cel,
        backtest::run_backtest_rhai,
        data::duckdb::query,
        sse::stream_bars,
        sse::stream_signals,
        store::list_strategies,
        store::create_strategy,
        store::get_strategy,
        store::list_strategy_versions,
        store::update_strategy,
        store::delete_strategy,
        store::list_cases,
        store::create_case,
        store::get_case,
        store::update_case,
        store::delete_case,
        store::run_case,
        store::run_case_signals,
        store::list_results,
        store::get_result,
        store::delete_result,
        watch::list_watches,
        watch::create_watch,
        watch::get_watch,
        watch::delete_watch,
    ),
    components(schemas(
        types::ErrorResponse,
        types::BarRecord,
        types::DataResponse,
        types::DataQuery,
        types::LatestQuery,
        types::CandlesQuery,
        types::CandlesResult,
        types::IndicatorConfig,
        types::IndicatorPoint,
        types::UnifiedDataRequest,
        types::UnifiedDataResponse,
    )),
    tags(
        (name = "live",     description = "Live ledger data"),
        (name = "backtest", description = "Backtest execution"),
        (name = "store",    description = "Saved strategies, cases, results"),
        (name = "watch",    description = "Watchlist / warm-set management"),
        (name = "stream",   description = "SSE real-time push"),
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
