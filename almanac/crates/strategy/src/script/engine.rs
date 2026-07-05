use rhai::{Array, Dynamic, Engine, EvalAltResult};

use crate::script::v1::MEntry;

// ── Shared types / constants ──────────────────────────────────────────────────

pub(crate) const DEFAULT_BUF_DEPTH: usize = 2;
pub(crate) const BAR_FIELDS: &[&str] = &["open", "high", "low", "close", "volume"];

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Extract a float from a `Dynamic`, MEntry-aware. Returns `None` when the
/// value is neither a float nor an `MEntry` (so callers can pick a sensible
/// default — `0.0` for sums, `±∞` for highest/lowest).
///
/// Handles `i64` (Rhai integer literals) via cast so that `avg([1, 2, 3])`
/// produces 2.0 instead of 0.0.
fn dyn_f(d: &Dynamic) -> Option<f64> {
    d.as_float()
        .ok()
        .or_else(|| d.as_int().ok().map(|i| i as f64))
        .or_else(|| d.read_lock::<MEntry>().map(|e| e.primary_value()))
}

/// Extract a float from a Dynamic. For `MEntry` uses the semantic primary field
/// (e.g. `"macd"` for macd, `"middle"` for bbands) so `rising(macd)` tracks
/// the MACD line and `rising(bbands)` tracks the middle band.
fn get_f(v: Option<&Dynamic>) -> f64 {
    v.and_then(dyn_f).unwrap_or(0.0)
}

/// Extract a named field from each element of an indicator history array → `Array<f64>`.
fn extract_field(arr: &Array, name: &str) -> Array {
    arr.iter().map(|d| {
        let v = d.read_lock::<MEntry>()
            .map(|e| e.field(name))
            .or_else(|| {
                d.read_lock::<rhai::Map>()
                    .and_then(|m| m.get(name).and_then(|v| v.as_float().ok()))
            });
        Dynamic::from_float(v.unwrap_or(0.0))
    }).collect()
}

/// Population mean + standard deviation over the first `n` elements of `arr`
/// (newest-first). Returns `(mean, stdev, count)`; `stdev = 0` when `count < 2`.
/// Non-numeric / missing slots are skipped.
fn mean_std(arr: &Array, n: usize) -> (f64, f64, usize) {
    let vals: Vec<f64> = arr.iter().take(n).filter_map(dyn_f).collect();
    let len = vals.len();
    if len == 0 {
        return (0.0, 0.0, 0);
    }
    let mean = vals.iter().sum::<f64>() / len as f64;
    if len < 2 {
        return (mean, 0.0, len);
    }
    let var = vals.iter().map(|v| (v - mean) * (v - mean)).sum::<f64>() / len as f64;
    (mean, var.sqrt(), len)
}

// All field names emitted by multi-output indicators.
const MULTI_FIELDS: &[&str] = &[
    // macd / trix / ppo / kst / pmo
    "histogram", "signal", "macd", "trix", "ppo", "kst", "pmo",
    // adx / dmi
    "adx", "plus_di", "minus_di", "dx",
    // bbands / keltner / donchian
    "upper", "middle", "lower", "bandwidth", "percent_b",
    // stochastic / stoch_rsi / kdj
    "k", "d", "j",
    // supertrend / parabolic_sar / alligator / gmma / fractal
    "value", "bullish", "sar", "bearish",
    // aroon
    "up", "down",
    // vortex
    "plus_vi", "minus_vi",
    // rvi / smi / fisher
    "rvi", "smi", "fisher",
    // rwi
    "rwi_high", "rwi_low",
    // ichimoku
    "tenkan", "kijun", "senkou_a", "senkou_b", "chikou", "above_cloud", "below_cloud", "chikou_above", "chikou_below",
    // alligator
    "jaw", "teeth", "lips",
    // gmma  (.spread is the default; .bullish already listed above)
    "spread",
    "short_0", "short_1", "short_2", "short_3", "short_4", "short_5",
    "long_0", "long_1", "long_2", "long_3", "long_4", "long_5",
    // kalman
    "velocity",
    // bull_bear
    "bull", "bear",
    // elder_ray
    "bull_power", "bear_power",
    // chandelier_exit / chande_kroll
    "long_stop", "short_stop", "stop_long", "stop_short",
    // fractal
    "fractal_high", "fractal_low",
    // chop_zone
    "angle", "zone",
    // shared (ema inside bull_bear_power, atr inside chandelier_exit)
    "ema", "atr",
    // atr raw true range
    "tr",
    // lsma regression slope
    "slope",
];

// ── Script engine with built-in functions ────────────────────────────────────

