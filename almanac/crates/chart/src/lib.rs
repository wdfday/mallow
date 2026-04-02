/// alm-chart — visualization layer for backtesting results and pattern debugging.
///
/// # Two rendering backends
/// - **charming** (Apache ECharts): interactive HTML output, ideal for browser debugging,
///   pattern overlays, zoom/pan, hover tooltips.
/// - **plotters**: static PNG/SVG output, ideal for unit-test artifacts and cross-validation
///   against TA-Lib reference data.
///
/// # Modules
/// - [`candle`]   — candlestick chart with indicator overlays
/// - [`equity`]   — equity curve + drawdown chart
/// - [`pattern`]  — geometric overlays: pivot points, trendlines, zones, annotations
/// - [`compare`]  — side-by-side charts for cross-validation (algo vs reference)
/// - [`render`]   — output helpers: write HTML / PNG / SVG to disk

pub mod candle;
pub mod compare;
pub mod equity;
pub mod pattern;
pub mod render;
