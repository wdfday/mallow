use std::sync::{Arc, Mutex};

use rhai::{Array, Dynamic, Engine};

use super::binding::MEntry;

// ── Shared types / constants ──────────────────────────────────────────────────

pub(crate) type PlotBuf = Arc<Mutex<Vec<(String, f64)>>>;

pub(crate) const DEFAULT_BUF_DEPTH: usize = 2;
pub(crate) const BAR_FIELDS: &[&str] = &["open", "high", "low", "close", "volume"];

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Extract a float from a Dynamic. For `MEntry` uses the semantic primary field
/// (e.g. `"macd"` for macd, `"middle"` for bbands) so `rising(macd)` tracks
/// the MACD line and `rising(bbands)` tracks the middle band.
fn get_f(v: Option<&Dynamic>) -> f64 {
    v.and_then(|d| {
        d.as_float().ok().or_else(|| {
            d.read_lock::<MEntry>().map(|e| e.primary_value())
        })
    }).unwrap_or(0.0)
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
    // supertrend / parabolic_sar / alligator / gmma / william_fractal
    "value", "bullish", "sar", "bearish",
    // aroon
    "up", "down", "oscillator",
    // vortex
    "plus_vi", "minus_vi",
    // rvi / smi / fisher
    "rvi", "smi", "fisher",
    // rwi
    "rwi_high", "rwi_low",
    // ichimoku
    "tenkan", "kijun", "senkou_a", "senkou_b", "chikou", "above_cloud",
    // alligator
    "jaw", "teeth", "lips",
    // gmma
    "short_avg", "long_avg",
    // kalman
    "slope",
    // bull_bear_power
    "bull", "bear",
    // chandelier_exit / chande_kroll_stop
    "long_stop", "short_stop", "stop_long", "stop_short",
    // william_fractal
    "fractal_high", "fractal_low",
    // chop_zone
    "angle", "zone",
    // shared (ema inside bull_bear_power, atr inside chandelier_exit)
    "ema", "atr",
];

// ── Script engine with built-in functions ────────────────────────────────────

pub(crate) fn build_engine(plot_buf: PlotBuf) -> Engine {
    let mut engine = Engine::new();

    // ── MEntry — multi-output indicator element ──────────────────────────────
    // Allows `supertrend[0] > close` (uses "value" field) AND `supertrend[0].value`.
    engine.register_type_with_name::<MEntry>("IndicatorEntry");

    // Comparison: MEntry op f64  and  f64 op MEntry  (fall back to "value" field)
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
    engine.register_fn("above",    |a: Array, b: Array| -> bool { get_f(a.get(0)) > get_f(b.get(0)) });
    engine.register_fn("below",    |a: Array, b: Array| -> bool { get_f(a.get(0)) < get_f(b.get(0)) });
    engine.register_fn("in_range", |v: f64, lo: f64, hi: f64| -> bool { v >= lo && v <= hi });

    // rising_n / falling_n — monotonically rising/falling for last n bars.
    engine.register_fn("rising_n", |a: Array, n: i64| -> bool {
        let n = n as usize;
        if a.len() < n + 1 { return false; }
        (0..n).all(|i| get_f(a.get(i)) > get_f(a.get(i + 1)))
    });
    engine.register_fn("falling_n", |a: Array, n: i64| -> bool {
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

    // momentum — absolute change vs N bars ago: arr[0] - arr[n].
    engine.register_fn("momentum", |a: Array, n: i64| -> f64 {
        get_f(a.get(0)) - get_f(a.get(n as usize))
    });

    // ── Lookback ─────────────────────────────────────────────────────────────
    engine.register_fn("highest", |arr: Array, n: i64| -> f64 {
        arr.iter().take(n as usize).map(|d| d.as_float().unwrap_or(f64::NEG_INFINITY)).fold(f64::NEG_INFINITY, f64::max)
    });
    engine.register_fn("lowest", |arr: Array, n: i64| -> f64 {
        arr.iter().take(n as usize).map(|d| d.as_float().unwrap_or(f64::INFINITY)).fold(f64::INFINITY, f64::min)
    });

    // ── Array aggregation ────────────────────────────────────────────────────
    engine.register_fn("avg", |arr: Array| -> f64 {
        if arr.is_empty() { return 0.0; }
        let s: f64 = arr.iter().map(|d| d.as_float().unwrap_or(0.0)).sum();
        s / arr.len() as f64
    });
    engine.register_fn("sum", |arr: Array| -> f64 {
        arr.iter().map(|d| d.as_float().unwrap_or(0.0)).sum()
    });

    // ── Scalar math ──────────────────────────────────────────────────────────
    engine.register_fn("abs",   |v: f64| -> f64 { v.abs() });
    engine.register_fn("sqrt",  |v: f64| -> f64 { v.sqrt() });
    engine.register_fn("pow",   |v: f64, e: f64| -> f64 { v.powf(e) });
    engine.register_fn("round", |v: f64| -> f64 { v.round() });
    engine.register_fn("floor", |v: f64| -> f64 { v.floor() });
    engine.register_fn("ceil",  |v: f64| -> f64 { v.ceil() });
    engine.register_fn("min",   |a: f64, b: f64| -> f64 { a.min(b) });
    engine.register_fn("max",   |a: f64, b: f64| -> f64 { a.max(b) });
    engine.register_fn("clamp", |v: f64, lo: f64, hi: f64| -> f64 { v.clamp(lo, hi) });

    // ── Debug ────────────────────────────────────────────────────────────────
    engine.register_fn("log", |msg: String| {
        tracing::debug!(rhai = %msg);
    });

    // ── Plot ─────────────────────────────────────────────────────────────────
    engine.register_fn("plot", move |name: String, value: f64| {
        if let Ok(mut buf) = plot_buf.lock() {
            buf.push((name, value));
        }
    });

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

/// Scan the cleaned script for lookback functions and return the maximum N
/// so `bar_buf` is sized correctly at build time.
pub(crate) fn extract_max_lookback(script: &str) -> usize {
    const SECOND_ARG_FNS: &[(&str, usize)] = &[
        ("highest(",   0),
        ("lowest(",    0),
        ("momentum(",  1),
        ("rising_n(",  1),
        ("falling_n(", 1),
    ];

    let mut max_n = 0usize;
    for (prefix, extra) in SECOND_ARG_FNS {
        let mut search = script;
        while let Some(idx) = search.find(prefix) {
            search = &search[idx + prefix.len()..];
            if let Some(comma) = search.find(',') {
                let after = search[comma + 1..].trim_start();
                let n: usize = after
                    .chars()
                    .take_while(|c| c.is_ascii_digit())
                    .collect::<String>()
                    .parse()
                    .unwrap_or(0);
                let needed = n + extra;
                if needed > max_n { max_n = needed; }
            }
        }
    }
    max_n
}
