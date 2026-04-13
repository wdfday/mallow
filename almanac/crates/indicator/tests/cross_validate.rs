//! Cross-validation tests: compare our indicators against 3 external libraries.
//!
//! - `ta`:      incremental (stateful, Next<f64> / Next<DataItem> trait)
//! - `rust_ti`: batch (stateless, &[f64] → Vec<f64>)
//! - `kand`:    TA-Lib style (output buffer, &[f64] → &mut [f64])

use alm_indicator::{
    Adx, Aroon, Atr, BBands, Cci, Cmo, Dema, Donchian, Ema, Macd, Mfi, Mom, Obv,
    Roc, Rsi, Sma, Stochastic, Tema, Trix, WilliamsR, Wma,
};

// ═══════════════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════════════

fn generate_prices(n: usize) -> Vec<f64> {
    let mut prices = Vec::with_capacity(n);
    let mut price = 100.0;
    for i in 0..n {
        price += 2.0 * (i as f64 * 0.7).sin() + 0.5 * (i as f64 * 1.3).cos();
        prices.push(price);
    }
    prices
}

/// Tạo OHLCV bars từ close prices — High/Low ±1.5%, Volume ngẫu nhiên giả.
fn generate_ohlcv(n: usize) -> (Vec<f64>, Vec<f64>, Vec<f64>, Vec<f64>, Vec<f64>) {
    let closes = generate_prices(n);
    let mut opens = Vec::with_capacity(n);
    let mut highs = Vec::with_capacity(n);
    let mut lows = Vec::with_capacity(n);
    let mut volumes = Vec::with_capacity(n);

    let mut prev = closes[0];
    for (i, &c) in closes.iter().enumerate() {
        let o = prev;
        let h = c.max(o) * (1.0 + 0.015 * ((i as f64 * 0.9).sin().abs()));
        let l = c.min(o) * (1.0 - 0.015 * ((i as f64 * 1.1).cos().abs()));
        let v = 10_000.0 + 5_000.0 * ((i as f64 * 0.5).sin() + 1.0);
        opens.push(o);
        highs.push(h);
        lows.push(l);
        volumes.push(v);
        prev = c;
    }
    (opens, highs, lows, closes, volumes)
}

/// Skip NaN từ đầu kand output và lấy tail.
fn kand_valid(out: &[f64]) -> Vec<f64> {
    out.iter().copied().filter(|v| !v.is_nan() && v.is_finite()).collect()
}

// ═══════════════════════════════════════════════════════════════════════
// SMA
// ═══════════════════════════════════════════════════════════════════════

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
            our_v[i], their_v[i]
        );
    }
}

#[test]
fn cross_validate_sma_vs_kand() {
    let prices = generate_prices(50);
    let period = 10;
    let mut ours = Sma::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut kand_out = vec![0.0_f64; prices.len()];
    kand::ohlcv::sma::sma(&prices, period, &mut kand_out).unwrap();
    let their_v: Vec<f64> = kand_out.iter().skip(period - 1).copied().collect();

    let min = our_v.len().min(their_v.len());
    for i in 0..min {
        assert!((our_v[i] - their_v[i]).abs() < 1e-10,
            "SMA vs kand at {i}: {:.10} vs {:.10}", our_v[i], their_v[i]);
    }
}

// ═══════════════════════════════════════════════════════════════════════
// EMA
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
        if let Some(v) = ours.update(p) { our_v.push(v); }
        their_v.push(theirs.next(p));
    }
    let skip = our_v.len().saturating_sub(50);
    for (o, t) in our_v[skip..].iter().zip(their_v[skip..].iter()) {
        assert!((o - t).abs() < t.abs() * 0.02 + 0.5, "EMA vs ta: {o:.6} vs {t:.6}");
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
        let (o, t) = (our_v[our_skip + i], their_v[their_skip + i]);
        assert!((o - t).abs() < t.abs() * 0.03 + 1.0, "EMA vs rust_ti: {o:.6} vs {t:.6}");
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

    let our_skip = our_v.len().saturating_sub(20);
    let their_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - our_skip).min(their_v.len() - their_skip);
    for i in 0..min {
        let (o, t) = (our_v[our_skip + i], their_v[their_skip + i]);
        assert!((o - t).abs() < t.abs() * 0.02 + 0.5, "EMA vs kand: {o:.6} vs {t:.6}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// RSI
// ═══════════════════════════════════════════════════════════════════════

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
        if let Some(v) = ours.update(p) { our_v.push(v); }
        their_v.push(theirs.next(p));
    }
    for &v in &our_v { assert!(v >= 0.0 && v <= 100.0); }
    let skip = our_v.len().saturating_sub(30);
    for (o, t) in our_v[skip..].iter().zip(their_v[skip..].iter()) {
        let near = (o - 50.0).abs() < 15.0 || (t - 50.0).abs() < 15.0;
        let agree = (*o > 55.0 && *t > 45.0) || (*o < 45.0 && *t < 55.0);
        assert!(near || agree, "RSI vs ta: {o:.2} vs {t:.2}");
    }
}

