//! # alm-ledger — live market state machine
//!
//! A process-local, thread-safe sliding bar window per `(symbol, timeframe)`.
//! Both the bot registry (via strategy evaluation) and HTTP endpoints read
//! from the same ledger. Indicators are computed client-side in WASM.
//!
//! ## Invariants
//!
//! 1. Out-of-order / duplicate bars are rejected — state is monotonic.
//! 2. Observers are isolated via `catch_unwind` — a panicking observer does
//!    not crash the ingestor.
//! 3. Bootstrap feeds bars without notifying observers — bots must not react
//!    to replayed historical bars.

mod state;
mod ledger;
mod bootstrap;

pub use state::{AdvanceOutcome, SymbolState};
pub use ledger::{Ledger, LedgerConfig, LedgerObserver, SymbolKey};
pub use bootstrap::BootstrapReport;
