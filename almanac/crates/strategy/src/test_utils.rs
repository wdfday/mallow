//! Shared test utilities for strategy parity tests.
#![cfg(test)]

use alm_core::{bar::Bar, signal::Direction, strategy::Strategy};

pub fn bar(ts: i64, close: f64) -> Bar {
    Bar::new(ts, "TEST", close, close * 1.005, close * 0.995, close, 1000.0)
}

pub fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
    bars.iter()
        .flat_map(|b| s.on_bar(b))
        .map(|s| (s.timestamp, s.direction))
        .collect()
}

pub fn assert_parity(label: &str, a: &[(i64, Direction)], b: &[(i64, Direction)]) {
    assert_eq!(
        a, b,
        "{label}: signal mismatch\n  left : {a:?}\n  right: {b:?}"
    );
}

/// down → up → down to force two crossovers (entry + exit)
pub fn trending_bars(n: usize) -> Vec<Bar> {
    let third = n / 3;
    (0..n)
        .map(|i| {
            let price = if i < third {
                200.0 - i as f64 * 1.5
            } else if i < third * 2 {
                200.0 - third as f64 * 1.5 + (i - third) as f64 * 2.0
            } else {
                200.0 - third as f64 * 1.5 + third as f64 * 2.0
                    - (i - third * 2) as f64 * 2.0
            };
            bar(i as i64 * 60_000, price.max(10.0))
        })
        .collect()
}

/// falling prices → RSI oversold → rising → RSI overbought
pub fn rsi_bars(n: usize) -> Vec<Bar> {
    (0..n)
        .map(|i| {
            let price = if i < n / 2 {
                150.0 - i as f64 * 3.0
            } else {
                150.0 - (n / 2) as f64 * 3.0 + (i - n / 2) as f64 * 4.0
            };
            bar(i as i64 * 60_000, price.max(1.0))
        })
        .collect()
}

/// Oscillate to build RSI variation, then sharp drop (K→0) then sharp rally (K→1).
/// StochasticRsi returns K in [0,1] — thresholds must be 0.2/0.8, not 20/80.
pub fn stoch_rsi_bars() -> Vec<Bar> {
    let osc: Vec<f64> = vec![
        100.0, 112.0, 91.0, 118.0, 88.0, 122.0, 95.0, 115.0, 87.0, 121.0,
        93.0, 116.0, 89.0, 123.0, 91.0, 117.0, 86.0, 120.0, 92.0, 114.0,
    ];
    let mut ts = 0i64;
    let mut bars = vec![];
    // 3 rounds of oscillation = 60 bars (enough warmup for rsi_period=14 + stoch window)
    for rep in 0..3u32 {
        let base = 100.0 + rep as f64 * 5.0;
        for &p in &osc { bars.push(bar(ts, p * base / 100.0)); ts += 60_000; }
    }
    // Sharp drop → RSI near 0 → K near 0 (crosses below 0.2)
    for i in 0..20u32 { bars.push(bar(ts, (125.0 - i as f64 * 6.0).max(1.0))); ts += 60_000; }
    // Strong rally → RSI near 100 → K near 1 (crosses above 0.8)
    for i in 0..25u32 { bars.push(bar(ts, 5.0 + i as f64 * 7.0)); ts += 60_000; }
    bars
}

/// 120 flat bars (warms up ConnorsRSI rank window) → sharp drop → sharp rally.
pub fn connors_rsi_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    for _ in 0..120 { bars.push(bar(ts, 100.0)); ts += 60_000; }
    for i in 0..20u32 { bars.push(bar(ts, (100.0 - i as f64 * 4.0).max(1.0))); ts += 60_000; }
    for i in 0..30u32 { bars.push(bar(ts, 20.0 + i as f64 * 5.0)); ts += 60_000; }
    bars
}

