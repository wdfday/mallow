/// So sánh signal bar-by-bar giữa 3 cách implement cùng 1 strategy:
///   1. Hardcoded struct  (RsiMeanRev / MaCrossover)
///   2. DynamicStrategy  (declarative JSON)
///   3. CelStrategy      (expression ema(9) syntax)
///
/// Chạy:
///   cargo run -p alm-engine --bin compare
///   cargo run -p alm-engine --bin compare -- ema

use alm_core::{bar::Bar, signal::Direction, strategy::Strategy};
use alm_strategy::{
    build_strategy,
    named::{ma_crossover::MaCrossover, rsi_mean_rev::RsiMeanRev},
};
use serde_json::json;

// ── Synthetic data ─────────────────────────────────────────────────────────────

fn rsi_bars(n: usize) -> Vec<Bar> {
    (0..n)
        .map(|i| {
            let price = if i < n / 2 {
                150.0 - i as f64 * 3.0
            } else {
                150.0 - (n / 2) as f64 * 3.0 + (i - n / 2) as f64 * 4.0
            };
            let p = price.max(1.0);
            Bar::new(i as i64 * 60_000, "SYN", p, p * 1.005, p * 0.995, p, 1000.0)
        })
        .collect()
}

fn ema_bars(n: usize) -> Vec<Bar> {
    let third = n / 3;
    (0..n)
        .map(|i| {
            let price = if i < third {
                200.0 - i as f64 * 1.5
            } else if i < third * 2 {
                200.0 - third as f64 * 1.5 + (i - third) as f64 * 2.0
            } else {
                200.0 - third as f64 * 1.5 + third as f64 * 2.0 - (i - third * 2) as f64 * 2.0
            };
            let p = price.max(10.0);
            Bar::new(i as i64 * 60_000, "SYN", p, p * 1.005, p * 0.995, p, 1000.0)
        })
        .collect()
}

// ── Signal collection ──────────────────────────────────────────────────────────

struct TaggedSignal {
    bar_idx: usize,
    dir: Direction,
}

fn collect(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<TaggedSignal> {
    bars.iter()
        .enumerate()
        .flat_map(|(i, b)| {
            s.on_bar(b)
                .into_iter()
                .map(move |sig| TaggedSignal { bar_idx: i, dir: sig.direction })
        })
        .collect()
}

// ── Printing ───────────────────────────────────────────────────────────────────

fn dir_char(d: Direction) -> &'static str {
    match d {
        Direction::Long  => "▲ Long",
        Direction::Short => "▼ Short",
        Direction::Close => "✕ Close",
    }
}

fn print_table(title: &str, a: &[TaggedSignal], b: &[TaggedSignal], c: &[TaggedSignal]) {
    println!("\n┌─ {title} ─────────────────────────────────────────────────────────");
    println!("│  {:>6}  {:<12}  {:<12}  {:<12}", "bar", "hardcoded", "dynamic", "cel");
    println!("├───────────────────────────────────────────────────────────────────");

    // Merge all bar indices that produced any signal
    let mut idxs: Vec<usize> = a.iter().chain(b.iter()).chain(c.iter())
        .map(|s| s.bar_idx)
        .collect();
    idxs.sort_unstable();
    idxs.dedup();

    for idx in &idxs {
        let fmt = |sigs: &[TaggedSignal]| -> String {
            sigs.iter()
                .filter(|s| s.bar_idx == *idx)
                .map(|s| dir_char(s.dir))
                .next()
                .unwrap_or("—")
                .to_string()
        };
        println!("│  {:>6}  {:<12}  {:<12}  {:<12}", idx, fmt(a), fmt(b), fmt(c));
    }

    // Parity check
    let a_key: Vec<_> = a.iter().map(|s| (s.bar_idx, s.dir)).collect();
    let b_key: Vec<_> = b.iter().map(|s| (s.bar_idx, s.dir)).collect();
    let c_key: Vec<_> = c.iter().map(|s| (s.bar_idx, s.dir)).collect();
    let parity = if a_key == b_key && b_key == c_key { "✓ identical" } else { "✗ mismatch!" };
    println!("└─ signals: hardcoded={} dynamic={} cel={}  {parity}",
        a.len(), b.len(), c.len());
}

