pub mod binance;
pub mod okx;
pub mod rest;

use tokio::sync::mpsc;
use alm_core::{Bar, Timeframe};

// ── Timestamp helper ──────────────────────────────────────────────────────────

/// Current Unix time in milliseconds. Used by feed tasks to timestamp
/// bar arrival for latency measurement.
#[inline]
pub fn now_ms() -> i64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

// ── BarEvent ──────────────────────────────────────────────────────────────────

/// A single bar event from a WebSocket kline feed.
///
/// `closed = true`  → confirmed, advance the ledger window.
/// `closed = false` → forming,   update `live_bar` only (no observer fan-out).
#[derive(Debug, Clone)]
pub struct BarEvent {
    pub tf:             Timeframe,
    pub bar:            Bar,
    pub closed:         bool,
    /// Unix-ms timestamp captured at WebSocket message receipt, before parsing.
    /// Used to compute delivery latency for closed bars.
    pub received_at_ms: i64,
}

/// Bounded bar channel capacity.
/// Sized to absorb a brief burst (all symbols × a few ticks) while applying
/// backpressure to the WS feed when the handler loop falls behind.
pub const BAR_CHANNEL_CAP: usize = 512;

pub type BarTx = mpsc::Sender<BarEvent>;
pub type BarRx = mpsc::Receiver<BarEvent>;

// ── Subscribed TF set ─────────────────────────────────────────────────────────

/// Re-export from `config::timeframe` — the source of truth for live-subscribable TFs.
pub use alm_herald::config::timeframe::{parse_tf, SUBSCRIBE_TFS};
