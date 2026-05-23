use serde::Serialize;

// ── WatchSignalBatch ──────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize)]
pub struct WatchSignalBatch {
    pub watch_id:     String,
    pub symbol:       String,
    pub bar_ts:       i64,
    pub signals:      Vec<WatchSignal>,
    #[serde(skip)]
    pub webhook_url:  Option<String>,
    #[serde(skip)]
    pub nats_subject: Option<String>,
}

// ── WatchSignal ───────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize)]
pub struct WatchSignal {
    pub direction: String,
    pub strength:  f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub price:     Option<f64>,
}

impl From<&alm_core::signal::Signal> for WatchSignal {
    fn from(s: &alm_core::signal::Signal) -> Self {
        Self {
            direction: format!("{:?}", s.direction).to_lowercase(),
            strength:  s.strength,
            price:     s.price,
        }
    }
}
