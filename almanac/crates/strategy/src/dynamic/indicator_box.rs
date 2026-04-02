use std::collections::HashMap;

use anyhow::{bail, Result};
use alm_core::Bar;
use alm_indicator::{
    Adx, Atr, AwesomeOscillator, BBands, Cci, Chop, Cmf, ConnorsRsi, Ema, Hma, Ichimoku, Kama,
    Kdj, Keltner, Macd, Mfi, Obv, ParabolicSar, Roc, Rsi, Rwi, Sma, StochasticRsi, Stochastic,
    SuperTrend, Tema, Trix, Tsi, VolatilityRatio, WilliamsR, Wma,
};
use serde_json::Value;

/// A boxed indicator that can be built from JSON config and produces
/// a flat map of named f64 fields on each bar.
pub enum IndicatorBox {
    Sma(Sma),
    Ema(Ema),
    Wma(Wma),
    Hma(Hma),
    Rsi(Rsi),
    Macd(Macd),
    BBands(BBands),
    Stochastic(Stochastic),
    Adx(Adx),
    Atr(Atr),
    Cci(Cci),
    Obv(Obv),
    Mfi(Mfi),
    Roc(Roc),
    Trix(Trix),
    WilliamsR(WilliamsR),
    SuperTrend(SuperTrend),
    ParabolicSar(ParabolicSar),
    Keltner(Keltner),
    Ichimoku(Ichimoku),
    Rwi(Rwi),
    VolatilityRatio(VolatilityRatio),
    Kama(Kama),
    Tsi(Tsi),
    StochRsi(StochasticRsi),
    Chop(Chop),
    ConnorsRsi(ConnorsRsi),
    Tema(Tema),
    Kdj(Kdj),
    Ao(AwesomeOscillator),
    Cmf(Cmf),
}

fn get_usize(v: &Value, key: &str, default: usize) -> usize {
    v.get(key)
        .and_then(Value::as_f64)
        .map(|x| x as usize)
        .unwrap_or(default)
}

fn get_f64(v: &Value, key: &str, default: f64) -> f64 {
    v.get(key)
        .and_then(Value::as_f64)
        .unwrap_or(default)
}

impl IndicatorBox {
    /// Build from `{ "type": "rsi", "period": 14, ... }`.
    pub fn from_config(cfg: &Value) -> Result<Self> {
        let type_ = cfg
            .get("type")
            .and_then(Value::as_str)
            .unwrap_or_default();

        let ind = match type_ {
            "sma" => Self::Sma(Sma::new(get_usize(cfg, "period", 20))),
            "ema" => Self::Ema(Ema::new(get_usize(cfg, "period", 20))),
            "wma" => Self::Wma(Wma::new(get_usize(cfg, "period", 20))),
            "hma" => Self::Hma(Hma::new(get_usize(cfg, "period", 20))),
            "rsi" => Self::Rsi(Rsi::new(get_usize(cfg, "period", 14))),
            "macd" => Self::Macd(Macd::new(
                get_usize(cfg, "fast", 12),
                get_usize(cfg, "slow", 26),
                get_usize(cfg, "signal", 9),
            )),
            "bbands" => Self::BBands(BBands::new(
                get_usize(cfg, "period", 20),
                get_f64(cfg, "multiplier", 2.0),
            )),
            "stochastic" => Self::Stochastic(Stochastic::new(
                get_usize(cfg, "k_period", 14),
                get_usize(cfg, "d_period", 3),
            )),
            "adx" => Self::Adx(Adx::new(get_usize(cfg, "period", 14))),
            "atr" => Self::Atr(Atr::new(get_usize(cfg, "period", 14))),
            "cci" => Self::Cci(Cci::new(get_usize(cfg, "period", 20))),
            "obv" => Self::Obv(Obv::new()),
            "mfi" => Self::Mfi(Mfi::new(get_usize(cfg, "period", 14))),
            "roc" => Self::Roc(Roc::new(get_usize(cfg, "period", 10))),
            "trix" => Self::Trix(Trix::new(
                get_usize(cfg, "period", 18),
                get_usize(cfg, "signal", 9),
            )),
            "williams_r" => Self::WilliamsR(WilliamsR::new(get_usize(cfg, "period", 14))),
            "supertrend" => Self::SuperTrend(SuperTrend::new(
                get_usize(cfg, "period", 10),
                get_f64(cfg, "multiplier", 3.0),
            )),
            "parabolic_sar" => Self::ParabolicSar(ParabolicSar::new(
                get_f64(cfg, "step", 0.02),
                get_f64(cfg, "max", 0.2),
            )),
            "keltner" => Self::Keltner(Keltner::new(
                get_usize(cfg, "period", 20),
                get_usize(cfg, "atr_period", 10),
                get_f64(cfg, "multiplier", 2.0),
            )),
            "ichimoku" => Self::Ichimoku(Ichimoku::new(
                get_usize(cfg, "tenkan", 9),
                get_usize(cfg, "kijun", 26),
                get_usize(cfg, "senkou_b", 52),
            )),
            "rwi" => Self::Rwi(Rwi::new(get_usize(cfg, "period", 14))),
            "volatility_ratio" => {
                Self::VolatilityRatio(VolatilityRatio::new(get_usize(cfg, "lookback", 10)))
            }
            "kama" => Self::Kama(Kama::new(
                get_usize(cfg, "er_period", 10),
                get_usize(cfg, "fast", 2),
                get_usize(cfg, "slow", 30),
            )),
            "tsi" => Self::Tsi(Tsi::new(
                get_usize(cfg, "first", 25),
                get_usize(cfg, "second", 13),
            )),
            "stoch_rsi" => Self::StochRsi(StochasticRsi::new(
                get_usize(cfg, "rsi_period", 14),
                get_usize(cfg, "smooth_d", 3),
            )),
            "chop" => Self::Chop(Chop::new(get_usize(cfg, "period", 14))),
            "connors_rsi" => Self::ConnorsRsi(ConnorsRsi::new(
                get_usize(cfg, "rsi_period", 3),
                get_usize(cfg, "streak_period", 2),
                get_usize(cfg, "rank_period", 100),
            )),
            "tema" => Self::Tema(Tema::new(get_usize(cfg, "period", 20))),
            "kdj" => Self::Kdj(Kdj::new(
                get_usize(cfg, "period", 9),
                get_usize(cfg, "k_period", 3),
                get_usize(cfg, "d_period", 3),
            )),
            "ao" => Self::Ao(AwesomeOscillator::new(
                get_usize(cfg, "fast", 5),
                get_usize(cfg, "slow", 34),
            )),
            "cmf" => Self::Cmf(Cmf::new(get_usize(cfg, "period", 20))),
            other => bail!("unknown indicator type: '{other}'"),
        };
        Ok(ind)
    }

