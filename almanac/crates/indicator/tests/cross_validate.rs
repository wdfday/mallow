//! Cross-validation tests: compare our indicators against 3 external libraries.
//!
//! - `ta`:      incremental (stateful, Next<f64> trait)
//! - `rust_ti`: batch (stateless, &[f64] → Vec<f64>)
//! - `kand`:    TA-Lib style (output buffer, &[f64] → &mut [f64])

use alm_indicator::{Ema, Rsi, Sma};

fn generate_prices(n: usize) -> Vec<f64> {
    let mut prices = Vec::with_capacity(n);
    let mut price = 100.0;
    for i in 0..n {
        price += 2.0 * (i as f64 * 0.7).sin() + 0.5 * (i as f64 * 1.3).cos();
        prices.push(price);
    }
    prices
}

// ═══════════════════════════════════════════════════════════════════════
// vs `ta` (incremental)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_ema_vs_ta() {
    use ta::indicators::ExponentialMovingAverage;
    use ta::Next;

    let prices = generate_prices(100);
    let mut ours = Ema::new(10);
    let mut theirs = ExponentialMovingAverage::new(10).unwrap();
    let mut our_v = Vec::new();
    let mut their_v = Vec::new();
    for &p in &prices {
        if let Some(v) = ours.update(p) {
            our_v.push(v);
        }
        their_v.push(theirs.next(p));
    }
    let skip = our_v.len().saturating_sub(50);
    for (o, t) in our_v[skip..].iter().zip(their_v[skip..].iter()) {
        let diff = (o - t).abs();
        assert!(diff < t.abs() * 0.02 + 0.5, "EMA vs ta: {o:.6} vs {t:.6}");
    }
}

#[test]
fn cross_validate_sma_vs_ta() {
    use ta::indicators::SimpleMovingAverage;
    use ta::Next;

    let prices = generate_prices(100);
    let mut ours = Sma::new(10);
    let mut theirs = SimpleMovingAverage::new(10).unwrap();
    for &p in &prices {
        let their_v = theirs.next(p);
        if let Some(our_v) = ours.update(p) {
            assert!(
                (our_v - their_v).abs() < 1e-10,
                "SMA vs ta: {our_v:.10} vs {their_v:.10}"
            );
        }
    }
}

