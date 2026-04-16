pub mod benchmark;
pub mod metrics;
pub mod monte_carlo;
pub mod portfolio_analytics;
pub mod report;

pub use benchmark::BuyHoldBenchmark;
pub use monte_carlo::{run as monte_carlo, MonteCarloConfig, MonteCarloResult};
pub use portfolio_analytics::{analyze as portfolio_analyze, PortfolioAnalytics};
pub use report::BacktestReport;
