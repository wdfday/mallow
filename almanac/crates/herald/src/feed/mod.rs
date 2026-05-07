pub mod binance;
pub mod okx;
pub mod rest;

use tokio::sync::mpsc;
use alm_core::{Bar, Timeframe};

// ── BarEvent ──────────────────────────────────────────────────────────────────

/// A single bar event from a WebSocket kline feed.
///
/// `closed = true`  → confirmed, advance the ledger window.
/// `closed = false` → forming,   update `live_bar` only (no observer fan-out).
#[derive(Debug, Clone)]
pub struct BarEvent {
    pub tf:     Timeframe,
    pub bar:    Bar,
    pub closed: bool,
}

pub type BarTx = mpsc::UnboundedSender<BarEvent>;
pub type BarRx = mpsc::UnboundedReceiver<BarEvent>;

// ── Subscribed TF set ─────────────────────────────────────────────────────────

/// Timeframes subscribed on every live WebSocket connection.
///
/// Both Binance and OKX support all of these. M10 / H8 are excluded because
/// they are not available on both exchanges.
pub const SUBSCRIBE_TFS: &[Timeframe] = &[
    Timeframe::M1,
    Timeframe::M5,
    Timeframe::M15,
    Timeframe::M30,
    Timeframe::H1,
    Timeframe::H4,
    Timeframe::D1,
    Timeframe::W1,
];
