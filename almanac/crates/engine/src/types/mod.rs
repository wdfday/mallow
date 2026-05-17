//! Backtest DTOs — split by direction:
//! - [`request`] — inputs from callers (HTTP handlers, CLI, NATS)
//! - [`response`] — outputs from the engine runner

pub mod request;
pub mod response;

pub use request::{BacktestRequest, MonteCarloConfig, ScriptBacktestRequest, WalkForwardConfig};
pub use response::{
    ActivityStats, BacktestResponse, BuyHoldBenchmarkResponse, CalendarStats, CapitalStats,
    CurvePoint, CurveStats, DistributionStats, DrawdownStats, ErrorResponse, ExcursionStats,
    ExitReasonBreakdownResponse, LongShortStats, MonteCarloResponse, RegimeSummaryResponse,
    RegimeTradeStatsResponse, ReturnStats, RiskAdjustedStats, TradeResponse, TradeStats,
    WalkForwardResponse, WalkForwardWindowResponse,
};