pub(crate) fn build_engine() -> Engine {
    let mut engine = Engine::new();
    engine.set_max_operations(500_000);
    crate::script::ta::register_ta(&mut engine);

    // ── Exact f64 comparisons (override Rhai's epsilon comparison) ───────────
    engine.register_fn("==", |a: f64, b: f64| a == b);
    engine.register_fn("!=", |a: f64, b: f64| a != b);
    engine.register_fn("<",  |a: f64, b: f64| a < b);
    engine.register_fn("<=", |a: f64, b: f64| a <= b);
    engine.register_fn(">",  |a: f64, b: f64| a > b);
    engine.register_fn(">=", |a: f64, b: f64| a >= b);
    engine.register_fn("eq",  |a: f64, b: f64| a == b);
    engine.register_fn("ne",  |a: f64, b: f64| a != b);
    engine.register_fn("lt",  |a: f64, b: f64| a < b);
    engine.register_fn("lte", |a: f64, b: f64| a <= b);
    engine.register_fn("gt",  |a: f64, b: f64| a > b);
    engine.register_fn("gte", |a: f64, b: f64| a >= b);

    // ── MEntry — multi-output indicator element ──────────────────────────────
    // Allows `supertrend[0] > close` (uses "value" field) AND `supertrend[0].value`.
    engine.register_type_with_name::<MEntry>("IndicatorEntry");

    // Comparison: MEntry op f64  and  f64 op MEntry  (fall back to primary field)
    engine.register_fn(">",  |a: MEntry, b: f64| a.primary_value() > b);
    engine.register_fn(">",  |a: f64, b: MEntry| a > b.primary_value());
    engine.register_fn(">=", |a: MEntry, b: f64| a.primary_value() >= b);
    engine.register_fn(">=", |a: f64, b: MEntry| a >= b.primary_value());
    engine.register_fn("<",  |a: MEntry, b: f64| a.primary_value() < b);
    engine.register_fn("<",  |a: f64, b: MEntry| a < b.primary_value());
    engine.register_fn("<=", |a: MEntry, b: f64| a.primary_value() <= b);
    engine.register_fn("<=", |a: f64, b: MEntry| a <= b.primary_value());
    engine.register_fn("==", |a: MEntry, b: f64| a.primary_value() == b);
    engine.register_fn("==", |a: f64, b: MEntry| a == b.primary_value());
    engine.register_fn("!=", |a: MEntry, b: f64| a.primary_value() != b);
    engine.register_fn("!=", |a: f64, b: MEntry| a != b.primary_value());

    // Comparison: MEntry op i64  and  i64 op MEntry
    // Rhai does NOT coerce i64 → f64 automatically, so `adx[0] > 25` (integer
    // literal) would fail at runtime without these overloads.
    engine.register_fn(">",  |a: MEntry, b: i64| a.primary_value() > b as f64);
    engine.register_fn(">",  |a: i64, b: MEntry| (a as f64) > b.primary_value());
    engine.register_fn(">=", |a: MEntry, b: i64| a.primary_value() >= b as f64);
    engine.register_fn(">=", |a: i64, b: MEntry| (a as f64) >= b.primary_value());
    engine.register_fn("<",  |a: MEntry, b: i64| a.primary_value() < b as f64);
    engine.register_fn("<",  |a: i64, b: MEntry| (a as f64) < b.primary_value());
    engine.register_fn("<=", |a: MEntry, b: i64| a.primary_value() <= b as f64);
    engine.register_fn("<=", |a: i64, b: MEntry| (a as f64) <= b.primary_value());
    engine.register_fn("==", |a: MEntry, b: i64| a.primary_value() == b as f64);
    engine.register_fn("==", |a: i64, b: MEntry| (a as f64) == b.primary_value());
    engine.register_fn("!=", |a: MEntry, b: i64| a.primary_value() != b as f64);
    engine.register_fn("!=", |a: i64, b: MEntry| (a as f64) != b.primary_value());

    // Arithmetic: MEntry op f64  and  f64 op MEntry  → f64
    engine.register_fn("-", |a: MEntry, b: f64| -> f64 { a.primary_value() - b });
    engine.register_fn("-", |a: f64, b: MEntry| -> f64 { a - b.primary_value() });
    engine.register_fn("+", |a: MEntry, b: f64| -> f64 { a.primary_value() + b });
    engine.register_fn("+", |a: f64, b: MEntry| -> f64 { a + b.primary_value() });
    engine.register_fn("*", |a: MEntry, b: f64| -> f64 { a.primary_value() * b });
    engine.register_fn("*", |a: f64, b: MEntry| -> f64 { a * b.primary_value() });
    engine.register_fn("/", |a: MEntry, b: f64| -> f64 {
        let b = b; if b == 0.0 { 0.0 } else { a.primary_value() / b }
    });
    engine.register_fn("/", |a: f64, b: MEntry| -> f64 {
        let b = b.primary_value(); if b == 0.0 { 0.0 } else { a / b }
    });

    // Arithmetic: MEntry op i64  and  i64 op MEntry  → f64
    engine.register_fn("-", |a: MEntry, b: i64| -> f64 { a.primary_value() - b as f64 });
    engine.register_fn("-", |a: i64, b: MEntry| -> f64 { a as f64 - b.primary_value() });
    engine.register_fn("+", |a: MEntry, b: i64| -> f64 { a.primary_value() + b as f64 });
    engine.register_fn("+", |a: i64, b: MEntry| -> f64 { a as f64 + b.primary_value() });
    engine.register_fn("*", |a: MEntry, b: i64| -> f64 { a.primary_value() * b as f64 });
    engine.register_fn("*", |a: i64, b: MEntry| -> f64 { a as f64 * b.primary_value() });
    engine.register_fn("/", |a: MEntry, b: i64| -> f64 {
        let b = b as f64; if b == 0.0 { 0.0 } else { a.primary_value() / b }
    });
    engine.register_fn("/", |a: i64, b: MEntry| -> f64 {
        let b = b.primary_value(); if b == 0.0 { 0.0 } else { a as f64 / b }
    });

    // Comparison / arithmetic: MEntry op MEntry (both use their primary field).
    // Enables `macd[0] > macd[1]`, `atr[0] - atr[1]`, `tenkan[0] - kijun[0]`, etc.
    engine.register_fn(">",  |a: MEntry, b: MEntry| a.primary_value() >  b.primary_value());
    engine.register_fn(">=", |a: MEntry, b: MEntry| a.primary_value() >= b.primary_value());
    engine.register_fn("<",  |a: MEntry, b: MEntry| a.primary_value() <  b.primary_value());
    engine.register_fn("<=", |a: MEntry, b: MEntry| a.primary_value() <= b.primary_value());
    engine.register_fn("==", |a: MEntry, b: MEntry| a.primary_value() == b.primary_value());
    engine.register_fn("!=", |a: MEntry, b: MEntry| a.primary_value() != b.primary_value());
    engine.register_fn("-", |a: MEntry, b: MEntry| -> f64 { a.primary_value() - b.primary_value() });
    engine.register_fn("+", |a: MEntry, b: MEntry| -> f64 { a.primary_value() + b.primary_value() });
    engine.register_fn("*", |a: MEntry, b: MEntry| -> f64 { a.primary_value() * b.primary_value() });
    engine.register_fn("/", |a: MEntry, b: MEntry| -> f64 {
        let b = b.primary_value(); if b == 0.0 { 0.0 } else { a.primary_value() / b }
    });

    // Property getters: `entry[0].field_name` → f64
    // Each field name from MULTI_FIELDS is registered as a property on MEntry.
    for &field in MULTI_FIELDS {
        engine.register_get(field, move |e: &mut MEntry| -> f64 { e.field(field) });
    }

    // ── Crossover / direction ────────────────────────────────────────────────
    engine.register_fn("cross_above", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 <= b1 && a0 > b0
    });
    engine.register_fn("crossover", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 <= b1 && a0 > b0
    });
    engine.register_fn("cross_below", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 >= b1 && a0 < b0
    });
    engine.register_fn("crossunder", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 >= b1 && a0 < b0
    });
    engine.register_fn("rising",   |a: Array| -> bool { get_f(a.get(0)) > get_f(a.get(1)) });
    engine.register_fn("falling",  |a: Array| -> bool { get_f(a.get(0)) < get_f(a.get(1)) });
    // crossed — either-direction cross (cross_above OR cross_below) this bar.
    engine.register_fn("crossed", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        (a1 <= b1 && a0 > b0) || (a1 >= b1 && a0 < b0)
    });
    engine.register_fn("above",    |a: Array, b: Array| -> bool { get_f(a.get(0)) > get_f(b.get(0)) });
    engine.register_fn("below",    |a: Array, b: Array| -> bool { get_f(a.get(0)) < get_f(b.get(0)) });
    engine.register_fn("in_range", |v: f64, lo: f64, hi: f64| -> bool { v >= lo && v <= hi });
    // within — tolerant float equality: |a - b| <= tol. Safer than `==` on f64.
    engine.register_fn("within", |a: f64, b: f64, tol: f64| -> bool { (a - b).abs() <= tol });

    // flag — coerce a bool-semantic indicator field (encoded as 0.0/1.0) into a
    // real `bool`, so `!`, `&&`, `||`, and `if` work naturally. Indicator output
    // is uniformly f64 (see `IndicatorBox::update`); fields like `bullish`,
    // `above_cloud`, … only ever carry 0.0/1.0. Use `flag(st[0].bullish)` instead
    // of `st[0].bullish > 0.5`, and `!flag(st[0].bullish)` instead of `< 0.5`.
    engine.register_fn("flag", |v: f64| -> bool { v > 0.5 });

    // rising_n / falling_n — monotonically rising/falling for last n bars.
    // SAFETY: `n` must be validated as positive *before* the `as usize` cast.
    // A negative `n` would wrap to `usize::MAX`, and the subsequent `n + 1`
    // would overflow back to 0 in release mode, making the length guard
    // `a.len() < 0` always false — then `(0..usize::MAX).all(...)` spins a
    // core to 100 % forever (CPU DoS). Guard: return false for n ≤ 0.
    engine.register_fn("rising_n", |a: Array, n: i64| -> bool {
        if n <= 0 { return false; }
        let n = n as usize;
        if a.len() < n + 1 { return false; }
        (0..n).all(|i| get_f(a.get(i)) > get_f(a.get(i + 1)))
    });
    engine.register_fn("falling_n", |a: Array, n: i64| -> bool {
        if n <= 0 { return false; }
        let n = n as usize;
        if a.len() < n + 1 { return false; }
        (0..n).all(|i| get_f(a.get(i)) < get_f(a.get(i + 1)))
    });

    // slope — (arr[0] - arr[n-1]) / (n-1). Positive = rising.
    engine.register_fn("slope", |a: Array| -> f64 {
        let n = a.len();
        if n < 2 { return 0.0; }
        (get_f(a.get(0)) - get_f(a.get(n - 1))) / (n - 1) as f64
    });
    // Two-arg form: slope(arr, n) computes over the first n elements.
    // Allows extract_max_lookback to size bar_buf correctly.
    engine.register_fn("slope", |a: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(0.0); }
        let n = n as usize;
        if a.len() < n {
            return Err(format!(
                "slope: bar buffer ({} bars) is smaller than requested n={n}. \
                 Use a literal integer for n.",
                a.len()
            ).into());
        }
        if n < 2 { return Ok(0.0); }
        Ok((get_f(a.get(0)) - get_f(a.get(n - 1))) / (n - 1) as f64)
    });

    // momentum — absolute change vs N bars ago: arr[0] - arr[n].
    // n ≤ 0: return 0.0 (no change / invalid lookback).
    engine.register_fn("momentum", |a: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(0.0); }
        let n = n as usize;
        if a.len() <= n {
            return Err(format!(
                "momentum: bar buffer ({} bars) is too small for lookback n={n} \
                 (needs at least {} bars). Use a literal integer for n.",
                a.len(), n + 1
            ).into());
        }
        Ok(get_f(a.get(0)) - get_f(a.get(n)))
    });

    // ── Lookback ─────────────────────────────────────────────────────────────
    // n ≤ 0: empty window → NEG_INFINITY / INFINITY sentinel (consistent with
    // an empty take()).
    //
    // SAFETY — buffer-size guard: if the array is smaller than `n` the bar
    // buffer was not sized correctly (most likely because `n` was supplied as
    // a variable/expression and `extract_max_lookback` could not parse it).
    // Returning a value silently in this case would produce completely wrong
    // technical-analysis results without any visible error. We throw a Rhai
    // runtime error instead so the problem is surfaced immediately.
    //
    // Note: this guard triggers only when arr.len() < n *and* n > arr.len()
    // (i.e. the buffer is genuinely under-sized, not just "few early bars").
    // The warm-up mechanism pre-fills the bar ring to `bar_buf_depth` before
    // the strategy starts producing signals, so at signal time arr.len() ==
    // bar_buf_depth.  When bar_buf_depth < n the linter should have already
    // reported an error; this is the last-resort safety net.
    engine.register_fn("highest", |arr: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(f64::NEG_INFINITY); }
        let n = n as usize;
        if arr.len() < n {
            return Err(format!(
                "highest: bar buffer ({} bars) is smaller than requested lookback n={n}. \
                 Use a literal integer for n so the buffer can be sized automatically, \
                 e.g. `highest(close, {n})`.",
                arr.len()
            ).into());
        }
        Ok(arr.iter().take(n).map(|d| dyn_f(d).unwrap_or(f64::NEG_INFINITY)).fold(f64::NEG_INFINITY, f64::max))
    });
    engine.register_fn("lowest", |arr: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(f64::INFINITY); }
        let n = n as usize;
        if arr.len() < n {
            return Err(format!(
                "lowest: bar buffer ({} bars) is smaller than requested lookback n={n}. \
                 Use a literal integer for n so the buffer can be sized automatically, \
                 e.g. `lowest(close, {n})`.",
                arr.len()
            ).into());
        }
        Ok(arr.iter().take(n).map(|d| dyn_f(d).unwrap_or(f64::INFINITY)).fold(f64::INFINITY, f64::min))
    });

    // ── Array aggregation ────────────────────────────────────────────────────
    engine.register_fn("avg", |arr: Array| -> f64 {
        // Empty array → NaN: the script is gate-guarded by warm-up so this
        // should never occur at signal time; returning NaN makes misconfiguration
        // loud (NaN comparisons always false) instead of silently producing 0.0.
        if arr.is_empty() { return f64::NAN; }
        let s: f64 = arr.iter().map(|d| dyn_f(d).unwrap_or(0.0)).sum();
        s / arr.len() as f64
    });
    engine.register_fn("avg", |arr: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(0.0); }
        let n = n as usize;
        if arr.len() < n {
            return Err(format!(
                "avg: bar buffer ({} bars) is smaller than requested window n={n}. \
                 Use a literal integer for n.",
                arr.len()
            ).into());
        }
        let s: f64 = arr.iter().take(n).map(|d| dyn_f(d).unwrap_or(0.0)).sum();
        Ok(s / n as f64)
    });
    engine.register_fn("sum", |arr: Array| -> f64 {
        arr.iter().map(|d| dyn_f(d).unwrap_or(0.0)).sum()
    });
    engine.register_fn("sum", |arr: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(0.0); }
        let n = n as usize;
        if arr.len() < n {
            return Err(format!(
                "sum: bar buffer ({} bars) is smaller than requested window n={n}. \
                 Use a literal integer for n.",
                arr.len()
            ).into());
        }
        let s: f64 = arr.iter().take(n).map(|d| dyn_f(d).unwrap_or(0.0)).sum();
        Ok(s)
    });

    // ── Volatility / statistics ──────────────────────────────────────────────
    // stdev — population standard deviation over the whole buffer or first `n` bars.
    engine.register_fn("stdev", |arr: Array| -> f64 { mean_std(&arr, arr.len()).1 });
    engine.register_fn("stdev", |arr: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(0.0); }
        let n = n as usize;
        if arr.len() < n {
            return Err(format!(
                "stdev: bar buffer ({} bars) is smaller than requested window n={n}. \
                 Use a literal integer for n.",
                arr.len()
            ).into());
        }
        Ok(mean_std(&arr, n).1)
    });
    // pct_change — percentage change vs `n` bars ago: (a[0] - a[n]) / a[n].
    engine.register_fn("pct_change", |arr: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(0.0); }
        let n = n as usize;
        if arr.len() <= n {
            return Err(format!(
                "pct_change: bar buffer ({} bars) is too small for lookback n={n} \
                 (needs at least {} bars). Use a literal integer for n.",
                arr.len(), n + 1
            ).into());
        }
        let cur  = get_f(arr.get(0));
        let past = get_f(arr.get(n));
        Ok(if past == 0.0 { 0.0 } else { (cur - past) / past })
    });
    // zscore — (a[0] - mean) / stdev over the whole buffer or first `n` bars.
    // Returns 0.0 when stdev is 0 (flat series) to avoid div-by-zero.
    engine.register_fn("zscore", |arr: Array| -> f64 {
        let (mean, sd, _) = mean_std(&arr, arr.len());
        if sd == 0.0 { 0.0 } else { (get_f(arr.get(0)) - mean) / sd }
    });
    engine.register_fn("zscore", |arr: Array, n: i64| -> Result<f64, Box<EvalAltResult>> {
        if n <= 0 { return Ok(0.0); }
        let n = n as usize;
        if arr.len() < n {
            return Err(format!(
                "zscore: bar buffer ({} bars) is smaller than requested window n={n}. \
                 Use a literal integer for n.",
                arr.len()
            ).into());
        }
        let (mean, sd, _) = mean_std(&arr, n);
        Ok(if sd == 0.0 { 0.0 } else { (get_f(arr.get(0)) - mean) / sd })
    });

    // ── Scalar math ──────────────────────────────────────────────────────────
    engine.register_fn("abs",   |v: f64| -> f64 { v.abs() });
    engine.register_fn("sqrt",  |v: f64| -> Result<f64, Box<EvalAltResult>> {
        if v < 0.0 {
            return Err(format!("sqrt: argument must be ≥ 0, got {v}").into());
        }
        Ok(v.sqrt())
    });
    engine.register_fn("pow",   |v: f64, e: f64| -> f64 { v.powf(e) });
    engine.register_fn("round", |v: f64| -> f64 { v.round() });
    engine.register_fn("floor", |v: f64| -> f64 { v.floor() });
    engine.register_fn("ceil",  |v: f64| -> f64 { v.ceil() });
    engine.register_fn("min",   |a: f64, b: f64| -> f64 { a.min(b) });
    engine.register_fn("max",   |a: f64, b: f64| -> f64 { a.max(b) });
    engine.register_fn("clamp", |v: f64, lo: f64, hi: f64| -> f64 { v.clamp(lo, hi) });
    engine.register_fn("sign",  |v: f64| -> f64 {
        if v > 0.0 { 1.0 } else if v < 0.0 { -1.0 } else { 0.0 }
    });

    // i64 overloads — Rhai integer literals are i64; without these,
    // `min(arr[0], 70)` fails at runtime with "Function not found".
    engine.register_fn("abs",   |v: i64| -> i64 { v.abs() });
    engine.register_fn("round", |v: i64| -> i64 { v });
    engine.register_fn("floor", |v: i64| -> i64 { v });
    engine.register_fn("ceil",  |v: i64| -> i64 { v });
    engine.register_fn("sign",  |v: i64| -> i64 {
        if v > 0 { 1 } else if v < 0 { -1 } else { 0 }
    });
    engine.register_fn("min",   |a: i64, b: i64| -> i64 { a.min(b) });
    engine.register_fn("max",   |a: i64, b: i64| -> i64 { a.max(b) });
    engine.register_fn("min",   |a: f64, b: i64| -> f64 { a.min(b as f64) });
    engine.register_fn("min",   |a: i64, b: f64| -> f64 { (a as f64).min(b) });
    engine.register_fn("max",   |a: f64, b: i64| -> f64 { a.max(b as f64) });
    engine.register_fn("max",   |a: i64, b: f64| -> f64 { (a as f64).max(b) });
    engine.register_fn("clamp", |v: i64, lo: i64, hi: i64| -> i64 { v.clamp(lo, hi) });
    engine.register_fn("clamp", |v: f64, lo: i64, hi: i64| -> f64 {
        v.clamp(lo as f64, hi as f64)
    });
    engine.register_fn("pow",   |v: f64, e: i64| -> Result<f64, Box<EvalAltResult>> {
        let exp = i32::try_from(e)
            .map_err(|_| format!("pow: exponent {e} is out of i32 range (must be in -2147483648..=2147483647)"))?;
        Ok(v.powi(exp))
    });
    engine.register_fn("pow",   |v: i64, e: i64| -> Result<f64, Box<EvalAltResult>> {
        let exp = i32::try_from(e)
            .map_err(|_| format!("pow: exponent {e} is out of i32 range (must be in -2147483648..=2147483647)"))?;
        Ok((v as f64).powi(exp))
    });

    // ── Debug ────────────────────────────────────────────────────────────────
    engine.register_fn("log", |msg: String| {
        tracing::debug!(rhai = %msg);
    });

    // ── Plot ─────────────────────────────────────────────────────────────────
    // `plot("name", value)` is a no-op hint: declared indicators are already
    // auto-collected into `take_indicator_series()` without needing plot calls.
    engine.register_fn("plot", |_name: String, _value: f64|   {});
    engine.register_fn("plot", |_name: String, _value: bool|  {});
    engine.register_fn("plot", |_name: String, _value: i64|   {});
    engine.register_fn("plot", |_name: String, _value: MEntry| {});

    // ── Multi-output field extractors ────────────────────────────────────────
    // Register as BOTH a free function AND an Array property getter so that
    // `arr.field()` (method), `arr.field` (property), and `field(arr)` all work.
    // Enables: `rising(macd.histogram)`, `cross_above(adx14.adx, ema50)`,
    //          `rising_n(adx14.adx, 3)` — all without parentheses on the field accessor.
    for &field in MULTI_FIELDS {
        engine.register_fn(field, move |arr: Array| -> Array {
            extract_field(&arr, field)
        });
        // Property getter on Array: `array.field` (without parentheses).
        engine.register_get(field, move |arr: &mut Array| -> Array {
            extract_field(arr, field)
        });
    }

    engine
}

