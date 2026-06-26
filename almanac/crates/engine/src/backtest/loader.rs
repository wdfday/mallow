//! Bar loading for backtest requests — file discovery + feed hydration.

use std::path::Path;

use alm_core::Bar;
use alm_data::BarFeed;
use anyhow::{Context, Result};

use alm_core::Timeframe;

use crate::data::{find_parquet_files, load_bars, market_region_from_data_source, parse_date_ms};
use crate::types::BacktestRequest;

/// Returns `"BTCUSD"` when the symbol field is empty.
///
/// Accepts both raw (`"BTCUSDT"`) and exchange-prefixed (`"binance:BTCUSDT"`)
/// forms — herald's live endpoints use prefixed keys for ledger routing, but
/// parquet files on disk are organised by raw ticker. The prefix is stripped
/// here so backtest loading works regardless of which form the caller sends.
pub fn effective_symbol(s: &str) -> &str {
    let s = if s.is_empty() { "BTCUSD" } else { s };
    match s.find(':') {
        Some(pos) => &s[pos + 1..],
        None      => s,
    }
}

/// Low-level bar loader: load a specific symbol + TF combination.
///
/// Used by MTF backtest to load each timeframe's feed independently.
pub fn load_bars_for_tf(
    symbol: &str,
    tf: Option<&str>,
    data_source: Option<&str>,
    from_ms: Option<i64>,
    to_ms: Option<i64>,
    data_dir: &std::path::Path,
    history_overrides: Option<&std::collections::HashMap<String, Vec<alm_core::Bar>>>,
) -> anyhow::Result<Vec<alm_core::Bar>> {
    if let (Some(tf_str), Some(overrides)) = (tf, history_overrides) {
        if let Some(bars) = overrides.get(tf_str) {
            return Ok(bars.clone());
        }
    }
    let market_region = data_source
        .map(market_region_from_data_source)
        .unwrap_or("");
    let market_hours_only = !market_region.is_empty();
    let files = find_parquet_files(data_dir, symbol, tf, data_source);
    let mut feed = load_bars(&files, symbol, from_ms, to_ms, market_hours_only, market_region)
        .with_context(|| {
            format!(
                "loading bars for '{}' tf={}",
                symbol,
                tf.unwrap_or("auto")
            )
        })?;
    Ok(std::iter::from_fn(|| feed.next()).collect())
}

/// Discover Parquet files, load bars, and drain the feed into a `Vec<Bar>`.
pub fn load_bars_for_request(req: &BacktestRequest, data_dir: &Path) -> Result<Vec<Bar>> {
    if let (Some(ref tf_str), Some(ref overrides)) = (&req.timeframe, &req.history_overrides) {
        if let Some(bars) = overrides.get(tf_str) {
            return Ok(bars.clone());
        }
    }
    let symbol = effective_symbol(&req.symbol);
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req
        .to
        .as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));
    // Default to 24/7 (no filter) when data_source is omitted; the previous
    // "us" default silently dropped weekend / off-hours bars for crypto.
    let market_region = req
        .data_source
        .as_deref()
        .map(market_region_from_data_source)
        .unwrap_or("");
    let market_hours_only = !market_region.is_empty();
    let files = find_parquet_files(
        data_dir,
        symbol,
        req.timeframe.as_deref(),
        req.data_source.as_deref(),
    );
    let mut feed = load_bars(&files, symbol, from_ms, to_ms, market_hours_only, market_region)
        .with_context(|| format!("loading data for '{}'", symbol))?;
    Ok(std::iter::from_fn(|| feed.next()).collect())
}

// ── Warm-up ─────────────────────────────────────────────────────────────────

/// Cap on warm-up bars loaded before `from` (guards against an absurd period).
const MAX_WARMUP_BARS: usize = 5_000;

/// Convergence factor per indicator kind. Exponential / Wilder-smoothed / IIR
/// indicators only converge asymptotically and need ~2.5× their period of history
/// before they're stable; SMA / window indicators are exact after exactly `period`.
fn warmup_factor(kind: &str) -> f64 {
    const EMA_FAMILY: &[&str] = &[
        "ema", "dema", "tema", "smma", "rma", "kama", "hma", "vidya", "frama",
        "trix", "ppo", "tsi", "kst", "pmo", "macd", "rsi", "atr", "supertrend",
        "keltner", "bull_bear", "fisher", "dmi", "adx", "vortex", "kalman",
        "chandelier_exit", "chande_kroll_stop", "ao", "elder_ray", "smi",
    ];
    if EMA_FAMILY.contains(&kind) { 2.5 } else { 1.0 }
}

/// Largest numeric argument in an `ind.X(...)` arg list ≈ the dominant lookback
/// period (covers `macd(12, slow=26, signal=9)` → 26, `supertrend(10, 3.0)` → 10).
fn max_numeric_arg(args: &str) -> f64 {
    args.split(',')
        .filter_map(|a| {
            let a = a.rsplit('=').next().unwrap_or(a).trim();
            a.trim_matches(|c: char| !c.is_ascii_digit() && c != '.').parse::<f64>().ok()
        })
        // ignore multiplier-like fractional args (e.g. supertrend mult 3.0) by taking
        // the max integer-ish lookback; fractions < 1.5 are not periods.
        .filter(|n| *n >= 1.5)
        .fold(0.0, f64::max)
}

