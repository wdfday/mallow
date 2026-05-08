pub mod broker;
pub mod bus_sync;
pub mod clock;
pub mod engine;
pub mod multi_engine;
pub mod runner;
pub mod walk_forward;

// Historical bar loading + canonical backtest runner.
// Moved from the former `logbook` crate when the HTTP surface collapsed
// into `herald`: keeping the library-side here puts engine run helpers
// next to the `Engine` itself, so any consumer (CLI, HTTP, NATS) gets
// the same dispatcher without a detour.
pub mod backtest;
pub mod curve_compress;
pub mod data;
pub mod types;

pub use bus_sync::SyncBus;
pub use engine::Engine;
pub use multi_engine::{MultiEngine, MultiStrategy};
pub use runner::{run_batch, run_portfolio, PortfolioReport, SymbolBars};
pub use walk_forward::{walk_forward, walk_forward_sync, WalkForwardConfig, WalkForwardMode, WalkForwardResult, WalkForwardWindow};
