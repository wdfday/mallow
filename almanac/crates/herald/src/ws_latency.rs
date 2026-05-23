//! WebSocket bar delivery latency tracker.
//!
//! Each feed task calls [`WsLatencyTracker::record`] for every **closed** bar,
//! passing the source name and the computed delivery latency:
//!
//! ```text
//! latency_ms = received_at_ms − (bar.open_time_ms + tf.duration_ms())
//! ```
//!
//! The tracker keeps a sliding window of the last [`WINDOW`] samples per
//! source and exposes snapshot stats for the admin endpoint.

use std::collections::{HashMap, VecDeque};
use std::sync::Mutex;
use std::time::Instant;

use serde::Serialize;

/// Number of closed-bar samples kept per source.
const WINDOW: usize = 1_000;

// ── Per-source window ─────────────────────────────────────────────────────────

#[derive(Default)]
struct SourceWindow {
    /// Latency values (ms) in chronological order, newest at back.
    samples:   VecDeque<i64>,
    /// Wall-clock time of the most-recently recorded bar.
    last_seen: Option<Instant>,
}

impl SourceWindow {
    fn push(&mut self, source: &str, latency_ms: i64) {
        self.samples.push_back(latency_ms);
        if self.samples.len() > WINDOW {
            self.samples.pop_front();
        }
        self.last_seen = Some(Instant::now());
        // Also emit to the global Prometheus recorder so Grafana can graph it.
        metrics::histogram!("herald_ws_bar_latency_ms", "source" => source.to_string())
            .record(latency_ms as f64);
    }

    fn stats(&self) -> Option<SourceStats> {
        if self.samples.is_empty() {
            return None;
        }
        let n = self.samples.len();
        let last_ms = *self.samples.back().unwrap();

        let mut sorted: Vec<i64> = self.samples.iter().copied().collect();
        sorted.sort_unstable();

        let mean_ms = sorted.iter().sum::<i64>() / n as i64;
        let p50_ms  = pct(&sorted, 50);
        let p95_ms  = pct(&sorted, 95);
        let p99_ms  = pct(&sorted, 99);
        let max_ms  = *sorted.last().unwrap();

        let last_seen_secs = self.last_seen
            .map(|i| i.elapsed().as_secs())
            .unwrap_or(u64::MAX);

        Some(SourceStats { sample_count: n, last_ms, mean_ms, p50_ms, p95_ms, p99_ms, max_ms, last_seen_secs })
    }
}

fn pct(sorted: &[i64], p: usize) -> i64 {
    let idx = (sorted.len() * p / 100).min(sorted.len() - 1);
    sorted[idx]
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Tracks WebSocket bar delivery latency per exchange source.
///
/// Thread-safe — feed tasks and the HTTP handler hold the same `Arc`.
pub struct WsLatencyTracker {
    inner: Mutex<HashMap<String, SourceWindow>>,
}

impl WsLatencyTracker {
    pub fn new() -> Self {
        Self { inner: Mutex::new(HashMap::new()) }
    }

    /// Record one closed-bar delivery latency for `source` (e.g. `"binance"`).
    pub fn record(&self, source: &str, latency_ms: i64) {
        self.inner
            .lock()
            .unwrap()
            .entry(source.to_string())
            .or_default()
            .push(source, latency_ms);
    }

    /// Snapshot stats for every source seen so far.
    pub fn snapshot(&self) -> HashMap<String, SourceStats> {
        self.inner
            .lock()
            .unwrap()
            .iter()
            .filter_map(|(src, w)| w.stats().map(|s| (src.clone(), s)))
            .collect()
    }
}

/// Delivery-latency statistics for one exchange source.
#[derive(Debug, Clone, Serialize)]
pub struct SourceStats {
    /// Number of samples in the current window (max [`WINDOW`]).
    pub sample_count:   usize,
    /// Most recent latency (ms).
    pub last_ms:        i64,
    /// Mean latency (ms) over the window.
    pub mean_ms:        i64,
    /// 50th-percentile latency (ms).
    pub p50_ms:         i64,
    /// 95th-percentile latency (ms).
    pub p95_ms:         i64,
    /// 99th-percentile latency (ms).
    pub p99_ms:         i64,
    /// Max latency (ms) in the window.
    pub max_ms:         i64,
    /// Seconds elapsed since the most-recently received bar from this source.
    pub last_seen_secs: u64,
}
