use polars::prelude::*;

/// Compute EMA of a Series using polars expressions.
pub fn ema_series(s: &Series, period: usize) -> Series {
    let alpha = 2.0 / (period as f64 + 1.0);
    let values = s.f64().expect("ema_series requires f64");
    let len = values.len();
    let mut out = Vec::with_capacity(len);

    let mut ema_val: Option<f64> = None;
    let mut seed_sum = 0.0;
    let mut count = 0usize;

    for i in 0..len {
        let v = values.get(i).unwrap_or(0.0);
        count += 1;

        if count < period {
            seed_sum += v;
            out.push(f64::NAN);
        } else if count == period {
            seed_sum += v;
            let seed = seed_sum / period as f64;
            ema_val = Some(seed);
            out.push(seed);
        } else {
            let prev = ema_val.unwrap();
            let new_ema = v * alpha + prev * (1.0 - alpha);
            ema_val = Some(new_ema);
            out.push(new_ema);
        }
    }

    Series::new("ema".into(), out)
}

/// Compute RSI of a Series using Wilder's smoothing.
pub fn rsi_series(close: &Series, period: usize) -> Series {
    let values = close.f64().expect("rsi_series requires f64");
    let len = values.len();
    let mut out = Vec::with_capacity(len);

    let mut avg_gain: Option<f64> = None;
    let mut avg_loss: Option<f64> = None;
    let mut gains = Vec::with_capacity(period);
    let mut losses = Vec::with_capacity(period);
    let mut count = 0usize;

    // First value — no change
    out.push(f64::NAN);

    for i in 1..len {
        let prev = values.get(i - 1).unwrap_or(0.0);
        let curr = values.get(i).unwrap_or(0.0);
        let change = curr - prev;
        let gain = change.max(0.0);
        let loss = (-change).max(0.0);

        count += 1;

        if count < period {
            gains.push(gain);
            losses.push(loss);
            out.push(f64::NAN);
        } else if count == period {
            gains.push(gain);
            losses.push(loss);
            let ag = gains.iter().sum::<f64>() / period as f64;
            let al = losses.iter().sum::<f64>() / period as f64;
            avg_gain = Some(ag);
            avg_loss = Some(al);
            out.push(rsi_from(ag, al));
        } else {
            let alpha = 1.0 / period as f64;
            let ag = gain * alpha + avg_gain.unwrap() * (1.0 - alpha);
            let al = loss * alpha + avg_loss.unwrap() * (1.0 - alpha);
            avg_gain = Some(ag);
            avg_loss = Some(al);
            out.push(rsi_from(ag, al));
        }
    }

    Series::new("rsi".into(), out)
}

/// Compute SMA of a Series.
pub fn sma_series(s: &Series, period: usize) -> Series {
    let values = s.f64().expect("sma_series requires f64");
    let len = values.len();
    let mut out = Vec::with_capacity(len);
    let mut window_sum = 0.0f64;

    for i in 0..len {
        let v = values.get(i).unwrap_or(0.0);
        window_sum += v;
        if i >= period {
            window_sum -= values.get(i - period).unwrap_or(0.0);
        }
        if i + 1 < period {
            out.push(f64::NAN);
        } else {
            out.push(window_sum / period as f64);
        }
    }
    Series::new("sma".into(), out)
}

/// Compute MACD line, signal line, and histogram.
/// Returns (macd, signal, histogram) each as a Series.
pub fn macd_series(
    close: &Series,
    fast: usize,
    slow: usize,
    signal: usize,
) -> (Series, Series, Series) {
    let fast_ema = ema_series(close, fast);
    let slow_ema = ema_series(close, slow);

    let fast_v = fast_ema.f64().unwrap();
    let slow_v = slow_ema.f64().unwrap();
    let n = fast_v.len();

    let macd_vals: Vec<f64> = (0..n)
        .map(|i| {
            let f = fast_v.get(i).unwrap_or(f64::NAN);
            let s = slow_v.get(i).unwrap_or(f64::NAN);
            if f.is_nan() || s.is_nan() { f64::NAN } else { f - s }
        })
        .collect();

    let macd_series = Series::new("macd".into(), macd_vals.clone());
    let signal_series = ema_series(&macd_series, signal);
    let sig_v = signal_series.f64().unwrap();

    let hist: Vec<f64> = (0..n)
        .map(|i| {
            let m = macd_vals[i];
            let s = sig_v.get(i).unwrap_or(f64::NAN);
            if m.is_nan() || s.is_nan() { f64::NAN } else { m - s }
        })
        .collect();

    (
        macd_series,
        signal_series,
        Series::new("hist".into(), hist),
    )
}

