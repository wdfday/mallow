use chrono::{DateTime, Utc};

/// Determines the engine's current time.
/// Swap between `HistoricalClock` (backtest) and `LiveClock` (live) without changing engine code.
pub trait Clock: Send + Sync {
    fn now(&self) -> DateTime<Utc>;
}

/// Backtest clock — advances as bars are fed.
#[derive(Debug, Clone)]
pub struct HistoricalClock {
    pub current: DateTime<Utc>,
}

impl HistoricalClock {
    pub fn new(ts_ms: i64) -> Self {
        Self { current: DateTime::from_timestamp_millis(ts_ms).unwrap_or(DateTime::<Utc>::MIN_UTC) }
    }

    pub fn advance(&mut self, ts_ms: i64) {
        if let Some(dt) = DateTime::from_timestamp_millis(ts_ms) {
            self.current = dt;
        }
    }
}

impl Clock for HistoricalClock {
    fn now(&self) -> DateTime<Utc> {
        self.current
    }
}

/// Live clock — uses wall clock time.
pub struct LiveClock;

impl Clock for LiveClock {
    fn now(&self) -> DateTime<Utc> {
        Utc::now()
    }
}
