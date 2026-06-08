use alm_core::signal::Signal;

/// Bars older than this are treated as warm-up only — no signals emitted.
pub const FRESHNESS_GATE_MS: i64 = 2 * 60 * 1000;

/// Snapshot of a registered hand returned by [`Registry::list_hands`].
/// Named struct instead of a positional tuple so callers don't have to count fields.
#[derive(Debug, Clone)]
pub struct HandListEntry {
    pub hand_id:   String,
    pub helm_id:   String,
    pub symbol:    String,
    pub script:    String,
    pub timeframe: String,
    pub exchange:  String,
    pub is_future: bool,
}

/// A single signal emitted by one hand on one bar advance, with routing metadata.
/// Pushed through an mpsc channel to the Handler which publishes to NATS.
#[derive(Debug, Clone, serde::Serialize)]
pub struct HandSignal {
    pub hand_id: String,
    pub helm_id: String,
    pub bar_ts: i64,
    pub signal: Signal,
}