#[test]
fn cross_validate_rsi_vs_ta() {
    use ta::indicators::RelativeStrengthIndex;
    use ta::Next;

    let prices = generate_prices(100);
    let mut ours = Rsi::new(14);
    let mut theirs = RelativeStrengthIndex::new(14).unwrap();
    let mut our_v = Vec::new();
    let mut their_v = Vec::new();
    for &p in &prices {
        if let Some(v) = ours.update(p) {
            our_v.push(v);
        }
        their_v.push(theirs.next(p));
    }
    for v in &our_v {
        assert!(*v >= 0.0 && *v <= 100.0);
    }
    let skip = our_v.len().saturating_sub(30);
    for (o, t) in our_v[skip..].iter().zip(their_v[skip..].iter()) {
        let near = (o - 50.0).abs() < 15.0 || (t - 50.0).abs() < 15.0;
        let agree = (*o > 55.0 && *t > 45.0) || (*o < 45.0 && *t < 55.0);
        assert!(near || agree, "RSI vs ta: {o:.2} vs {t:.2}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// vs `rust_ti` (batch)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_sma_vs_rust_ti() {
    use rust_ti::moving_average::bulk::moving_average;
    use rust_ti::MovingAverageType;

    let prices = generate_prices(50);
    let period: usize = 10;

    let mut ours = Sma::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();
    let their_v = moving_average(&prices, MovingAverageType::Simple, period);

    let min = our_v.len().min(their_v.len());
    assert!(min > 0);
    for i in 0..min {
        assert!(
            (our_v[i] - their_v[i]).abs() < 1e-8,
            "SMA vs rust_ti at {i}: {:.8} vs {:.8}",
            our_v[i],
            their_v[i]
        );
    }
}

#[test]
fn cross_validate_ema_vs_rust_ti() {
    use rust_ti::moving_average::bulk::moving_average;
    use rust_ti::MovingAverageType;

    let prices = generate_prices(50);
    let period: usize = 10;

    let mut ours = Ema::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();
    let their_v = moving_average(&prices, MovingAverageType::Exponential, period);

    let our_skip = our_v.len().saturating_sub(20);
    let their_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - our_skip).min(their_v.len() - their_skip);
    for i in 0..min {
        let o = our_v[our_skip + i];
        let t = their_v[their_skip + i];
        assert!(
            (o - t).abs() < t.abs() * 0.03 + 1.0,
            "EMA vs rust_ti: {o:.6} vs {t:.6}"
        );
    }
}

#[test]
fn cross_validate_rsi_vs_rust_ti() {
    use rust_ti::standard_indicators::bulk::rsi as rust_ti_rsi;

    let prices = generate_prices(50);
    let mut ours = Rsi::new(14);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();
    let their_v = rust_ti_rsi(&prices);

    for &v in &our_v {
        assert!(v >= 0.0 && v <= 100.0);
    }
    for &v in &their_v {
        assert!(v >= 0.0 && v <= 100.0);
    }

    let our_skip = our_v.len().saturating_sub(15);
    let their_skip = their_v.len().saturating_sub(15);
    let min = (our_v.len() - our_skip).min(their_v.len() - their_skip);
    for i in 0..min {
        let o = our_v[our_skip + i];
        let t = their_v[their_skip + i];
        let near = (o - 50.0).abs() < 15.0 || (t - 50.0).abs() < 15.0;
        let agree = (o > 55.0 && t > 45.0) || (o < 45.0 && t < 55.0);
        assert!(near || agree, "RSI vs rust_ti: {o:.2} vs {t:.2}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// vs `kand` (TA-Lib style: output buffer)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_sma_vs_kand() {
    let prices = generate_prices(50);
    let period = 10;

    // Our SMA
    let mut ours = Sma::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    // kand SMA: fills output buffer, first (period-1) values are NaN
    let mut kand_out = vec![0.0_f64; prices.len()];
    kand::ohlcv::sma::sma(&prices, period, &mut kand_out).unwrap();
    let their_v: Vec<f64> = kand_out.iter().skip(period - 1).copied().collect();

    let min = our_v.len().min(their_v.len());
    assert!(min > 0);
    for i in 0..min {
        let diff = (our_v[i] - their_v[i]).abs();
        assert!(
            diff < 1e-10,
            "SMA vs kand at {i}: {:.10} vs {:.10}",
            our_v[i],
            their_v[i]
        );
    }
}

#[test]
fn cross_validate_ema_vs_kand() {
    let prices = generate_prices(50);
    let period = 10;

    let mut ours = Ema::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut kand_out = vec![0.0_f64; prices.len()];
    kand::ohlcv::ema::ema(&prices, period, None, &mut kand_out).unwrap();
    let their_v: Vec<f64> = kand_out.iter().skip(period - 1).copied().collect();

    // Compare tail — seeding may differ
    let our_skip = our_v.len().saturating_sub(20);
    let their_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - our_skip).min(their_v.len() - their_skip);
    for i in 0..min {
        let o = our_v[our_skip + i];
        let t = their_v[their_skip + i];
        let diff = (o - t).abs();
        assert!(diff < t.abs() * 0.02 + 0.5, "EMA vs kand: {o:.6} vs {t:.6}");
    }
}

#[test]
fn cross_validate_rsi_vs_kand() {
    let prices = generate_prices(50);
    let period = 14;

    let mut ours = Rsi::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut kand_gains = vec![0.0_f64; prices.len()];
    let mut kand_losses = vec![0.0_f64; prices.len()];
    let mut kand_out = vec![0.0_f64; prices.len()];
    kand::ohlcv::rsi::rsi(
        &prices,
        period,
        &mut kand_gains,
        &mut kand_losses,
        &mut kand_out,
    )
    .unwrap();
    // kand RSI: first `period` values are NaN
    let their_v: Vec<f64> = kand_out
        .iter()
        .skip(period)
        .copied()
        .filter(|v| !v.is_nan())
        .collect();

    for &v in &our_v {
        assert!(v >= 0.0 && v <= 100.0);
    }
    for &v in &their_v {
        assert!(v >= 0.0 && v <= 100.0, "kand RSI out of range: {v}");
    }

    // Directional agreement on tail
    let our_skip = our_v.len().saturating_sub(15);
    let their_skip = their_v.len().saturating_sub(15);
    let min = (our_v.len() - our_skip).min(their_v.len() - their_skip);
    for i in 0..min {
        let o = our_v[our_skip + i];
        let t = their_v[their_skip + i];
        let near = (o - 50.0).abs() < 15.0 || (t - 50.0).abs() < 15.0;
        let agree = (o > 55.0 && t > 45.0) || (o < 45.0 && t < 55.0);
        assert!(near || agree, "RSI vs kand: {o:.2} vs {t:.2}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// 4-way: ours vs ta vs rust_ti vs kand (SMA is deterministic)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn four_way_sma_validation() {
    use rust_ti::moving_average::bulk::moving_average;
    use rust_ti::MovingAverageType;
    use ta::indicators::SimpleMovingAverage;
    use ta::Next;

    let prices = generate_prices(60);
    let period: usize = 10;

    // Ours
    let mut ours = Sma::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    // ta
    let mut ta_sma = SimpleMovingAverage::new(period).unwrap();
    let ta_all: Vec<f64> = prices.iter().map(|&p| ta_sma.next(p)).collect();
    let ta_v = &ta_all[period - 1..];

    // rust_ti
    let rti_v = moving_average(&prices, MovingAverageType::Simple, period);

    // kand
    let mut kand_out = vec![0.0_f64; prices.len()];
    kand::ohlcv::sma::sma(&prices, period, &mut kand_out).unwrap();
    let kand_v: Vec<f64> = kand_out.iter().skip(period - 1).copied().collect();

    let min = our_v
        .len()
        .min(ta_v.len())
        .min(rti_v.len())
        .min(kand_v.len());
    assert!(min > 10, "not enough overlapping values");
    for i in 0..min {
        let (o, t, r, k) = (our_v[i], ta_v[i], rti_v[i], kand_v[i]);
        assert!((o - t).abs() < 1e-8, "ours vs ta at {i}: {o} vs {t}");
        assert!((o - r).abs() < 1e-8, "ours vs rust_ti at {i}: {o} vs {r}");
        assert!((o - k).abs() < 1e-8, "ours vs kand at {i}: {o} vs {k}");
    }
}