/// Warm-up bars to load before `from` so indicators are converged at the trading
/// start. Scans script `ind.TYPE(args)` declarations (period × kind-factor); for
/// named strategies it falls back to the largest numeric param × the EMA factor
/// (most named strategies are EMA-based). Returns 0 when nothing is detected.
pub fn compute_warmup_bars(req: &BacktestRequest) -> usize {
    if req.strategy == "script" {
        let script = req.params.as_ref()
            .and_then(|p| p.get("script")).and_then(|v| v.as_str()).unwrap_or("");
        return script_warmup_bars(script);
    }
    // Named strategy: largest period-like param × EMA factor (conservative).
    let mut max_warm = 0.0_f64;
    if let Some(obj) = req.params.as_ref().and_then(|p| p.as_object()) {
        for v in obj.values() {
            if let Some(n) = v.as_f64() {
                if n >= 1.5 { max_warm = max_warm.max(n * 2.5); }
            }
        }
    }
    (max_warm.ceil() as usize).min(MAX_WARMUP_BARS)
}

/// Scan a script's `ind.TYPE(args)` declarations → warm-up bars (max period × factor).
pub(crate) fn script_warmup_bars(script: &str) -> usize {
    let mut max_warm = 0.0_f64;
    for part in script.split("ind.").skip(1) {
        let kind: String = part.chars().take_while(|c| c.is_alphanumeric() || *c == '_').collect();
        let rest = &part[kind.len()..];
        if let (Some(open), Some(rel_close)) = (rest.find('('), rest.find(')')) {
            if rel_close > open {
                let period = max_numeric_arg(&rest[open + 1..rel_close]);
                max_warm = max_warm.max(period * warmup_factor(&kind));
            }
        }
    }
    (max_warm.ceil() as usize).min(MAX_WARMUP_BARS)
}

/// Per-timeframe warm-up bars for an MTF (v2) script. An indicator declared as
/// `ind.X(period, "H4")` warms on the **H4** feed (period × factor H4 bars); one
/// with no TF arg warms on `base_tf`. Returns warm-up bars *in each TF's own bars*.
pub(crate) fn script_warmup_per_tf(
    script: &str,
    base_tf: Timeframe,
) -> std::collections::HashMap<Timeframe, usize> {
    let mut max_warm: std::collections::HashMap<Timeframe, f64> = std::collections::HashMap::new();
    for part in script.split("ind.").skip(1) {
        let kind: String = part.chars().take_while(|c| c.is_alphanumeric() || *c == '_').collect();
        let rest = &part[kind.len()..];
        if let (Some(open), Some(rel_close)) = (rest.find('('), rest.find(')')) {
            if rel_close > open {
                let args = &rest[open + 1..rel_close];
                let period = max_numeric_arg(args);
                // A quoted arg that parses as a Timeframe selects the feed; else base.
                let tf = args.split(',').find_map(|a| {
                    a.trim().trim_matches(|c| c == '"' || c == '\'').parse::<Timeframe>().ok()
                }).unwrap_or(base_tf);
                let warm = period * warmup_factor(&kind);
                let e = max_warm.entry(tf).or_insert(0.0);
                if warm > *e { *e = warm; }
            }
        }
    }
    max_warm.into_iter()
        .map(|(tf, w)| (tf, (w.ceil() as usize).min(MAX_WARMUP_BARS)))
        .collect()
}

#[cfg(test)]
mod warmup_tests {
    use super::*;

    #[test]
    fn script_warmup_scales_ema_family_by_2_5() {
        // EMA(200) → 500; the larger of two EMAs wins.
        assert_eq!(script_warmup_bars("let e = ind.ema(200); let f = ind.ema(20);"), 500);
        // SMA is exact (×1).
        assert_eq!(script_warmup_bars("let s = ind.sma(20);"), 20);
        // MACD: largest arg (slow=26) × 2.5 = 65.
        assert_eq!(script_warmup_bars("let m = ind.macd(12, 26, 9);"), 65);
        // Supertrend: period 10 (mult 3.0 ignored as non-period) × 2.5 = 25.
        assert_eq!(script_warmup_bars("let st = ind.supertrend(10, 3.0);"), 25);
        // No indicators → 0.
        assert_eq!(script_warmup_bars("let x = close[0];"), 0);
    }
}

/// Like [`load_bars_for_request`] but loads `warmup_bars` extra bars before `from`
/// so indicators can warm. Returns `(bars, trading_from_ms)` — `trading_from_ms` is
/// the original `from` (the engine trades from there; earlier bars only warm).
pub fn load_bars_warmed(
    req: &BacktestRequest,
    data_dir: &Path,
    warmup_bars: usize,
) -> Result<(Vec<Bar>, Option<i64>)> {
    if let (Some(ref tf_str), Some(ref overrides)) = (&req.timeframe, &req.history_overrides) {
        if let Some(bars) = overrides.get(tf_str) {
            let trading_from_ms = req.from.as_deref().and_then(parse_date_ms);
            return Ok((bars.clone(), trading_from_ms));
        }
    }
    let symbol = effective_symbol(&req.symbol);
    let trading_from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req.to.as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));

    // Shift the load window back by warmup_bars × timeframe.
    let tf_ms = req.timeframe.as_deref().unwrap_or("M1")
        .parse::<Timeframe>().map(|tf| tf.duration_ms()).unwrap_or(60_000);
    let load_from_ms = trading_from_ms.map(|f| f - warmup_bars as i64 * tf_ms);

    let market_region = req.data_source.as_deref()
        .map(market_region_from_data_source).unwrap_or("");
    let market_hours_only = !market_region.is_empty();
    let files = find_parquet_files(data_dir, symbol, req.timeframe.as_deref(), req.data_source.as_deref());
    let mut feed = load_bars(&files, symbol, load_from_ms, to_ms, market_hours_only, market_region)
        .with_context(|| format!("loading data for '{}'", symbol))?;
    Ok((std::iter::from_fn(|| feed.next()).collect(), trading_from_ms))
}