/// Clear down→up→down for ParabolicSAR flips.
pub fn sar_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    for i in 0..50u32 { bars.push(bar(ts, (200.0 - i as f64 * 3.0).max(5.0))); ts += 60_000; }
    for i in 0..80u32 { bars.push(bar(ts, 50.0 + i as f64 * 4.0)); ts += 60_000; }
    for i in 0..50u32 { bars.push(bar(ts, (370.0 - i as f64 * 4.0).max(5.0))); ts += 60_000; }
    bars
}

/// Wide-spread bar: low is 12% below close so bear_power < 0 throughout uptrends.
pub fn wide_bar(ts: i64, close: f64) -> Bar {
    Bar::new(ts, "TEST", close, close * 1.01, close * 0.88, close, 1000.0)
}

/// Rising uptrend +2/bar with wide spread — for ElderRay parity.
/// Step=2 ensures 0.88*Δclose > ΔEMA (bears weakening condition fires).
pub fn elder_ray_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    // Warmup: 30 flat bars at 100
    for _ in 0..30u32 { bars.push(wide_bar(ts, 100.0)); ts += 60_000; }
    // Uptrend +2/bar: entry condition fires (bear_power < 0 but rising)
    for i in 0..100u32 {
        bars.push(wide_bar(ts, 100.0 + i as f64 * 2.0));
        ts += 60_000;
    }
    // Sharp drop: high drops below EMA → bull_power turns negative → exit
    for i in 0..60u32 {
        bars.push(wide_bar(ts, (300.0 - i as f64 * 5.0).max(5.0)));
        ts += 60_000;
    }
    bars
}

/// 100 bars flat (builds slow MAs), then sharp rise and fall.
/// For slow SMA/EMA cross tests: SMA200 built at flat level then sharp rise puts
/// SMA50 above SMA200, then fall triggers exit.
pub fn slow_trend_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    // Phase 1: high flat — SMA200 builds high
    for _ in 0..100 { bars.push(bar(ts, 200.0)); ts += 60_000; }
    // Phase 2: low flat — SMA50 falls below SMA200
    for _ in 0..100 { bars.push(bar(ts, 60.0)); ts += 60_000; }
    // Phase 3: rapid rise — SMA50 crosses above SMA200 (and MACD hist > 0)
    for i in 0..200u32 { bars.push(bar(ts, 60.0 + i as f64 * 2.0)); ts += 60_000; }
    // Phase 4: fall — exit conditions fire
    for i in 0..100u32 { bars.push(bar(ts, (460.0 - i as f64 * 4.0).max(10.0))); ts += 60_000; }
    bars
}

/// Long uptrend then brief dip then recovery — for compound oscillator+MA-filter strategies.
/// After 200 rising bars EMA(50)≈price-25; short dip of 5 bars creates oversold oscillator
/// readings while EMA50 barely moves, so close > EMA50 when oscillator recovers.
pub fn dip_in_uptrend_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    // Long uptrend at 1/bar: price 10→210 over 200 bars
    for i in 0..200u32 { bars.push(bar(ts, 10.0 + i as f64)); ts += 60_000; }
    // Short dip: 8 bars at -15/bar → price 210→90
    for i in 0..8u32 { bars.push(bar(ts, (210.0 - i as f64 * 15.0).max(80.0))); ts += 60_000; }
    // Recovery: 60 bars rising +3/bar
    for i in 0..60u32 { bars.push(bar(ts, 90.0 + i as f64 * 3.0)); ts += 60_000; }
    // Fall for exit: 80 bars
    for i in 0..80u32 { bars.push(bar(ts, (270.0 - i as f64 * 3.0).max(10.0))); ts += 60_000; }
    bars
}

/// Asymmetric bars for CMF test — close near high in uptrend, near low in downtrend.
/// This gives MFM ≈ +0.67 (bull) or -0.50 (bear) instead of ≈ 0 with symmetric bars.
pub fn cmf_bar_bull(ts: i64, close: f64) -> Bar {
    // close near high → MFM = ((close-low)-(high-close))/(high-low) > 0
    let high = close * 1.02;
    let low  = close * 0.97;
    Bar::new(ts, "TEST", close * 0.99, high, low, close * 1.01, 1000.0)
}

