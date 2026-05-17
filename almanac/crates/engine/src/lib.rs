//! # alm-engine — Event-driven backtesting engine
//!
//! Two engines share the same event loop and component model:
//!
//! | Engine | Symbols | Use case |
//! |---|---|---|
//! | [`Engine`] | 1 | Single-symbol strategy, walk-forward, optimisation |
//! | [`MultiEngine`] | N | Pair trading, cross-symbol filters, portfolio allocation |
//!
//! ## Event loop
//!
//! Both engines communicate exclusively through a [`SyncBus`] (VecDeque, zero atomic
//! overhead). Each bar triggers a cascade of events processed in order:
//!
//! ```text
//! BarFeed ──► MarketEvent
//!                │
//!                ├─► SimBroker::process_pending   ──► FillEvent (pending orders from prev bar)
//!                ├─► Portfolio::record_equity
//!                ├─► PositionTracker::update_and_check ──► force_close if SL/TP/time hit
//!                ├─► Strategy::on_bar / on_window ──► SignalEvent
//!                │
//!                └── SignalEvent
//!                        │
//!                        ├─► RiskManager::validate + size
//!                        └─► OrderEvent ──► SimBroker::submit ──► (filled next bar)
//! ```
//!
//! **No look-ahead bias**: market orders submitted at bar[i] fill at bar[i+1].open
//! (via `SimBroker::process_pending`). The signal and the fill are always in different bars.
//!
//! ## Engine — single symbol
//!
//! ```text
//! ┌──────────────────────────────────────────────────┐
//! │ Engine<S, R, B>                                  │
//! │                                                  │
//! │  BarFeed ──► bar                                 │
//! │              │                                   │
//! │  ┌───────────▼────────────────────────────────┐  │
//! │  │  dispatch(MarketEvent)                     │  │
//! │  │  1. last_price = bar.close                 │  │
//! │  │  2. risk.on_bar(bar)                       │  │
//! │  │  3. broker.process_pending(bar) → fills    │  │
//! │  │  4. portfolio.record_equity                │  │
//! │  │  5. position_trackers.update_and_check     │  │
//! │  │     └─ exit rule hit → force_close         │  │
//! │  │  6. portfolio.snapshot → strategy (opt-in) │  │
//! │  │  7. strategy.on_bar / on_window → signals  │  │
//! │  └────────────────────────────────────────────┘  │
//! │                                                  │
//! │  dispatch(SignalEvent)                           │
//! │    Long/Short → RiskManager → OrderEvent         │
//! │    Close      → force market order               │
//! │                                                  │
//! │  dispatch(FillEvent)                             │
//! │    apply_fill → open: create PositionTracker     │
//! │               → close: propagate MAE/MFE/bars    │
//! └──────────────────────────────────────────────────┘
//! ```
//!
//! ### Builder options
//!
//! ```rust,ignore
//! Engine::sync(capital, strategy, risk, commission, slippage)
//!     .with_exit_rules(ExitRules { stop_loss_pct: Some(0.05), .. })
//!     .with_candle_transform(CandleTransform::new(CandleType::HeikenAshi, 0))
//!     .with_next_bar(true)      // buffer signals, fill at next bar.open explicitly
//!     .with_single_entry()      // block re-entry while position is open
//!     .with_window(300)         // on_window() sliding window size
//! ```
//!
//! ### PositionTracker
//!
//! Every open position has a `PositionTracker` that:
//! - Increments `bars_held` each bar
//! - Tracks `mae` (max adverse excursion) and `mfe` (max favorable excursion) from intra-bar H/L
//! - Holds signal-level absolute `stop_price` / `target_price` (set from `signal.sl` / `signal.tp`)
//! - Checks exit rules every bar using `intra_bar_mode` to determine fill price
//!
//! On close, `mae_pct`, `mfe_pct`, `bars_held`, `exit_reason` are written to the last `Trade`.
//!
//! ### Signal-level TP/SL
//!
//! A script strategy can set absolute prices in script:
//! ```rhai
//! let atr = atr14[0].atr;
//! tp = close + 3.0 * atr;   // written to signal.target_price
//! sl = close - 1.5 * atr;   // written to signal.stop_price
//! long = ema9[0] > ema21[0];
//! ```
//! The engine reads these at fill time and stores them in `PositionTracker`.
//!
//! ## MultiEngine — multiple symbols
//!
//! ### Feed priming and heap merge
//!
//! ```text
//! add_feed(btc_feed):
//!   btc_feed.next() → bar@t=1000  ──► heap.push((t=1000, "BTCUSDT", bar))
//!   feeds.push(btc_feed)              heap = [(1000,BTC)]
//!
//! add_feed(eth_feed):
//!   eth_feed.next() → bar@t=1000  ──► heap.push((t=1000, "ETHUSDT", bar))
//!                                     heap = [(1000,BTC), (1000,ETH)]  ← min-heap
//! ```
//!
//! ### Run loop (time-merge)
//!
//! ```text
//! loop:
//!   entry = heap.pop()          ← always smallest (timestamp, symbol) pair
//!   bar   = entry.bar
//!
//!   // refill: advance the feed that produced this bar
//!   find feed where feed.symbol() == bar.symbol
//!   if feed.next() → next_bar:
//!     heap.push((next_bar.timestamp, next_bar.symbol, next_bar))
//!
//!   dispatch(MarketEvent { bar })
//! ```
//!
//! When two symbols share the same timestamp, ordering is by symbol name (alphabetical),
//! ensuring deterministic, reproducible results.
//!
//! `last_prices` always holds the most recent known close for every symbol. When the
//! strategy receives BTC@t=2000, `last_prices["ETHUSDT"]` is ETH's close from the last
//! ETH bar processed — which may be t=1000 if ETH hasn't emitted a t=2000 bar yet.
//! This is the correct backtest assumption: you only know prices already published.
//!
//! ```text
//! Timeline (M1 feeds, same frequency):
//!
//!   t=1000: BTC pop → dispatch → last_prices={BTC:100}
//!   t=1000: ETH pop → dispatch → last_prices={BTC:100, ETH:50}
//!   t=2000: BTC pop → dispatch → last_prices={BTC:102, ETH:50}  ← ETH still at t=1000 close
//!   t=2000: ETH pop → dispatch → last_prices={BTC:102, ETH:51}
//! ```
//!
//! ### Differences from Engine
//!
//! | Feature | Engine | MultiEngine |
//! |---|---|---|
//! | Symbols | 1 | N |
//! | last price | `f64` | `HashMap<String, f64>` |
//! | bar windows | single `VecDeque` | `HashMap<String, VecDeque>` |
//! | next-bar buffering | ✓ | ✗ |
//! | candle transform | ✓ | ✓ |
//! | single-entry guard | ✓ | ✓ |
//! | regime detection | via script | via script |
//! | reports | 1 | 1 per symbol |

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