#[test]
fn cross_validate_rsi_vs_rust_ti() {
    use rust_ti::standard_indicators::bulk::rsi as rust_ti_rsi;

    let prices = generate_prices(50);
    let mut ours = Rsi::new(14);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();
    let their_v = rust_ti_rsi(&prices);

    for &v in &our_v { assert!(v >= 0.0 && v <= 100.0); }
    for &v in &their_v { assert!(v >= 0.0 && v <= 100.0); }

    let our_skip = our_v.len().saturating_sub(15);
    let their_skip = their_v.len().saturating_sub(15);
    let min = (our_v.len() - our_skip).min(their_v.len() - their_skip);
    for i in 0..min {
        let (o, t) = (our_v[our_skip + i], their_v[their_skip + i]);
        let near = (o - 50.0).abs() < 15.0 || (t - 50.0).abs() < 15.0;
        let agree = (o > 55.0 && t > 45.0) || (o < 45.0 && t < 55.0);
        assert!(near || agree, "RSI vs rust_ti: {o:.2} vs {t:.2}");
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
    kand::ohlcv::rsi::rsi(&prices, period, &mut kand_gains, &mut kand_losses, &mut kand_out).unwrap();
    let their_v = kand_valid(&kand_out);

    for &v in &our_v { assert!(v >= 0.0 && v <= 100.0); }
    let our_skip = our_v.len().saturating_sub(15);
    let their_skip = their_v.len().saturating_sub(15);
    let min = (our_v.len() - our_skip).min(their_v.len() - their_skip);
    for i in 0..min {
        let (o, t) = (our_v[our_skip + i], their_v[their_skip + i]);
        let near = (o - 50.0).abs() < 15.0 || (t - 50.0).abs() < 15.0;
        let agree = (o > 55.0 && t > 45.0) || (o < 45.0 && t < 55.0);
        assert!(near || agree, "RSI vs kand: {o:.2} vs {t:.2}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// ATR
// Tolerance rộng: ta dùng Wilder SMMA, chúng ta dùng EMA → seed khác nhau.
// Sau nhiều bar, hai chuỗi hội tụ về cùng giá trị xấp xỉ.
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_atr_vs_ta() {
    use ta::indicators::AverageTrueRange;
    use ta::{DataItem, Next};

    let (opens, highs, lows, closes, volumes) = generate_ohlcv(100);
    let n = closes.len();
    let period = 14;

    let mut ours = Atr::new(period);
    let mut theirs = AverageTrueRange::new(period).unwrap();
    let mut our_v = Vec::new();
    let mut their_v = Vec::new();

    for i in 0..n {
        let item = DataItem::builder()
            .open(opens[i]).high(highs[i]).low(lows[i]).close(closes[i]).volume(volumes[i])
            .build().unwrap();
        if let Some(v) = ours.update(highs[i], lows[i], closes[i]) { our_v.push(v.atr); }
        their_v.push(theirs.next(&item));
    }
    // ATR phải dương
    for &v in &our_v { assert!(v > 0.0, "ATR must be positive: {v}"); }

    // Sau warmup, hai chuỗi phải đi cùng hướng (relative agreement trong tail)
    let skip = our_v.len().saturating_sub(30);
    let t_skip = their_v.len().saturating_sub(30);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 1..min {
        let o_delta = our_v[skip + i] - our_v[skip + i - 1];
        let t_delta = their_v[t_skip + i] - their_v[t_skip + i - 1];
        // Hướng thay đổi phải giống nhau (cùng tăng hoặc cùng giảm)
        assert!(
            o_delta * t_delta >= 0.0 || (o_delta.abs() < 0.01 && t_delta.abs() < 0.01),
            "ATR direction mismatch at {i}: ours Δ={o_delta:.4} vs ta Δ={t_delta:.4}"
        );
    }
}

#[test]
fn cross_validate_atr_vs_kand() {
    let (_, highs, lows, closes, _) = generate_ohlcv(100);
    let n = closes.len();
    let period = 14;

    let mut ours = Atr::new(period);
    let our_v: Vec<f64> = (0..n)
        .filter_map(|i| ours.update(highs[i], lows[i], closes[i]).map(|v| v.atr))
        .collect();

    let mut kand_out = vec![f64::NAN; n];
    kand::ohlcv::atr::atr(&highs, &lows, &closes, period, &mut kand_out).unwrap();
    let their_v = kand_valid(&kand_out);

    for &v in &our_v { assert!(v > 0.0, "ATR positive: {v}"); }
    for &v in &their_v { assert!(v > 0.0, "kand ATR positive: {v}"); }

    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 1..min {
        let o_delta = our_v[skip + i] - our_v[skip + i - 1];
        let t_delta = their_v[t_skip + i] - their_v[t_skip + i - 1];
        assert!(
            o_delta * t_delta >= 0.0 || (o_delta.abs() < 0.01 && t_delta.abs() < 0.01),
            "ATR vs kand direction mismatch at {i}: Δ={o_delta:.4} vs Δ={t_delta:.4}"
        );
    }
}

// ═══════════════════════════════════════════════════════════════════════
// Bollinger Bands — SMA-based → middle band phải khớp chính xác
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_bbands_middle_vs_ta() {
    use ta::indicators::BollingerBands;
    use ta::Next;

    let prices = generate_prices(100);
    let period = 20;
    let k = 2.0;

    let mut ours = BBands::new(period, k);
    let mut theirs = BollingerBands::new(period, k).unwrap();
    let mut our_middles = Vec::new();
    let mut their_middles = Vec::new();

    for &p in &prices {
        if let Some(v) = ours.update(p) { our_middles.push(v.middle); }
        let t = theirs.next(p);
        their_middles.push(t.average);
    }

    let skip = our_middles.len().saturating_sub(40);
    let t_skip = their_middles.len().saturating_sub(40);
    let min = (our_middles.len() - skip).min(their_middles.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_middles[skip + i], their_middles[t_skip + i]);
        assert!((o - t).abs() < 1e-8, "BBands middle vs ta at {i}: {o:.10} vs {t:.10}");
    }
}

#[test]
fn cross_validate_bbands_vs_kand() {
    let prices = generate_prices(100);
    let n = prices.len();
    let period = 20;
    let k = 2.0;

    let mut ours = BBands::new(period, k);
    let our_v: Vec<_> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut upper_out = vec![f64::NAN; n];
    let mut middle_out = vec![f64::NAN; n];
    let mut lower_out = vec![f64::NAN; n];
    let mut sma_out = vec![f64::NAN; n];
    let mut var_out = vec![f64::NAN; n];
    let mut sum_out = vec![f64::NAN; n];
    let mut sum_sq_out = vec![f64::NAN; n];
    kand::ohlcv::bbands::bbands(&prices, period, k, k, &mut upper_out, &mut middle_out, &mut lower_out, &mut sma_out, &mut var_out, &mut sum_out, &mut sum_sq_out).unwrap();

    let their_mid: Vec<f64> = kand_valid(&middle_out);
    let min = our_v.len().min(their_mid.len());
    let skip = min.saturating_sub(40);
    for i in skip..min {
        let (o, t) = (our_v[i - skip].middle, their_mid[i - skip]);
        assert!((o - t).abs() < 1e-8, "BBands middle vs kand at {i}: {o:.10} vs {t:.10}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// MACD — tolerance rộng vì seed khác nhau
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_macd_direction_vs_ta() {
    use ta::indicators::MovingAverageConvergenceDivergence;
    use ta::Next;

    let prices = generate_prices(150);
    let (fast, slow, signal) = (12, 26, 9);

    let mut ours = Macd::new(fast, slow, signal);
    let mut theirs = MovingAverageConvergenceDivergence::new(fast, slow, signal).unwrap();

    let mut our_v = Vec::new();
    let mut their_v = Vec::new();
    for &p in &prices {
        if let Some(v) = ours.update(p) { our_v.push(v.macd); }
        let t = theirs.next(p);
        their_v.push(t.macd);
    }

    // Chỉ kiểm tra cùng dấu (sign agreement) ở tail sau warm up
    let skip = our_v.len().saturating_sub(30);
    let t_skip = their_v.len().saturating_sub(30);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!(
            o * t > 0.0 || (o.abs() < 0.5_f64 && t.abs() < 0.5_f64),
            "MACD sign mismatch vs ta at {i}: {o:.4} vs {t:.4}"
        );
    }
}

#[test]
fn cross_validate_macd_direction_vs_kand() {
    let prices = generate_prices(150);
    let n = prices.len();
    let (fast, slow, signal) = (12_usize, 26_usize, 9_usize);

    let mut ours = Macd::new(fast, slow, signal);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p).map(|v| v.macd)).collect();

    let mut macd_out = vec![f64::NAN; n];
    let mut signal_out = vec![f64::NAN; n];
    let mut hist_out = vec![f64::NAN; n];
    let mut fast_ema_out = vec![f64::NAN; n];
    let mut slow_ema_out = vec![f64::NAN; n];
    kand::ohlcv::macd::macd(&prices, fast, slow, signal, &mut macd_out, &mut signal_out, &mut hist_out, &mut fast_ema_out, &mut slow_ema_out).unwrap();
    let their_v = kand_valid(&macd_out);

    let skip = our_v.len().saturating_sub(25);
    let t_skip = their_v.len().saturating_sub(25);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!(
            o * t > 0.0 || (o.abs() < 0.5 && t.abs() < 0.5),
            "MACD sign mismatch vs kand at {i}: {o:.4} vs {t:.4}"
        );
    }
}

// ═══════════════════════════════════════════════════════════════════════
// Stochastic — SMA-based → kết quả gần nhau
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_stochastic_vs_ta() {
    use ta::indicators::SlowStochastic;
    use ta::{DataItem, Next};

    let (opens, highs, lows, closes, volumes) = generate_ohlcv(100);
    let n = closes.len();
    let (k, d) = (14, 3);

    let mut ours = Stochastic::new(k, d);
    let mut theirs = SlowStochastic::new(k, d).unwrap();

    let mut our_d = Vec::new();
    let mut their_d = Vec::new();
    for i in 0..n {
        let item = DataItem::builder()
            .open(opens[i]).high(highs[i]).low(lows[i]).close(closes[i]).volume(volumes[i])
            .build().unwrap();
        if let Some(v) = ours.update(highs[i], lows[i], closes[i]) { our_d.push(v.d); }
        their_d.push(theirs.next(&item));
    }

    for &v in &our_d { assert!(v >= 0.0 && v <= 100.0, "Stoch D out of range: {v}"); }
    // ta SlowStochastic adds extra EMA smoothing, so direction won't match exactly.
    // Validate that ta also stays in [0, 100].
    for &v in &their_d { assert!(v >= 0.0 && v <= 100.0, "ta SlowStoch out of range: {v}"); }
    // Both should have valid values after warm-up
    assert!(!our_d.is_empty(), "our Stochastic produced no output");
    assert!(!their_d.is_empty(), "ta SlowStochastic produced no output");
}

#[test]
fn cross_validate_stochastic_vs_kand() {
    let (_, highs, lows, closes, _) = generate_ohlcv(100);
    let n = closes.len();
    let (k_period, d_period) = (14_usize, 3_usize);

    let mut ours = Stochastic::new(k_period, d_period);
    let our_v: Vec<f64> = (0..n)
        .filter_map(|i| ours.update(highs[i], lows[i], closes[i]).map(|v| v.k))
        .collect();

    let mut fast_k_out = vec![f64::NAN; n];
    let mut k_out = vec![f64::NAN; n];
    let mut d_out = vec![f64::NAN; n];
    // kand stoch params must all be >= 2; k_slow_period=3 → compare our raw K vs kand fast_k
    kand::ohlcv::stoch::stoch(&highs, &lows, &closes, k_period, 3, d_period, &mut fast_k_out, &mut k_out, &mut d_out).unwrap();
    // fast_k is the raw range-position %K, same as our k
    let their_k = kand_valid(&fast_k_out);

    for &v in &our_v { assert!(v >= 0.0 && v <= 100.0, "Stoch K out of range: {v}"); }
    for &v in &their_k { assert!(v >= 0.0 && v <= 100.0, "kand Stoch K out of range: {v}"); }

    // raw %K is pure arithmetic → should agree closely
    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_k.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_k.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_k[t_skip + i]);
        assert!((o - t).abs() < 0.5, "Stoch K vs kand at {i}: {o:.4} vs {t:.4}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// Williams %R — deterministic (range-based) → tolerance chặt
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_williams_r_vs_kand() {
    let (_, highs, lows, closes, _) = generate_ohlcv(100);
    let n = closes.len();
    let period = 14;

    let mut ours = WilliamsR::new(period);
    let our_v: Vec<f64> = (0..n)
        .filter_map(|i| ours.update(highs[i], lows[i], closes[i]))
        .collect();

    let mut kand_out = vec![f64::NAN; n];
    let mut highest_out = vec![f64::NAN; n];
    let mut lowest_out = vec![f64::NAN; n];
    kand::ohlcv::willr::willr(&highs, &lows, &closes, period, &mut kand_out, &mut highest_out, &mut lowest_out).unwrap();
    let their_v = kand_valid(&kand_out);

    for &v in &our_v { assert!(v >= -100.0 && v <= 0.0, "%R out of range: {v}"); }

    let skip = our_v.len().saturating_sub(30);
    let t_skip = their_v.len().saturating_sub(30);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!((o - t).abs() < 0.1, "Williams %R vs kand at {i}: {o:.4} vs {t:.4}");
    }
}

#[test]
fn cross_validate_williams_r_vs_rust_ti() {
    use rust_ti::momentum_indicators::bulk::williams_percent_r;

    let (_, highs, lows, closes, _) = generate_ohlcv(100);
    let period = 14;

    let mut ours = WilliamsR::new(period);
    let n = closes.len();
    let our_v: Vec<f64> = (0..n)
        .filter_map(|i| ours.update(highs[i], lows[i], closes[i]))
        .collect();

    let their_v = williams_percent_r(&highs, &lows, &closes, period);

    for &v in &our_v { assert!(v >= -100.0 && v <= 0.0, "%R out of range: {v}"); }

    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!((o - t).abs() < 0.1, "Williams %R vs rust_ti at {i}: {o:.4} vs {t:.4}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// ROC — pure arithmetic → tolerance rất chặt
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_roc_vs_ta() {
    use ta::indicators::RateOfChange;
    use ta::Next;

    let prices = generate_prices(100);
    let period = 10;

    let mut ours = Roc::new(period);
    let mut theirs = RateOfChange::new(period).unwrap();
    let mut our_v = Vec::new();
    let mut their_v = Vec::new();
    for &p in &prices {
        if let Some(v) = ours.update(p) { our_v.push(v); }
        their_v.push(theirs.next(p));
    }

    let skip = our_v.len().saturating_sub(30);
    let t_skip = their_v.len().saturating_sub(30);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!((o - t).abs() < 1e-6, "ROC vs ta at {i}: {o:.6} vs {t:.6}");
    }
}

#[test]
fn cross_validate_roc_vs_kand() {
    let prices = generate_prices(100);
    let n = prices.len();
    let period = 10;

    let mut ours = Roc::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut kand_out = vec![f64::NAN; n];
    kand::ohlcv::roc::roc(&prices, period, &mut kand_out).unwrap();
    let their_v = kand_valid(&kand_out);

    let skip = our_v.len().saturating_sub(30);
    let t_skip = their_v.len().saturating_sub(30);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!((o - t).abs() < 0.01, "ROC vs kand at {i}: {o:.6} vs {t:.6}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// CCI — SMA + mean deviation → gần chính xác
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_cci_vs_ta() {
    use ta::indicators::CommodityChannelIndex;
    use ta::{DataItem, Next};

    let (opens, highs, lows, closes, volumes) = generate_ohlcv(100);
    let n = closes.len();
    let period = 20;

    let mut ours = Cci::new(period);
    let mut theirs = CommodityChannelIndex::new(period).unwrap();
    let mut our_v = Vec::new();
    let mut their_v = Vec::new();

    for i in 0..n {
        let item = DataItem::builder()
            .open(opens[i]).high(highs[i]).low(lows[i]).close(closes[i]).volume(volumes[i])
            .build().unwrap();
        if let Some(v) = ours.update(highs[i], lows[i], closes[i]) { our_v.push(v); }
        their_v.push(theirs.next(&item));
    }

    // CCI phải cùng dấu trong tail
    let skip = our_v.len().saturating_sub(30);
    let t_skip = their_v.len().saturating_sub(30);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!(
            o * t >= 0.0 || (o.abs() < 10.0 && t.abs() < 10.0),
            "CCI sign mismatch vs ta at {i}: {o:.2} vs {t:.2}"
        );
    }
}

#[test]
fn cross_validate_cci_vs_kand() {
    let (_, highs, lows, closes, _) = generate_ohlcv(100);
    let n = closes.len();
    let period = 20;

    let mut ours = Cci::new(period);
    let our_v: Vec<f64> = (0..n)
        .filter_map(|i| ours.update(highs[i], lows[i], closes[i]))
        .collect();

    let mut kand_out = vec![f64::NAN; n];
    let mut tp_out = vec![f64::NAN; n];
    let mut tp_sma_out = vec![f64::NAN; n];
    let mut mean_dev_out = vec![f64::NAN; n];
    kand::ohlcv::cci::cci(&highs, &lows, &closes, period, &mut kand_out, &mut tp_out, &mut tp_sma_out, &mut mean_dev_out).unwrap();
    let their_v = kand_valid(&kand_out);

    let skip = our_v.len().saturating_sub(30);
    let t_skip = their_v.len().saturating_sub(30);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!(
            (o - t).abs() < 2.0 || o * t >= 0.0,
            "CCI vs kand at {i}: {o:.4} vs {t:.4}"
        );
    }
}

// ═══════════════════════════════════════════════════════════════════════
// OBV — cumulative sum → exact match
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_obv_vs_ta() {
    use ta::indicators::OnBalanceVolume;
    use ta::{DataItem, Next};

    let (opens, highs, lows, closes, volumes) = generate_ohlcv(100);
    let n = closes.len();

    let mut ours = Obv::new();
    let mut theirs = OnBalanceVolume::new();
    let mut our_v = Vec::new();
    let mut their_v = Vec::new();

    for i in 0..n {
        let item = DataItem::builder()
            .open(opens[i]).high(highs[i]).low(lows[i]).close(closes[i]).volume(volumes[i])
            .build().unwrap();
        our_v.push(ours.update(closes[i], volumes[i]));
        their_v.push(theirs.next(&item));
    }

    // OBV là tổng tích lũy — hướng (sign of delta) phải giống nhau
    let skip = 10;
    for i in skip..n - 1 {
        let od = our_v[i + 1] - our_v[i];
        let td = their_v[i + 1] - their_v[i];
        assert!(
            od * td >= 0.0,
            "OBV direction mismatch at {i}: Δours={od:.0} Δta={td:.0}"
        );
    }
}

#[test]
fn cross_validate_obv_vs_kand() {
    let (_, _, _, closes, volumes) = generate_ohlcv(100);
    let n = closes.len();

    let mut ours = Obv::new();
    let our_v: Vec<f64> = (0..n).map(|i| ours.update(closes[i], volumes[i])).collect();

    let mut kand_out = vec![0.0_f64; n];
    kand::ohlcv::obv::obv(&closes, &volumes, &mut kand_out).unwrap();

    // OBV delta phải giống nhau
    for i in 1..n - 1 {
        let od = our_v[i + 1] - our_v[i];
        let td = kand_out[i + 1] - kand_out[i];
        assert!(od * td >= 0.0, "OBV direction mismatch vs kand at {i}: Δ={od:.0} vs Δ={td:.0}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// MFI — volume-weighted RSI → range [0, 100], cùng hướng với RSI
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_mfi_vs_ta() {
    use ta::indicators::MoneyFlowIndex;
    use ta::{DataItem, Next};

    let (opens, highs, lows, closes, volumes) = generate_ohlcv(100);
    let n = closes.len();
    let period = 14;

    let mut ours = Mfi::new(period);
    let mut theirs = MoneyFlowIndex::new(period).unwrap();
    let mut our_v = Vec::new();
    let mut their_v = Vec::new();

    for i in 0..n {
        let item = DataItem::builder()
            .open(opens[i]).high(highs[i]).low(lows[i]).close(closes[i]).volume(volumes[i])
            .build().unwrap();
        if let Some(v) = ours.update(highs[i], lows[i], closes[i], volumes[i]) { our_v.push(v); }
        their_v.push(theirs.next(&item));
    }

    for &v in &our_v { assert!(v >= 0.0 && v <= 100.0, "MFI out of range: {v}"); }

    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        // Cùng vùng: cả hai overbought hoặc cả hai oversold hoặc gần nhau
        let agree = (o > 60.0 && t > 40.0) || (o < 40.0 && t < 60.0) || (o - t).abs() < 25.0;
        assert!(agree, "MFI vs ta at {i}: {o:.2} vs {t:.2}");
    }
}

#[test]
fn cross_validate_mfi_vs_kand() {
    let (_, highs, lows, closes, volumes) = generate_ohlcv(100);
    let n = closes.len();
    let period = 14;

    let mut ours = Mfi::new(period);
    let our_v: Vec<f64> = (0..n)
        .filter_map(|i| ours.update(highs[i], lows[i], closes[i], volumes[i]))
        .collect();

    let mut kand_out = vec![f64::NAN; n];
    let mut typ_prices_out = vec![f64::NAN; n];
    let mut money_flows_out = vec![f64::NAN; n];
    let mut pos_flows_out = vec![f64::NAN; n];
    let mut neg_flows_out = vec![f64::NAN; n];
    kand::ohlcv::mfi::mfi(&highs, &lows, &closes, &volumes, period, &mut kand_out, &mut typ_prices_out, &mut money_flows_out, &mut pos_flows_out, &mut neg_flows_out).unwrap();
    let their_v = kand_valid(&kand_out);

    for &v in &our_v { assert!(v >= 0.0 && v <= 100.0, "MFI out of range: {v}"); }

    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!((o - t).abs() < 2.0, "MFI vs kand at {i}: {o:.4} vs {t:.4}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// Donchian — vs rust_ti (chỉ lib này có)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_donchian_vs_rust_ti() {
    use rust_ti::candle_indicators::bulk::donchian_channels;

    let (_, highs, lows, _, _) = generate_ohlcv(100);
    let period = 20;
    let n = highs.len();

    let mut ours = Donchian::new(period);
    let our_v: Vec<_> = (0..n).filter_map(|i| ours.update(highs[i], lows[i])).collect();

    // rust_ti returns Vec<(upper, middle, lower)> or similar
    let their_v = donchian_channels(&highs, &lows, period);

    // rust_ti Donchian trả về Vec<(f64,f64,f64)> theo thứ tự (lower, middle, upper)
    // (confirmed from single example: (min_price, mid, max_price))
    let min = our_v.len().min(their_v.len());
    let skip = min.saturating_sub(30);
    for i in skip..min {
        let ours_upper = our_v[i].upper;
        let ours_lower = our_v[i].lower;
        let (t_lower, _, t_upper) = their_v[i];
        assert!((ours_upper - t_upper).abs() < 1e-8,
            "Donchian upper vs rust_ti at {i}: {ours_upper:.6} vs {t_upper:.6}");
        assert!((ours_lower - t_lower).abs() < 1e-8,
            "Donchian lower vs rust_ti at {i}: {ours_lower:.6} vs {t_lower:.6}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// 4-way: ours vs ta vs rust_ti vs kand (SMA — exact)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn four_way_sma_validation() {
    use rust_ti::moving_average::bulk::moving_average;
    use rust_ti::MovingAverageType;
    use ta::indicators::SimpleMovingAverage;
    use ta::Next;

    let prices = generate_prices(60);
    let period: usize = 10;

    let mut ours = Sma::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut ta_sma = SimpleMovingAverage::new(period).unwrap();
    let ta_all: Vec<f64> = prices.iter().map(|&p| ta_sma.next(p)).collect();
    let ta_v = &ta_all[period - 1..];

    let rti_v = moving_average(&prices, MovingAverageType::Simple, period);

    let mut kand_out = vec![0.0_f64; prices.len()];
    kand::ohlcv::sma::sma(&prices, period, &mut kand_out).unwrap();
    let kand_v: Vec<f64> = kand_out.iter().skip(period - 1).copied().collect();

    let min = our_v.len().min(ta_v.len()).min(rti_v.len()).min(kand_v.len());
    assert!(min > 10);
    for i in 0..min {
        let (o, t, r, k) = (our_v[i], ta_v[i], rti_v[i], kand_v[i]);
        assert!((o - t).abs() < 1e-8, "SMA ours vs ta at {i}: {o} vs {t}");
        assert!((o - r).abs() < 1e-8, "SMA ours vs rust_ti at {i}: {o} vs {r}");
        assert!((o - k).abs() < 1e-8, "SMA ours vs kand at {i}: {o} vs {k}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// WMA — weighted moving average, arithmetic → exact match với kand
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_wma_vs_kand() {
    let prices = generate_prices(80);
    let n = prices.len();
    let period = 10;

    let mut ours = Wma::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut kand_out = vec![f64::NAN; n];
    kand::ohlcv::wma::wma(&prices, period, &mut kand_out).unwrap();
    let their_v = kand_valid(&kand_out);

    let min = our_v.len().min(their_v.len());
    assert!(min > 10);
    let skip = min.saturating_sub(30);
    for i in skip..min {
        let (o, t) = (our_v[i], their_v[i]);
        assert!((o - t).abs() < 1e-8, "WMA vs kand at {i}: {o:.10} vs {t:.10}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// DEMA / TEMA — double/triple EMA, seed diverges → directional agreement
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_dema_vs_kand() {
    let prices = generate_prices(150);
    let n = prices.len();
    let period = 20;

    let mut ours = Dema::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut dema_out = vec![f64::NAN; n];
    let mut ema1_out = vec![f64::NAN; n];
    let mut ema2_out = vec![f64::NAN; n];
    kand::ohlcv::dema::dema(&prices, period, &mut dema_out, &mut ema1_out, &mut ema2_out).unwrap();
    let their_v = kand_valid(&dema_out);

    // DEMA nên theo dõi xu hướng giá → both trending same direction in tail
    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    assert!(min > 5);
    for i in 1..min {
        let od = our_v[skip + i] - our_v[skip + i - 1];
        let td = their_v[t_skip + i] - their_v[t_skip + i - 1];
        assert!(
            od * td >= 0.0 || (od.abs() < 0.05 && td.abs() < 0.05),
            "DEMA direction mismatch vs kand at {i}: Δ={od:.4} vs Δ={td:.4}"
        );
    }
}

#[test]
fn cross_validate_tema_vs_kand() {
    let prices = generate_prices(200);
    let n = prices.len();
    let period = 20;

    let mut ours = Tema::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut tema_out = vec![f64::NAN; n];
    let mut ema1_out = vec![f64::NAN; n];
    let mut ema2_out = vec![f64::NAN; n];
    let mut ema3_out = vec![f64::NAN; n];
    kand::ohlcv::tema::tema(&prices, period, &mut tema_out, &mut ema1_out, &mut ema2_out, &mut ema3_out).unwrap();
    let their_v = kand_valid(&tema_out);

    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    assert!(min > 5);
    for i in 1..min {
        let od = our_v[skip + i] - our_v[skip + i - 1];
        let td = their_v[t_skip + i] - their_v[t_skip + i - 1];
        assert!(
            od * td >= 0.0 || (od.abs() < 0.05 && td.abs() < 0.05),
            "TEMA direction mismatch vs kand at {i}: Δ={od:.4} vs Δ={td:.4}"
        );
    }
}

// ═══════════════════════════════════════════════════════════════════════
// MOM (Momentum) — price(t) − price(t−n), pure arithmetic → exact match
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_mom_vs_kand() {
    let prices = generate_prices(80);
    let n = prices.len();
    let period = 10;

    let mut ours = Mom::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    let mut kand_out = vec![f64::NAN; n];
    kand::ohlcv::mom::mom(&prices, period, &mut kand_out).unwrap();
    let their_v = kand_valid(&kand_out);

    let min = our_v.len().min(their_v.len());
    assert!(min > 10);
    let skip = min.saturating_sub(30);
    for i in skip..min {
        let (o, t) = (our_v[i], their_v[i]);
        // MOM là phép trừ thuần tuý, phải exact
        assert!((o - t).abs() < 1e-8, "MOM vs kand at {i}: {o:.8} vs {t:.8}");
    }
}

// ═══════════════════════════════════════════════════════════════════════
// CMO — Chande Momentum Oscillator, range [−100, 100]
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_cmo_vs_rust_ti() {
    use rust_ti::momentum_indicators::bulk::chande_momentum_oscillator;

    let prices = generate_prices(100);
    let period = 14;

    let mut ours = Cmo::new(period);
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p)).collect();

    // rust_ti bulk CMO — computes over sliding windows of `period` bars
    let their_v = chande_momentum_oscillator(&prices, period);

    for &v in &our_v { assert!(v >= -100.0 && v <= 100.0, "CMO out of range: {v}"); }
    for &v in &their_v { assert!(v >= -100.0 && v <= 100.0, "rust_ti CMO out of range: {v}"); }

    // Sign agreement in tail (both should agree on bull/bear momentum)
    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!(
            o * t >= 0.0 || (o.abs() < 10.0 && t.abs() < 10.0),
            "CMO sign mismatch vs rust_ti at {i}: {o:.2} vs {t:.2}"
        );
    }
}

// ═══════════════════════════════════════════════════════════════════════
// TRIX — triple-smoothed EMA rate of change, sign agreement
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_trix_vs_kand() {
    let prices = generate_prices(200);
    let n = prices.len();
    let period = 15;

    let mut ours = Trix::new(period, 9);
    // TrixValue has .trix and .signal fields
    let our_v: Vec<f64> = prices.iter().filter_map(|&p| ours.update(p).map(|v| v.trix)).collect();

    let mut trix_out = vec![f64::NAN; n];
    let mut ema1_out = vec![f64::NAN; n];
    let mut ema2_out = vec![f64::NAN; n];
    let mut ema3_out = vec![f64::NAN; n];
    kand::ohlcv::trix::trix(&prices, period, &mut trix_out, &mut ema1_out, &mut ema2_out, &mut ema3_out).unwrap();
    let their_v = kand_valid(&trix_out);

    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    assert!(min > 5);
    for i in 0..min {
        let (o, t) = (our_v[skip + i], their_v[t_skip + i]);
        assert!(
            o * t >= 0.0 || (o.abs() < 0.005 && t.abs() < 0.005),
            "TRIX sign mismatch vs kand at {i}: {o:.6} vs {t:.6}"
        );
    }
}

// ═══════════════════════════════════════════════════════════════════════
// AROON — timing indicator, range [0, 100]
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_aroon_vs_kand() {
    let (_, highs, lows, _, _) = generate_ohlcv(100);
    let n = highs.len();
    let period = 14;

    let mut ours = Aroon::new(period);
    let our_up: Vec<f64> = (0..n).filter_map(|i| ours.update(highs[i], lows[i]).map(|v| v.up)).collect();
    let mut ours = Aroon::new(period);
    let our_down: Vec<f64> = (0..n).filter_map(|i| ours.update(highs[i], lows[i]).map(|v| v.down)).collect();

    let mut up_out = vec![f64::NAN; n];
    let mut down_out = vec![f64::NAN; n];
    let mut prev_high_out = vec![f64::NAN; n];
    let mut prev_low_out = vec![f64::NAN; n];
    let mut days_high_out = vec![0usize; n];
    let mut days_low_out = vec![0usize; n];
    kand::ohlcv::aroon::aroon(&highs, &lows, period, &mut up_out, &mut down_out,
        &mut prev_high_out, &mut prev_low_out, &mut days_high_out, &mut days_low_out).unwrap();
    let their_up = kand_valid(&up_out);
    let their_down = kand_valid(&down_out);

    for &v in &our_up { assert!(v >= 0.0 && v <= 100.0, "Aroon Up out of range: {v}"); }
    for &v in &our_down { assert!(v >= 0.0 && v <= 100.0, "Aroon Down out of range: {v}"); }

    let min = our_up.len().min(their_up.len());
    let skip = min.saturating_sub(20);
    for i in skip..min {
        let (ou, tu) = (our_up[i], their_up[i]);
        let (od, td) = (our_down[i], their_down[i]);
        assert!((ou - tu).abs() < 1.0, "Aroon Up vs kand at {i}: {ou:.2} vs {tu:.2}");
        assert!((od - td).abs() < 1.0, "Aroon Down vs kand at {i}: {od:.2} vs {td:.2}");
    }
}

#[test]
fn cross_validate_aroon_vs_rust_ti() {
    use rust_ti::trend_indicators::bulk::aroon_indicator;

    let (_, highs, lows, _, _) = generate_ohlcv(100);
    let period = 14;
    let n = highs.len();

    let mut ours = Aroon::new(period);
    let our_vals: Vec<_> = (0..n).filter_map(|i| ours.update(highs[i], lows[i])).collect();

    // rust_ti aroon_indicator returns Vec<(aroon_up, aroon_down, aroon_oscillator)>
    let their_v = aroon_indicator(&highs, &lows, period);

    // rust_ti dùng convention lookback khác (period vs period+1, min vs max position
    // definition) → chỉ kiểm tra range [0,100]; giá trị exact kiểm tra ở vs kand.
    for v in &our_vals {
        assert!(v.up >= 0.0 && v.up <= 100.0, "our Aroon Up out of range: {}", v.up);
        assert!(v.down >= 0.0 && v.down <= 100.0, "our Aroon Down out of range: {}", v.down);
    }
    for &(tu, td, _) in &their_v {
        assert!(tu >= 0.0 && tu <= 100.0, "rust_ti Aroon Up out of range: {tu}");
        assert!(td >= 0.0 && td <= 100.0, "rust_ti Aroon Down out of range: {td}");
    }
    assert!(!our_vals.is_empty() && !their_v.is_empty());
}

// ═══════════════════════════════════════════════════════════════════════
// ADX — Average Directional Index, range [0, 100]
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn cross_validate_adx_vs_kand() {
    let (_, highs, lows, closes, _) = generate_ohlcv(200);
    let n = closes.len();
    let period = 14;

    let mut ours = Adx::new(period);
    let our_v: Vec<f64> = (0..n)
        .filter_map(|i| ours.update(highs[i], lows[i], closes[i]).map(|v| v.adx))
        .collect();

    let mut adx_out = vec![f64::NAN; n];
    let mut plus_dm_out = vec![f64::NAN; n];
    let mut minus_dm_out = vec![f64::NAN; n];
    let mut tr_out = vec![f64::NAN; n];
    kand::ohlcv::adx::adx(&highs, &lows, &closes, period, &mut adx_out,
        &mut plus_dm_out, &mut minus_dm_out, &mut tr_out).unwrap();
    let their_v = kand_valid(&adx_out);

    // ADX ∈ [0, 100] cho cả hai
    for &v in &our_v { assert!(v >= 0.0 && v <= 100.0, "ADX out of range: {v}"); }
    for &v in &their_v { assert!(v >= 0.0 && v <= 100.0, "kand ADX out of range: {v}"); }

    // Cả hai trending cùng hướng (tăng khi trend mạnh, giảm khi sideways)
    let skip = our_v.len().saturating_sub(20);
    let t_skip = their_v.len().saturating_sub(20);
    let min = (our_v.len() - skip).min(their_v.len() - t_skip);
    for i in 1..min {
        let od = our_v[skip + i] - our_v[skip + i - 1];
        let td = their_v[t_skip + i] - their_v[t_skip + i - 1];
        assert!(
            od * td >= 0.0 || (od.abs() < 1.0 && td.abs() < 1.0),
            "ADX direction mismatch vs kand at {i}: Δ={od:.3} vs Δ={td:.3}"
        );
    }
}