// ── Lookback scanner ─────────────────────────────────────────────────────────

/// Walk `args` (the text after a function's opening `(`) and find the
/// first **top-level** comma — one not nested inside `()` or `[]`.
/// Returns the integer literal that immediately follows it, or 0 if:
/// - no top-level comma exists before the matching `)`, or
/// - the token after the comma is not a plain decimal integer.
///
/// Handles nested calls correctly: `highest(some_fn(x, y), 20)` → 20.
/// Evaluate a simple constant-integer expression consisting of non-negative
/// integer literals combined with `+`, `-`, `*`, `/` operators (no parens,
/// no variables).  Returns `None` when the expression contains identifiers or
/// unsupported syntax.
///
/// Examples: `"20"` → `Some(20)`, `"20 + 5"` → `Some(25)`,
///           `"3 * 10"` → `Some(30)`, `"length"` → `None`.
pub(crate) fn eval_const_int_expr(expr: &str) -> Option<usize> {
    let expr = expr.trim();
    if expr.is_empty() { return None; }

    // Plain non-negative integer literal.
    if expr.chars().all(|c| c.is_ascii_digit()) {
        return expr.parse().ok();
    }

    // Scan right-to-left for + / - at the top level (lowest precedence, handle
    // left-associativity by taking the rightmost split point).
    let bytes = expr.as_bytes();
    let mut depth = 0i32;
    let mut split: Option<(usize, u8)> = None;
    for (i, &b) in bytes.iter().enumerate() {
        match b {
            b'(' => depth += 1,
            b')' => depth -= 1,
            b'+' | b'-' if depth == 0 && i > 0 => { split = Some((i, b)); }
            _ => {}
        }
    }
    if let Some((pos, op)) = split {
        let left  = eval_const_int_expr(&expr[..pos])?;
        let right = eval_const_int_expr(&expr[pos + 1..])?;
        return match op {
            b'+' => Some(left.saturating_add(right)),
            b'-' => left.checked_sub(right),
            _    => None,
        };
    }

    // No + / - → look for * / / at the top level (left-to-right, first hit).
    for (i, &b) in bytes.iter().enumerate() {
        if b == b'*' {
            let left  = eval_const_int_expr(&expr[..i])?;
            let right = eval_const_int_expr(&expr[i + 1..])?;
            return Some(left.saturating_mul(right));
        }
        if b == b'/' && i > 0 {
            let left  = eval_const_int_expr(&expr[..i])?;
            let right = eval_const_int_expr(&expr[i + 1..])?;
            if right == 0 { return None; }
            return Some(left / right);
        }
    }

    None
}

