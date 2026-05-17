//! Shared helpers for OHLCV and unified data handlers.

use alm_core::Timeframe;
use axum::{http::StatusCode, response::{IntoResponse, Response}, Json};

use super::super::types::{BarRecord, ErrorResponse};

pub const DEFAULT_LIMIT: usize = 500;
pub const MAX_LIMIT: usize = 5_000;
pub const MAX_INDICATORS_PER_REQUEST: usize = 16;

/// Paging result over a slice of bars sorted by timestamp ascending.
pub struct Page {
    pub bars: Vec<BarRecord>,
    pub next_before: Option<i64>,
    pub next_after: Option<i64>,
    pub truncated_below: bool,
}

/// Apply the three supported cursor modes (`before`, `after`, neither)
/// against `bars` which must be sorted ascending by `t`.
pub fn paginate(
    bars_asc: &[alm_core::Bar],
    before: Option<i64>,
    after: Option<i64>,
    limit: usize,
    window_starts_at: Option<i64>,
) -> Page {
    // Fast path: empty ledger window.
    if bars_asc.is_empty() {
        return Page {
            bars: Vec::new(),
            next_before: None,
            next_after: None,
            truncated_below: before.is_some() && window_starts_at.is_none(),
        };
    }

    let first_t = bars_asc.first().map(|b| b.timestamp).unwrap_or(0);

    // Select the raw range according to the cursor.
    let range: Vec<&alm_core::Bar> = match (before, after) {
        (Some(cut), _) => {
            // Bars with t < cut, take newest `limit` of those.
            let mut hits: Vec<&alm_core::Bar> =
                bars_asc.iter().filter(|b| b.timestamp < cut).collect();
            if hits.len() > limit {
                let skip = hits.len() - limit;
                hits.drain(..skip);
            }
            hits
        }
        (None, Some(start)) => bars_asc
            .iter()
            .filter(|b| b.timestamp > start)
            .take(limit)
            .collect(),
        (None, None) => {
            // Last `limit` bars.
            let skip = bars_asc.len().saturating_sub(limit);
            bars_asc.iter().skip(skip).collect()
        }
    };

    let records: Vec<BarRecord> = range.iter().map(|b| BarRecord::from(*b)).collect();

    let next_before = records.first().map(|r| r.t);
    let next_after = records.last().map(|r| r.t);

    let truncated_below = match before {
        Some(cut) => cut <= first_t && window_starts_at.is_some(),
        None if after.is_none() => false,
        _ => false,
    };

    Page {
        bars: records,
        next_before,
        next_after,
        truncated_below,
    }
}

pub fn clamp_limit(raw: Option<usize>, default: usize) -> usize {
    raw.unwrap_or(default).clamp(1, MAX_LIMIT)
}

pub fn resolve_tf(raw: &Option<String>, default: Timeframe) -> Timeframe {
    raw.as_deref()
        .and_then(parse_tf)
        .unwrap_or(default)
}

pub fn parse_tf(s: &str) -> Option<Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),
        "M5"  => Some(Timeframe::M5),
        "M15" => Some(Timeframe::M15),
        "M30" => Some(Timeframe::M30),
        "H1"  => Some(Timeframe::H1),
        "H4"  => Some(Timeframe::H4),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _ => None,
    }
}

pub fn err(status: StatusCode, msg: impl Into<String>) -> Response {
    (status, Json(ErrorResponse { status: status.as_u16(), code: None, message: msg.into() })).into_response()
}