    /// Feed one bar and return named field values (None = not warmed up yet).
    pub fn update(&mut self, bar: &Bar) -> Option<HashMap<String, f64>> {
        match self {
            Self::Sma(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Ema(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Wma(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Hma(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Rsi(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Macd(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("macd".into(), v.macd);
                m.insert("signal".into(), v.signal);
                m.insert("histogram".into(), v.histogram);
                Some(m)
            }
            Self::BBands(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("upper".into(), v.upper);
                m.insert("middle".into(), v.middle);
                m.insert("lower".into(), v.lower);
                m.insert("bandwidth".into(), v.bandwidth);
                m.insert("percent_b".into(), v.percent_b);
                Some(m)
            }
            Self::Stochastic(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("k".into(), v.k);
                m.insert("d".into(), v.d);
                Some(m)
            }
            Self::Adx(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("adx".into(), v.adx);
                m.insert("plus_di".into(), v.plus_di);
                m.insert("minus_di".into(), v.minus_di);
                Some(m)
            }
            Self::Atr(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("atr".into(), v.atr);
                Some(m)
            }
            Self::Cci(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Obv(i) => {
                let v = i.update(bar.close, bar.volume);
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Mfi(i) => {
                let v = i.update(bar.high, bar.low, bar.close, bar.volume)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Roc(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Trix(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("trix".into(), v.trix);
                m.insert("signal".into(), v.signal);
                m.insert("histogram".into(), v.histogram);
                Some(m)
            }
            Self::WilliamsR(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::SuperTrend(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v.value);
                m.insert("bullish".into(), if v.is_bullish { 1.0 } else { 0.0 });
                Some(m)
            }
            Self::ParabolicSar(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("sar".into(), v.sar);
                m.insert("bullish".into(), if v.is_bullish { 1.0 } else { 0.0 });
                Some(m)
            }
            Self::Keltner(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("upper".into(), v.upper);
                m.insert("middle".into(), v.middle);
                m.insert("lower".into(), v.lower);
                Some(m)
            }
            Self::Ichimoku(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("tenkan".into(), v.tenkan);
                m.insert("kijun".into(), v.kijun);
                m.insert("senkou_a".into(), v.senkou_a);
                m.insert("senkou_b".into(), v.senkou_b);
                m.insert("chikou".into(), v.chikou);
                m.insert("above_cloud".into(), if v.above_cloud { 1.0 } else { 0.0 });
                Some(m)
            }
            Self::Rwi(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("rwi_high".into(), v.rwi_high);
                m.insert("rwi_low".into(), v.rwi_low);
                Some(m)
            }
            Self::VolatilityRatio(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Kama(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Tsi(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::StochRsi(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("k".into(), v.k);
                m.insert("d".into(), v.d);
                Some(m)
            }
            Self::Chop(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::ConnorsRsi(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Tema(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Kdj(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("k".into(), v.k);
                m.insert("d".into(), v.d);
                m.insert("j".into(), v.j);
                Some(m)
            }
            Self::Ao(i) => {
                let v = i.update(bar.high, bar.low)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Cmf(i) => {
                let v = i.update(bar.high, bar.low, bar.close, bar.volume)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
        }
    }

    pub fn reset(&mut self) {
        match self {
            Self::Sma(i) => i.reset(),
            Self::Ema(i) => i.reset(),
            Self::Wma(i) => i.reset(),
            Self::Hma(i) => i.reset(),
            Self::Rsi(i) => i.reset(),
            Self::Macd(i) => i.reset(),
            Self::BBands(i) => i.reset(),
            Self::Stochastic(i) => i.reset(),
            Self::Adx(i) => i.reset(),
            Self::Atr(i) => i.reset(),
            Self::Cci(i) => i.reset(),
            Self::Obv(i) => i.reset(),
            Self::Mfi(i) => i.reset(),
            Self::Roc(i) => i.reset(),
            Self::Trix(i) => i.reset(),
            Self::WilliamsR(i) => i.reset(),
            Self::SuperTrend(i) => i.reset(),
            Self::ParabolicSar(i) => i.reset(),
            Self::Keltner(i) => i.reset(),
            Self::Ichimoku(i) => i.reset(),
            Self::Rwi(i) => i.reset(),
            Self::VolatilityRatio(i) => i.reset(),
            Self::Kama(i) => i.reset(),
            Self::Tsi(i) => i.reset(),
            Self::StochRsi(i) => i.reset(),
            Self::Chop(i) => i.reset(),
            Self::ConnorsRsi(i) => i.reset(),
            Self::Tema(i) => i.reset(),
            Self::Kdj(i) => i.reset(),
            Self::Ao(i) => i.reset(),
            Self::Cmf(i) => i.reset(),
        }
    }
}