/// Extract the text of the second top-level argument from the argument list
/// that starts right after the opening `(` of a call. Stops at the matching
/// `)` for the outer call. Returns `None` if there is no second argument.
fn second_arg_text(args: &str) -> Option<&str> {
    let mut depth = 0usize;
    let mut comma_byte: Option<usize> = None;
    for (i, ch) in args.char_indices() {
        match ch {
            '(' | '[' => depth += 1,
            ')' | ']' => {
                if depth == 0 {
                    // End of the outer call — stop here.
                    if let Some(cb) = comma_byte {
                        return Some(args[cb + 1..i].trim());
                    }
                    return None;
                }
                depth -= 1;
            }
            ',' if depth == 0 => {
                if comma_byte.is_none() {
                    comma_byte = Some(i);
                    // Don't break — we still need to find the matching `)`.
                }
            }
            _ => {}
        }
    }
    // Comma found but no closing `)` (caller passed only the inner args string).
    comma_byte.map(|cb| args[cb + 1..].trim())
}

fn parse_second_int_arg(args: &str) -> usize {
    second_arg_text(args)
        .and_then(eval_const_int_expr)
        .unwrap_or(0)
}

/// Return `true` when the second argument of the call is a static integer
/// expression (all operands are integer literals, no identifiers).  Used by
/// the linter to detect variable / dynamic `n` arguments.
pub(crate) fn second_arg_is_static_literal(args: &str) -> bool {
    match second_arg_text(args) {
        Some(text) => eval_const_int_expr(text).is_some(),
        None       => false,
    }
}