pub fn cmf_bar_bear(ts: i64, close: f64) -> Bar {
    let high = close * 1.02;
    let low  = close * 0.97;
    Bar::new(ts, "TEST", close * 1.01, high, low, close * 0.99, 1000.0)
}

pub fn cmf_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    // 60 bear bars (fall from 200→140) — CMF negative
    for i in 0..60u32 {
        bars.push(cmf_bar_bear(ts, 200.0 - i as f64));
        ts += 60_000;
    }
    // 150 bull bars (rise 140→290) — CMF positive, close > EMA(50)
    for i in 0..150u32 {
        bars.push(cmf_bar_bull(ts, 140.0 + i as f64));
        ts += 60_000;
    }
    // 80 bear bars (fall 290→210) — exit
    for i in 0..80u32 {
        bars.push(cmf_bar_bear(ts, 290.0 - i as f64));
        ts += 60_000;
    }
    bars
}

/// Sharp drop below BB lower + RSI < 35, then recovery above BB middle.
pub fn bb_rsi_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    // 60 bars stable at 200 — BB warms up with tiny std; RSI = 50ish
    for _ in 0..30 { bars.push(bar(ts, 200.0)); ts += 60_000; }
    // 30 bars rising slowly 200→230 — RSI > 50, BB upper rises
    for i in 0..30u32 { bars.push(bar(ts, 200.0 + i as f64)); ts += 60_000; }
    // 30 bars sharp crash 230→50 — RSI → 0, close << BB lower
    for i in 0..30u32 { bars.push(bar(ts, (230.0 - i as f64 * 6.0).max(10.0))); ts += 60_000; }
    // 60 bars recovery 50→290 — RSI recovers, close crosses above BB middle
    for i in 0..60u32 { bars.push(bar(ts, 50.0 + i as f64 * 4.0)); ts += 60_000; }
    // 40 bars fall (exit if needed)
    for i in 0..40u32 { bars.push(bar(ts, (290.0 - i as f64 * 6.0).max(10.0))); ts += 60_000; }
    bars
}

/// Bars with actual session gaps for VWAP reset.
/// Each "session" is separated by a large timestamp gap (4 hours).
pub fn vwap_bars() -> Vec<Bar> {
    let mut ts = 0i64;
    let session_gap_ms = 7 * 60 * 60 * 1_000i64; // 7 hours (> 390-min CEL vwap default)
    let mut bars = vec![];
    // Session 1: 60 bars falling (VWAP builds)
    for i in 0..60u32 {
        let p = (200.0 - i as f64 * 1.5).max(10.0);
        bars.push(Bar::new(ts, "TEST", p, p*1.005, p*0.995, p, 1000.0));
        ts += 60_000;
    }
    // New session after gap
    ts += session_gap_ms;
    // Session 2: 60 bars falling then rising (close crosses VWAP with RSI < 50)
    for i in 0..30u32 {
        let p = (120.0 - i as f64 * 2.0).max(30.0);
        bars.push(Bar::new(ts, "TEST", p, p*1.005, p*0.995, p, 1000.0));
        ts += 60_000;
    }
    for i in 0..30u32 {
        let p = 60.0 + i as f64 * 2.0;
        bars.push(Bar::new(ts, "TEST", p, p*1.005, p*0.995, p, 1000.0));
        ts += 60_000;
    }
    // New session after gap
    ts += session_gap_ms;
    // Session 3: 60 bars rising then falling (exit)
    for i in 0..30u32 {
        let p = 80.0 + i as f64 * 2.0;
        bars.push(Bar::new(ts, "TEST", p, p*1.005, p*0.995, p, 1000.0));
        ts += 60_000;
    }
    for i in 0..30u32 {
        let p = (140.0 - i as f64 * 3.0).max(10.0);
        bars.push(Bar::new(ts, "TEST", p, p*1.005, p*0.995, p, 1000.0));
        ts += 60_000;
    }
    bars
}