// ── RSI comparison ─────────────────────────────────────────────────────────────

fn run_rsi() {
    let bars = rsi_bars(80);

    let mut hc = RsiMeanRev::new(14, 30.0, 70.0);
    let hc_sigs = collect(&mut hc, &bars);

    let mut dyn_s = build_strategy("dynamic", &json!({
        "indicators": { "rsi": { "type": "rsi", "period": 14 } },
        "entry": { "logic": "and", "rules": [
            { "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }
        ]},
        "exit": { "logic": "and", "rules": [
            { "source": "rsi", "field": "value", "op": "gt", "value": 70.0 }
        ]}
    })).unwrap();
    let dyn_sigs = collect(dyn_s.as_mut(), &bars);

    let mut cel = build_strategy("cel", &json!({
        "entry": "rsi(14) < 30.0",
        "exit":  "rsi(14) > 70.0"
    })).unwrap();
    let cel_sigs = collect(cel.as_mut(), &bars);

    print_table("RSI mean reversion (period=14, oversold=30, overbought=70)",
        &hc_sigs, &dyn_sigs, &cel_sigs);
}

// ── EMA crossover comparison ───────────────────────────────────────────────────

fn run_ema() {
    let bars = ema_bars(300);

    let mut hc = MaCrossover::new(20, 50);
    let hc_sigs = collect(&mut hc, &bars);

    let mut dyn_s = build_strategy("dynamic", &json!({
        "indicators": {
            "fast": { "type": "ema", "period": 20 },
            "slow": { "type": "ema", "period": 50 }
        },
        "entry": { "logic": "and", "rules": [{
            "source": "fast", "field": "value",
            "op": "cross_above",
            "compare": "slow", "compare_field": "value"
        }]},
        "exit": { "logic": "and", "rules": [{
            "source": "fast", "field": "value",
            "op": "cross_below",
            "compare": "slow", "compare_field": "value"
        }]}
    })).unwrap();
    let dyn_sigs = collect(dyn_s.as_mut(), &bars);

    let mut cel = build_strategy("cel", &json!({
        "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)",
        "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
    })).unwrap();
    let cel_sigs = collect(cel.as_mut(), &bars);

    print_table("EMA crossover (fast=20, slow=50)",
        &hc_sigs, &dyn_sigs, &cel_sigs);
}

// ── MACD comparison ────────────────────────────────────────────────────────────

fn run_macd() {
    use alm_strategy::named::macd_crossover::MacdCrossover;

    let bars = ema_bars(300);

    let mut hc = MacdCrossover::new(12, 26, 9);
    let hc_sigs = collect(&mut hc, &bars);

    let mut dyn_s = build_strategy("dynamic", &json!({
        "indicators": {
            "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
        },
        "entry": { "logic": "and", "rules": [
            { "source": "macd", "field": "histogram", "op": "cross_above", "value": 0.0 }
        ]},
        "exit": { "logic": "and", "rules": [
            { "source": "macd", "field": "histogram", "op": "cross_below", "value": 0.0 }
        ]}
    })).unwrap();
    let dyn_sigs = collect(dyn_s.as_mut(), &bars);

    let mut cel = build_strategy("cel", &json!({
        "entry": "prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0",
        "exit":  "prev_macd_hist(12) >= 0.0 && macd_hist(12) < 0.0"
    })).unwrap();
    let cel_sigs = collect(cel.as_mut(), &bars);

    print_table("MACD histogram crossover (fast=12, slow=26, signal=9)",
        &hc_sigs, &dyn_sigs, &cel_sigs);
}

// ── main ───────────────────────────────────────────────────────────────────────

fn main() {
    let mode = std::env::args().nth(1).unwrap_or_else(|| "all".into());
    println!("almanac — strategy signal comparison");
    println!("mode: {mode}  (args: rsi | ema | macd | all)\n");

    match mode.as_str() {
        "rsi"  => run_rsi(),
        "ema"  => run_ema(),
        "macd" => run_macd(),
        _      => { run_rsi(); run_ema(); run_macd(); }
    }
}