/// Scan the cleaned script for lookback functions and return the maximum N
/// so `bar_buf` is sized correctly at build time.
pub(crate) fn extract_max_lookback(script: &str) -> usize {
    const SECOND_ARG_FNS: &[(&str, usize)] = &[
        ("highest(",    0),
        ("lowest(",     0),
        ("momentum(",   1),
        ("pct_change(", 1), // reads a[n] → needs n+1 bars
        ("rising_n(",   1),
        ("falling_n(",  1),
        ("stdev(",      0), // windowed form stdev(arr, n) → needs n bars
        ("zscore(",     0), // windowed form zscore(arr, n) → needs n bars
        ("slope(",      0), // two-arg form slope(arr, n) → needs n bars
        ("avg(",        0), // windowed form avg(arr, n) → needs n bars
        ("sum(",        0), // windowed form sum(arr, n) → needs n bars
    ];

    let mut max_n = 0usize;
    for (prefix, extra) in SECOND_ARG_FNS {
        let mut search = script;
        while let Some(idx) = search.find(prefix) {
            let after_open = &search[idx + prefix.len()..];
            let n = parse_second_int_arg(after_open);
            let needed = n + extra;
            if needed > max_n { max_n = needed; }
            // Advance past this occurrence (not just prefix.len() to avoid
            // re-matching the same call on an infinite loop when n == 0).
            search = after_open;
        }
    }
    max_n
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use crate::test_utils::*;
    use super::*;
    use std::collections::HashMap;
    use alm_indicator::IndicatorBox;
    use crate::script::lint::KNOWN_INDICATOR_TYPES;
    use crate::script::v1::{indicator_json_config, map_indicator_type, IndicatorKind};

    /// Parity guard for the user's vision: anything a Rust named-strategy reads
    /// from an indicator must be reachable from script.
    ///
    /// Every output field of every known indicator MUST be registered in
    /// `MULTI_FIELDS` (otherwise `entry[0].field` errors at runtime — and
    /// `on_bar` swallows that error, so the gap is invisible without this test).
    /// Also asserts each Multi indicator's declared primary is a real field.
    #[test]
    fn multi_fields_cover_every_indicator_output_field() {
        let registered: std::collections::HashSet<&str> =
            MULTI_FIELDS.iter().copied().collect();
        let empty = HashMap::new();

        let mut missing_fields = Vec::new();
        let mut bad_primaries  = Vec::new();

        // field_names() is period-independent, but some indicators reject
        // certain periods (e.g. coppock needs short < long; ema needs > 0) and
        // do so via panic, not Err. Try a few candidate periods under a
        // silenced panic hook and use the first that constructs.
        let candidate_periods = [11usize, 14, 20, 26, 34, 52];
        let prev_hook = std::panic::take_hook();
        std::panic::set_hook(Box::new(|_| {}));

        for &ty in KNOWN_INDICATOR_TYPES {
            let bx = candidate_periods.iter().find_map(|&p| {
                std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    IndicatorBox::from_config(&indicator_json_config(ty, p, &empty)).ok()
                })).ok().flatten()
            });
            let bx = match bx {
                Some(b) => b,
                None => { std::panic::set_hook(prev_hook); panic!("no candidate period built `{ty}`"); }
            };
            let fields = bx.field_names();

            for &f in fields {
                if !registered.contains(f) {
                    missing_fields.push(format!("{ty}.{f}"));
                }
            }

            if let (_, IndicatorKind::Multi(primary)) = map_indicator_type(ty) {
                if !fields.contains(&primary.as_str()) {
                    bad_primaries.push(format!("{ty} → primary `{primary}` not in {fields:?}"));
                }
            }
        }

        std::panic::set_hook(prev_hook);

        assert!(missing_fields.is_empty(),
            "indicator output fields missing from MULTI_FIELDS (script can't read them): {missing_fields:?}");
        assert!(bad_primaries.is_empty(),
            "Multi indicators whose primary field is not produced: {bad_primaries:?}");
    }

    /// `flag(x)` coerces a 0.0/1.0 bool-semantic field into a real `bool`, so
    /// `!`, `&&`, `||`, and `if` all work — the ergonomic fix for the
    /// `Function not found: ! (f64)` error on `!st.bullish`.
    #[test]
    fn flag_coerces_f64_to_bool() {
        let engine = build_engine();

        // truthiness: 1.0 → true, 0.0 → false (threshold 0.5).
        assert!(engine.eval::<bool>("flag(1.0)").unwrap());
        assert!(!engine.eval::<bool>("flag(0.0)").unwrap());

        // negation now works (the original pain point).
        assert!(engine.eval::<bool>("!flag(0.0)").unwrap());
        assert!(!engine.eval::<bool>("!flag(1.0)").unwrap());

        // composes with && / || and `if`.
        assert!(engine.eval::<bool>("flag(1.0) && !flag(0.0)").unwrap());
        assert_eq!(
            engine.eval::<i64>("if flag(1.0) { 7 } else { 9 }").unwrap(),
            7
        );

        // cross-up idiom: was-not-bull && now-bull.
        assert!(engine.eval::<bool>("!flag(0.0) && flag(1.0)").unwrap());
    }

    /// Integer arrays must work in all statistical built-ins — `avg([1,2,3])`
    /// should return 2.0, not 0.0.  Before the `dyn_f` fix, `i64` elements were
    /// silently dropped (treated as `None`), giving wrong results.
    #[test]
    fn statistics_builtins_integer_arrays() {
        let engine = build_engine();

        // avg
        let a = engine.eval::<f64>("avg([1, 2, 3])").unwrap();
        assert!((a - 2.0).abs() < 1e-9, "avg int = {a}");

        // sum
        let s = engine.eval::<f64>("sum([1, 2, 3, 4])").unwrap();
        assert!((s - 10.0).abs() < 1e-9, "sum int = {s}");

        // highest / lowest require n argument
        let hi = engine.eval::<f64>("highest([3, 1, 4, 1, 5], 5)").unwrap();
        assert!((hi - 5.0).abs() < 1e-9, "highest int = {hi}");
        let lo = engine.eval::<f64>("lowest([3, 1, 4, 1, 5], 5)").unwrap();
        assert!((lo - 1.0).abs() < 1e-9, "lowest int = {lo}");

        // stdev of [2,4,4,4,5,5,7,9] (population) = 2.0
        let sd = engine.eval::<f64>("stdev([2,4,4,4,5,5,7,9])").unwrap();
        assert!((sd - 2.0).abs() < 1e-9, "stdev int = {sd}");

        // zscore: same series, newest element = 9
        // mean = 4.5, sd = 2.0 → z = (9-4.5)/2.0 = 2.25
        let z = engine.eval::<f64>("zscore([9,1,1,1,1])").unwrap();
        assert!((z - 2.0).abs() < 1e-9, "zscore int = {z}");

        // pct_change: [110, 100] → +10%
        let pc = engine.eval::<f64>("pct_change([110, 100], 1)").unwrap();
        assert!((pc - 0.10).abs() < 1e-9, "pct_change int = {pc}");

        // mixed int/float array must also work
        let m = engine.eval::<f64>("avg([1, 2.0, 3])").unwrap();
        assert!((m - 2.0).abs() < 1e-9, "avg mixed = {m}");
    }

    /// Tier-1 statistics built-ins: stdev / pct_change / zscore.
    #[test]
    fn statistics_builtins() {
        let engine = build_engine();

        // stdev of [2,4,4,4,5,5,7,9] (population) = 2.0.
        let sd = engine.eval::<f64>("stdev([2.0,4.0,4.0,4.0,5.0,5.0,7.0,9.0])").unwrap();
        assert!((sd - 2.0).abs() < 1e-9, "stdev = {sd}");

        // stdev with n limits the window: first 2 of [10,0,...] → mean 5, sd 5.
        let sd_n = engine.eval::<f64>("stdev([10.0, 0.0, 99.0, 99.0], 2)").unwrap();
        assert!((sd_n - 5.0).abs() < 1e-9, "stdev(n=2) = {sd_n}");

        // flat series → stdev 0, zscore 0 (no div-by-zero).
        assert_eq!(engine.eval::<f64>("stdev([3.0,3.0,3.0])").unwrap(), 0.0);
        assert_eq!(engine.eval::<f64>("zscore([3.0,3.0,3.0])").unwrap(), 0.0);

        // pct_change: newest-first, (a[0]-a[n])/a[n]. [110,100] over 1 bar = +10%.
        let pc = engine.eval::<f64>("pct_change([110.0, 100.0], 1)").unwrap();
        assert!((pc - 0.10).abs() < 1e-9, "pct_change = {pc}");
        // div-by-zero guard.
        assert_eq!(engine.eval::<f64>("pct_change([5.0, 0.0], 1)").unwrap(), 0.0);

        // zscore: current = mean + 2σ → z = 2.
        // [9,1,1,1,1] → mean 2.6, sd 3.2; z = (9-2.6)/3.2 = 2.0.
        let z = engine.eval::<f64>("zscore([9.0,1.0,1.0,1.0,1.0])").unwrap();
        assert!((z - 2.0).abs() < 1e-9, "zscore = {z}");
    }

    /// Windowed avg and sum overloads.
    #[test]
    fn statistics_builtins_windowed_avg_sum() {
        let engine = build_engine();

        // avg with n
        let a1 = engine.eval::<f64>("avg([1, 2, 3, 4, 5], 3)").unwrap();
        assert!((a1 - 2.0).abs() < 1e-9, "avg(3) = {a1}");

        let a2 = engine.eval::<f64>("avg([10.0, 20.0, 30.0], 2)").unwrap();
        assert!((a2 - 15.0).abs() < 1e-9, "avg(2) = {a2}");

        // sum with n
        let s1 = engine.eval::<f64>("sum([1, 2, 3, 4, 5], 3)").unwrap();
        assert!((s1 - 6.0).abs() < 1e-9, "sum(3) = {s1}");

        let s2 = engine.eval::<f64>("sum([10.0, 20.0, 30.0], 2)").unwrap();
        assert!((s2 - 30.0).abs() < 1e-9, "sum(2) = {s2}");

        // n <= 0 returns 0.0
        assert_eq!(engine.eval::<f64>("avg([1, 2, 3], 0)").unwrap(), 0.0);
        assert_eq!(engine.eval::<f64>("sum([1, 2, 3], 0)").unwrap(), 0.0);
        assert_eq!(engine.eval::<f64>("avg([1, 2, 3], -5)").unwrap(), 0.0);
        assert_eq!(engine.eval::<f64>("sum([1, 2, 3], -5)").unwrap(), 0.0);

        // size smaller than n returns error
        assert!(engine.eval::<f64>("avg([1, 2], 3)").is_err());
        assert!(engine.eval::<f64>("sum([1, 2], 3)").is_err());
    }

    /// Tier-2 convenience built-ins: sign / crossed / within.
    #[test]
    fn convenience_builtins() {
        let engine = build_engine();

        assert_eq!(engine.eval::<f64>("sign(3.5)").unwrap(), 1.0);
        assert_eq!(engine.eval::<f64>("sign(-3.5)").unwrap(), -1.0);
        assert_eq!(engine.eval::<f64>("sign(0.0)").unwrap(), 0.0);

        // within: tolerant float equality.
        assert!(engine.eval::<bool>("within(1.0, 1.05, 0.1)").unwrap());
        assert!(!engine.eval::<bool>("within(1.0, 1.5, 0.1)").unwrap());

        // crossed: either direction. [1,-1] vs [0,0]: was below(−1<0), now above(1>0) → cross up.
        assert!(engine.eval::<bool>("crossed([1.0, -1.0], [0.0, 0.0])").unwrap());
        // [-1,1] vs [0,0]: was above, now below → cross down (still crossed).
        assert!(engine.eval::<bool>("crossed([-1.0, 1.0], [0.0, 0.0])").unwrap());
        // no cross: both bars above.
        assert!(!engine.eval::<bool>("crossed([2.0, 3.0], [0.0, 0.0])").unwrap());
    }

    /// Integer literals in Rhai are `i64` — Rhai does NOT coerce i64→f64.
    /// Without explicit `(MEntry, i64)` overloads, `adx[0] > 25` fails at
    /// runtime with "Function not found". Verify all comparison + arithmetic
    /// operators work with integer literals on both sides.
    #[test]
    fn mentry_integer_operators() {
        use crate::script::v1::MEntry;
        use std::collections::HashMap;

        let engine = build_engine();

        // Build an MEntry with primary value ≈ 13.5 (e.g. "value" field).
        let mut fields = HashMap::new();
        fields.insert("value".to_string(), 13.5_f64);
        let entry = MEntry::new(fields, "value".to_string());

        let mut scope = rhai::Scope::new();
        scope.push("e", entry);

        // Comparisons: MEntry op i64
        assert!(engine.eval_with_scope::<bool>(&mut scope, "e > 10").unwrap(),  "e > 10");
        assert!(!engine.eval_with_scope::<bool>(&mut scope, "e > 999").unwrap(), "e > 999 false");
        assert!(engine.eval_with_scope::<bool>(&mut scope, "e >= 10").unwrap(),  "e >= 10");
        assert!(engine.eval_with_scope::<bool>(&mut scope, "e < 999").unwrap(),  "e < 999");
        assert!(engine.eval_with_scope::<bool>(&mut scope, "e <= 999").unwrap(), "e <= 999");
        assert!(!engine.eval_with_scope::<bool>(&mut scope, "e == 0").unwrap(),  "e == 0 false");
        assert!(engine.eval_with_scope::<bool>(&mut scope, "e != 0").unwrap(),   "e != 0");

        // Comparisons: i64 op MEntry
        assert!(engine.eval_with_scope::<bool>(&mut scope, "10 < e").unwrap(),  "10 < e");
        assert!(engine.eval_with_scope::<bool>(&mut scope, "999 > e").unwrap(), "999 > e");

        // Arithmetic: MEntry op i64 → f64  (13.5 as reference)
        let diff = engine.eval_with_scope::<f64>(&mut scope, "e - 10").unwrap();
        assert!((diff - 3.5).abs() < 1e-9, "e - 10 = {diff}");
        let sum = engine.eval_with_scope::<f64>(&mut scope, "e + 0").unwrap();
        assert!((sum - 13.5).abs() < 1e-9, "e + 0 = {sum}");
        let prod = engine.eval_with_scope::<f64>(&mut scope, "e * 2").unwrap();
        assert!((prod - 27.0).abs() < 1e-9, "e * 2 = {prod}");
        let quot = engine.eval_with_scope::<f64>(&mut scope, "e / 2").unwrap();
        assert!((quot - 6.75).abs() < 1e-9, "e / 2 = {quot}");
        // division by zero → 0.0
        let zero_div = engine.eval_with_scope::<f64>(&mut scope, "e / 0").unwrap();
        assert_eq!(zero_div, 0.0, "e / 0 guard");

        // Arithmetic: i64 op MEntry → f64
        let sub2 = engine.eval_with_scope::<f64>(&mut scope, "100 - e").unwrap();
        assert!((sub2 - 86.5).abs() < 1e-9, "100 - e = {sub2}");
        let add2 = engine.eval_with_scope::<f64>(&mut scope, "0 + e").unwrap();
        assert!((add2 - 13.5).abs() < 1e-9, "0 + e = {add2}");
    }

    /// Negative `n` in `rising_n` / `falling_n` previously wrapped `n as usize`
    /// to `usize::MAX`, then `(0..usize::MAX).all(...)` would spin a CPU core
    /// forever (CPU DoS). `momentum` / `highest` / `lowest` with negative n had
    /// similar cast bugs (correctness, not DoS). All must return a safe sentinel.
    #[test]
    fn negative_n_is_safe_not_infinite_loop() {
        let engine = build_engine();

        // rising_n / falling_n with n ≤ 0 must return false immediately.
        assert!(!engine.eval::<bool>("rising_n([3.0, 2.0, 1.0], -1)").unwrap(),  "rising_n n=-1");
        assert!(!engine.eval::<bool>("rising_n([3.0, 2.0, 1.0],  0)").unwrap(),  "rising_n n=0");
        assert!(!engine.eval::<bool>("falling_n([1.0, 2.0, 3.0], -1)").unwrap(), "falling_n n=-1");
        assert!(!engine.eval::<bool>("falling_n([1.0, 2.0, 3.0],  0)").unwrap(), "falling_n n=0");

        // Positive n still works correctly.
        assert!(engine.eval::<bool>("rising_n([3.0, 2.0, 1.0], 2)").unwrap(),  "rising_n n=2 should be true");
        assert!(!engine.eval::<bool>("rising_n([1.0, 2.0, 3.0], 2)").unwrap(), "rising_n n=2 falling array");

        // momentum with n ≤ 0 → 0.0.
        let m = engine.eval::<f64>("momentum([5.0, 3.0, 1.0], -1)").unwrap();
        assert_eq!(m, 0.0, "momentum n=-1 must be 0.0");
        let m0 = engine.eval::<f64>("momentum([5.0, 3.0, 1.0], 0)").unwrap();
        assert_eq!(m0, 0.0, "momentum n=0 must be 0.0");

        // highest / lowest with n ≤ 0 → sentinel, not incorrect result.
        let hi = engine.eval::<f64>("highest([1.0, 2.0, 3.0], -1)").unwrap();
        assert_eq!(hi, f64::NEG_INFINITY, "highest n=-1 → NEG_INFINITY");
        let lo = engine.eval::<f64>("lowest([1.0, 2.0, 3.0], -1)").unwrap();
        assert_eq!(lo, f64::INFINITY, "lowest n=-1 → INFINITY");
    }

    /// Windowed statistics functions must extend the bar buffer to fit `n`,
    /// otherwise `stdev(close, 50)` would silently see a too-short slice.
    #[test]
    fn lookback_scanner_covers_windowed_stats() {
        assert_eq!(extract_max_lookback("x = stdev(close, 50);"), 50);
        assert_eq!(extract_max_lookback("z = zscore(close, 30);"), 30);
        // pct_change reads a[n] → needs n+1 bars.
        assert_eq!(extract_max_lookback("p = pct_change(close, 9);"), 10);
        // whole-buffer form (no comma) contributes nothing.
        assert_eq!(extract_max_lookback("s = stdev(close);"), 0);
    }

    /// Nested function calls in the first arg must not trick the scanner into
    /// reading a comma that belongs to the inner call instead of the outer one.
    #[test]
    fn lookback_scanner_nested_args() {
        // Inner comma inside avg(open, close) must not be mistaken for the
        // top-level comma separating arr from n.
        assert_eq!(
            extract_max_lookback("h = highest(avg(open, close), 20);"),
            20,
            "nested fn: inner comma must be skipped"
        );
        assert_eq!(
            extract_max_lookback("l = lowest(some_fn(x, y), 15);"),
            15,
            "nested fn: inner comma must be skipped"
        );
        // Bracket indexing inside the first arg also contains commas in Rhai
        // array literals — skip those too.
        assert_eq!(
            extract_max_lookback("m = momentum(arr, 10);"),
            11, // extra=1 for momentum
            "plain case still works after refactor"
        );
        // Variable as second arg: parse returns 0, contributes nothing.
        assert_eq!(
            extract_max_lookback("h = highest(close, n);"),
            0,
            "variable second arg contributes 0"
        );
        // Multiple calls: scanner should return the max across all.
        assert_eq!(
            extract_max_lookback("a = highest(close, 5); b = highest(avg(open, close), 30);"),
            30,
            "max across two calls"
        );
    }

    /// Constant-expression evaluation for the second arg of lookback functions.
    #[test]
    fn eval_const_int_expr_basic() {
        // Plain literals.
        assert_eq!(eval_const_int_expr("20"),    Some(20));
        assert_eq!(eval_const_int_expr(" 5 "),   Some(5));
        assert_eq!(eval_const_int_expr("0"),      Some(0));

        // Simple arithmetic.
        assert_eq!(eval_const_int_expr("20 + 5"),  Some(25));
        assert_eq!(eval_const_int_expr("30 - 5"),  Some(25));
        assert_eq!(eval_const_int_expr("4 * 5"),   Some(20));
        assert_eq!(eval_const_int_expr("20 / 4"),  Some(5));

        // Chained operators (left-associative via rightmost +/- split).
        assert_eq!(eval_const_int_expr("10 + 5 + 3"), Some(18));
        assert_eq!(eval_const_int_expr("20 - 3 - 2"), Some(15));

        // Variables and identifiers → None.
        assert_eq!(eval_const_int_expr("n"),       None);
        assert_eq!(eval_const_int_expr("length"),  None);
        assert_eq!(eval_const_int_expr("20 + n"),  None);

        // Division by zero → None.
        assert_eq!(eval_const_int_expr("10 / 0"),  None);
    }

    /// Lookback scanner now evaluates simple constant-arithmetic expressions
    /// in the second arg position (e.g. `highest(close, 20 + 5)`).
    #[test]
    fn lookback_scanner_constant_expression_second_arg() {
        assert_eq!(
            extract_max_lookback("h = highest(close, 20 + 5);"),
            25,
            "20 + 5 should evaluate to 25"
        );
        assert_eq!(
            extract_max_lookback("h = highest(close, 3 * 10);"),
            30,
            "3 * 10 should evaluate to 30"
        );
        // Variable still contributes 0 (cannot evaluate statically).
        assert_eq!(
            extract_max_lookback("h = highest(close, length);"),
            0,
            "variable second arg still contributes 0"
        );
    }

    /// `second_arg_is_static_literal` distinguishes static integer expressions
    /// from variables / complex runtime expressions.
    #[test]
    fn second_arg_is_static_literal_detection() {
        // Literal → true.
        assert!(second_arg_is_static_literal("close, 20)"),  "literal 20");
        assert!(second_arg_is_static_literal("arr, 20 + 5)"), "expr 20+5");
        assert!(second_arg_is_static_literal("arr, 3 * 10)"), "expr 3*10");

        // Variable → false.
        assert!(!second_arg_is_static_literal("close, n)"),      "variable n");
        assert!(!second_arg_is_static_literal("close, length)"), "variable length");
        assert!(!second_arg_is_static_literal("arr, n + 5)"),    "mixed expr+var");

        // No second arg → false.
        assert!(!second_arg_is_static_literal("close)"),  "no comma");
    }

    /// Runtime guard: highest / lowest / etc. throw an error when the array is
    /// smaller than n (under-buffered due to variable n or expression n).
    #[test]
    fn runtime_guard_underbuffered_throws_error() {
        let engine = build_engine();

        // A 3-element array with n=10 → under-buffered → Rhai error.
        assert!(
            engine.eval::<f64>("highest([1.0, 2.0, 3.0], 10)").is_err(),
            "highest: under-buffered should error"
        );
        assert!(
            engine.eval::<f64>("lowest([1.0, 2.0, 3.0], 10)").is_err(),
            "lowest: under-buffered should error"
        );
        assert!(
            engine.eval::<f64>("stdev([1.0, 2.0, 3.0], 10)").is_err(),
            "stdev: under-buffered should error"
        );
        assert!(
            engine.eval::<f64>("zscore([1.0, 2.0, 3.0], 10)").is_err(),
            "zscore: under-buffered should error"
        );
        assert!(
            engine.eval::<f64>("pct_change([1.0, 2.0, 3.0], 10)").is_err(),
            "pct_change: under-buffered should error"
        );
        assert!(
            engine.eval::<f64>("momentum([1.0, 2.0, 3.0], 10)").is_err(),
            "momentum: under-buffered should error"
        );

        // A sufficiently sized array must still work correctly.
        let hi = engine.eval::<f64>("highest([5.0, 3.0, 1.0], 3)").unwrap();
        assert!((hi - 5.0).abs() < 1e-9, "highest correct result");
        let lo = engine.eval::<f64>("lowest([5.0, 3.0, 1.0], 3)").unwrap();
        assert!((lo - 1.0).abs() < 1e-9, "lowest correct result");
    }

    /// i64 overloads for scalar math — Rhai integer literals are i64.
    #[test]
    fn scalar_math_i64_overloads() {
        let engine = build_engine();
        assert_eq!(engine.eval::<i64>("abs(-5)").unwrap(),     5);
        assert_eq!(engine.eval::<i64>("min(3, 7)").unwrap(),   3);
        assert_eq!(engine.eval::<i64>("max(3, 7)").unwrap(),   7);
        assert_eq!(engine.eval::<f64>("min(3.0, 7)").unwrap(), 3.0);
        assert_eq!(engine.eval::<f64>("max(3, 7.0)").unwrap(), 7.0);
        assert_eq!(engine.eval::<i64>("clamp(10, 0, 5)").unwrap(), 5);
        assert_eq!(engine.eval::<i64>("sign(-3)").unwrap(), -1);
        assert_eq!(engine.eval::<i64>("sign(0)").unwrap(),   0);
        assert_eq!(engine.eval::<i64>("sign(3)").unwrap(),   1);
        // pow with integer exponent
        let p = engine.eval::<f64>("pow(2.0, 3)").unwrap();
        assert!((p - 8.0).abs() < 1e-9, "pow(2.0, 3) = {p}");
    }

    /// sqrt of negative number throws a Rhai error instead of producing NaN.
    #[test]
    fn sqrt_negative_throws_error() {
        let engine = build_engine();
        assert!(engine.eval::<f64>("sqrt(-1.0)").is_err(), "sqrt(-1) should error");
        let ok = engine.eval::<f64>("sqrt(4.0)").unwrap();
        assert!((ok - 2.0).abs() < 1e-9, "sqrt(4) = {ok}");
    }

    /// Two-arg slope is scanned by extract_max_lookback.
    #[test]
    fn slope_two_arg_is_scanned() {
        assert_eq!(extract_max_lookback("s = slope(close, 20);"), 20);
        assert_eq!(extract_max_lookback("s = slope(close);"),      0);
    }
}