/// Compute Bollinger Bands.
/// Returns (upper, middle/SMA, lower) as Series.
pub fn bb_series(close: &Series, period: usize, num_std: f64) -> (Series, Series, Series) {
    let values = close.f64().expect("bb_series requires f64");
    let n = values.len();

    let mut uppers = Vec::with_capacity(n);
    let mut middles = Vec::with_capacity(n);
    let mut lowers = Vec::with_capacity(n);

    for i in 0..n {
        if i + 1 < period {
            uppers.push(f64::NAN);
            middles.push(f64::NAN);
            lowers.push(f64::NAN);
            continue;
        }
        let slice: Vec<f64> = (i + 1 - period..=i)
            .map(|j| values.get(j).unwrap_or(0.0))
            .collect();
        let mean = slice.iter().sum::<f64>() / period as f64;
        let variance = slice.iter().map(|&x| (x - mean).powi(2)).sum::<f64>() / period as f64;
        let std_dev = variance.sqrt();
        uppers.push(mean + num_std * std_dev);
        middles.push(mean);
        lowers.push(mean - num_std * std_dev);
    }

    (
        Series::new("bb_upper".into(), uppers),
        Series::new("bb_mid".into(), middles),
        Series::new("bb_lower".into(), lowers),
    )
}

/// Compute ATR (Average True Range) using Wilder's smoothing.
pub fn atr_series(high: &Series, low: &Series, close: &Series, period: usize) -> Series {
    let h = high.f64().expect("atr high");
    let l = low.f64().expect("atr low");
    let c = close.f64().expect("atr close");
    let n = h.len();

    let mut tr_vals = Vec::with_capacity(n);
    for i in 0..n {
        let hi = h.get(i).unwrap_or(0.0);
        let lo = l.get(i).unwrap_or(0.0);
        let tr = if i == 0 {
            hi - lo
        } else {
            let pc = c.get(i - 1).unwrap_or(0.0);
            (hi - lo).max((hi - pc).abs()).max((lo - pc).abs())
        };
        tr_vals.push(tr);
    }

    // Wilder's smoothing
    let mut out = Vec::with_capacity(n);
    let mut atr_val: Option<f64> = None;
    let mut seed_sum = 0.0;

    for (i, &tr) in tr_vals.iter().enumerate() {
        if i < period - 1 {
            seed_sum += tr;
            out.push(f64::NAN);
        } else if i == period - 1 {
            seed_sum += tr;
            let seed = seed_sum / period as f64;
            atr_val = Some(seed);
            out.push(seed);
        } else {
            let prev = atr_val.unwrap();
            let new_atr = (prev * (period as f64 - 1.0) + tr) / period as f64;
            atr_val = Some(new_atr);
            out.push(new_atr);
        }
    }

    Series::new("atr".into(), out)
}

/// Compute Stochastic %K using a rolling window.
pub fn stoch_k_series(high: &Series, low: &Series, close: &Series, period: usize) -> Series {
    let h = high.f64().expect("stoch high");
    let l = low.f64().expect("stoch low");
    let c = close.f64().expect("stoch close");
    let n = h.len();

    let mut out = Vec::with_capacity(n);

    for i in 0..n {
        if i + 1 < period {
            out.push(f64::NAN);
            continue;
        }
        let start = i + 1 - period;
        let hh = (start..=i).map(|j| h.get(j).unwrap_or(0.0)).fold(f64::NEG_INFINITY, f64::max);
        let ll = (start..=i).map(|j| l.get(j).unwrap_or(0.0)).fold(f64::INFINITY, f64::min);
        let cv = c.get(i).unwrap_or(0.0);
        let k = if (hh - ll).abs() < f64::EPSILON {
            50.0
        } else {
            (cv - ll) / (hh - ll) * 100.0
        };
        out.push(k);
    }

    Series::new("stoch_k".into(), out)
}

fn rsi_from(avg_gain: f64, avg_loss: f64) -> f64 {
    if avg_loss < f64::EPSILON {
        return 100.0;
    }
    let rs = avg_gain / avg_loss;
    100.0 - (100.0 / (1.0 + rs))
}

/// Simulate equity curve from position signals and close prices.
///
/// `positions`: 1.0 = long, 0.0 = flat
/// Returns (equity_curve, pnl_per_trade)
pub fn simulate_equity(
    close: &[f64],
    positions: &[f64],
    initial_capital: f64,
    commission_pct: f64,
) -> (Vec<f64>, Vec<f64>) {
    let n = close.len();
    let mut equity = Vec::with_capacity(n);
    let mut pnl_trades = Vec::new();

    let mut cash = initial_capital;
    let mut shares = 0.0;
    let mut entry_price = 0.0;
    let mut in_position = false;

    for i in 0..n {
        let pos = if i < positions.len() {
            positions[i]
        } else {
            0.0
        };
        let price = close[i];

        // Entry
        if pos > 0.0 && !in_position {
            shares = (cash / price).floor();
            let cost = shares * price;
            let comm = cost * commission_pct;
            cash -= cost + comm;
            entry_price = price;
            in_position = true;
        }

        // Exit
        if pos <= 0.0 && in_position {
            let proceeds = shares * price;
            let comm = proceeds * commission_pct;
            cash += proceeds - comm;
            let pnl = (price - entry_price) / entry_price;
            pnl_trades.push(pnl);
            shares = 0.0;
            in_position = false;
        }

        // Mark-to-market
        let mtm = cash + shares * price;
        equity.push(mtm);
    }

    (equity, pnl_trades)
}
