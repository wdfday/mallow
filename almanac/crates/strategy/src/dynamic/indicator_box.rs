use std::collections::HashMap;

use anyhow::{bail, Result};
use alm_core::Bar;
use alm_indicator::{
    // Trend / MA
    Adx, Alligator, Alma, Aroon, Dema, Dmi, Ema, Gmma, Hma, Kama, Kdj, Lsma, Macd,
    McGinleyDynamic, Sma, Smma, Tema, Trix, Vortex, Vwma, Wma,
    // Momentum
    AwesomeOscillator, Bop, BullBearPower, Cci, Cmo, ConnorsRsi, Coppock, Dpo, Fisher, Kst,
    Mfi, Mom, Pmo, Ppo, Rci, Roc, Rsi, Rvi, Smi, Stochastic, StochasticRsi, Tsi, Uo, WilliamsR,
    // Volatility
    Atr, BBands, ChandeKrollStop, ChandelierExit, Chop, ChopZone, ChopZoneClass, Donchian,
    Keltner, SuperTrend, VolatilityRatio,
    // Volume
    Cmf, Obv, Vwap,
    // Pattern
    Ichimoku, ParabolicSar, Rwi, WilliamsFractal,
};
use serde_json::Value;

/// A boxed indicator that can be built from JSON config and produces
/// a flat map of named f64 fields on each bar.
pub enum IndicatorBox {
    // ── Trend / MA ────────────────────────────────────────────────────────────
    Sma(Sma),
    Ema(Ema),
    Wma(Wma),
    Hma(Hma),
    Dema(Dema),
    Tema(Tema),
    Smma(Smma),
    Alma(Alma),
    McGinley(McGinleyDynamic),
    Lsma(Lsma),
    Vwma(Vwma),
    Kama(Kama),
    Macd(Macd),
    Trix(Trix),
    Adx(Adx),
    Dmi(Dmi),
    Aroon(Aroon),
    Vortex(Vortex),
    Alligator(Alligator),
    Gmma(Gmma),
    Kdj(Kdj),
    // ── Momentum / Oscillator ─────────────────────────────────────────────────
    Rsi(Rsi),
    Cci(Cci),
    Roc(Roc),
    Mom(Mom),
    Cmo(Cmo),
    Dpo(Dpo),
    Mfi(Mfi),
    Bop(Bop),
    WilliamsR(WilliamsR),
    Stochastic(Stochastic),
    StochRsi(StochasticRsi),
    Tsi(Tsi),
    Rci(Rci),
    BullBear(BullBearPower),
    Fisher(Fisher),
    Kst(Kst),
    Pmo(Pmo),
    Ppo(Ppo),
    Rvi(Rvi),
    Smi(Smi),
    Uo(Uo),
    ConnorsRsi(ConnorsRsi),
    Ao(AwesomeOscillator),
    Coppock(Coppock),
    // ── Volatility ────────────────────────────────────────────────────────────
    Atr(Atr),
    BBands(BBands),
    Keltner(Keltner),
    SuperTrend(SuperTrend),
    Donchian(Donchian),
    Chop(Chop),
    ChopZone(ChopZone),
    ChandelierExit(ChandelierExit),
    ChandeKroll(ChandeKrollStop),
    VolatilityRatio(VolatilityRatio),
    // ── Volume ────────────────────────────────────────────────────────────────
    Obv(Obv),
    Cmf(Cmf),
    Vwap(Vwap),
    // ── Pattern ───────────────────────────────────────────────────────────────
    Ichimoku(Ichimoku),
    ParabolicSar(ParabolicSar),
    Rwi(Rwi),
    WilliamsFractal(WilliamsFractal),
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
            // ── Trend / MA ────────────────────────────────────────────────────
            "sma"       => Self::Sma(Sma::new(get_usize(cfg, "period", 20))),
            "ema"       => Self::Ema(Ema::new(get_usize(cfg, "period", 20))),
            "wma"       => Self::Wma(Wma::new(get_usize(cfg, "period", 20))),
            "hma"       => Self::Hma(Hma::new(get_usize(cfg, "period", 20))),
            "dema"      => Self::Dema(Dema::new(get_usize(cfg, "period", 20))),
            "tema"      => Self::Tema(Tema::new(get_usize(cfg, "period", 20))),
            "smma"      => Self::Smma(Smma::new(get_usize(cfg, "period", 20))),
            "alma"      => Self::Alma(Alma::new(
                get_usize(cfg, "period", 9),
                get_f64(cfg, "offset", 0.85),
                get_f64(cfg, "sigma", 6.0),
            )),
            "mcginley"  => Self::McGinley(McGinleyDynamic::new(get_usize(cfg, "period", 14))),
            "lsma"      => Self::Lsma(Lsma::new(get_usize(cfg, "period", 25))),
            "vwma"      => Self::Vwma(Vwma::new(get_usize(cfg, "period", 20))),
            "kama"      => Self::Kama(Kama::new(
                get_usize(cfg, "er_period", 10),
                get_usize(cfg, "fast", 2),
                get_usize(cfg, "slow", 30),
            )),
            "macd"      => Self::Macd(Macd::new(
                get_usize(cfg, "fast", 12),
                get_usize(cfg, "slow", 26),
                get_usize(cfg, "signal", 9),
            )),
            "trix"      => Self::Trix(Trix::new(
                get_usize(cfg, "period", 18),
                get_usize(cfg, "signal", 9),
            )),
            "adx"       => Self::Adx(Adx::new(get_usize(cfg, "period", 14))),
            "dmi"       => Self::Dmi(Dmi::new(get_usize(cfg, "period", 14))),
            "aroon"     => Self::Aroon(Aroon::new(get_usize(cfg, "period", 25))),
            "vortex"    => Self::Vortex(Vortex::new(get_usize(cfg, "period", 14))),
            "alligator" => Self::Alligator(Alligator::new(
                get_usize(cfg, "jaw", 13),
                get_usize(cfg, "teeth", 8),
                get_usize(cfg, "lips", 5),
            )),
            "gmma"      => Self::Gmma(Gmma::new()),
            "kdj"       => Self::Kdj(Kdj::new(
                get_usize(cfg, "period", 9),
                get_usize(cfg, "k_period", 3),
                get_usize(cfg, "d_period", 3),
            )),
            // ── Momentum / Oscillator ─────────────────────────────────────────
            "rsi"         => Self::Rsi(Rsi::new(get_usize(cfg, "period", 14))),
            "cci"         => Self::Cci(Cci::new(get_usize(cfg, "period", 20))),
            "roc"         => Self::Roc(Roc::new(get_usize(cfg, "period", 10))),
            "mom"         => Self::Mom(Mom::new(get_usize(cfg, "period", 10))),
            "cmo"         => Self::Cmo(Cmo::new(get_usize(cfg, "period", 14))),
            "dpo"         => Self::Dpo(Dpo::new(get_usize(cfg, "period", 20))),
            "mfi"         => Self::Mfi(Mfi::new(get_usize(cfg, "period", 14))),
            "bop"         => Self::Bop(Bop::new()),
            "williams_r"  => Self::WilliamsR(WilliamsR::new(get_usize(cfg, "period", 14))),
            "stochastic"  => Self::Stochastic(Stochastic::new(
                get_usize(cfg, "k_period", 14),
                get_usize(cfg, "d_period", 3),
            )),
            "stoch_rsi"   => Self::StochRsi(StochasticRsi::new(
                get_usize(cfg, "rsi_period", 14),
                get_usize(cfg, "smooth_d", 3),
            )),
            "tsi"         => Self::Tsi(Tsi::new(
                get_usize(cfg, "first", 25),
                get_usize(cfg, "second", 13),
            )),
            "rci"         => Self::Rci(Rci::new(get_usize(cfg, "period", 9))),
            "bull_bear"   => Self::BullBear(BullBearPower::new(get_usize(cfg, "period", 13))),
            "fisher"      => Self::Fisher(Fisher::new(get_usize(cfg, "period", 9))),
            "kst"         => Self::Kst(Kst::standard()),
            "pmo"         => Self::Pmo(Pmo::standard()),
            "ppo"         => Self::Ppo(Ppo::new(
                get_usize(cfg, "fast", 12),
                get_usize(cfg, "slow", 26),
                get_usize(cfg, "signal", 9),
            )),
            "rvi"         => Self::Rvi(Rvi::new(get_usize(cfg, "period", 10))),
            "smi"         => Self::Smi(Smi::standard()),
            "uo"          => Self::Uo(Uo::new(
                get_usize(cfg, "fast", 7),
                get_usize(cfg, "medium", 14),
                get_usize(cfg, "slow", 28),
            )),
            "connors_rsi" => Self::ConnorsRsi(ConnorsRsi::new(
                get_usize(cfg, "rsi_period", 3),
                get_usize(cfg, "streak_period", 2),
                get_usize(cfg, "rank_period", 100),
            )),
            "ao"          => Self::Ao(AwesomeOscillator::new(
                get_usize(cfg, "fast", 5),
                get_usize(cfg, "slow", 34),
            )),
            "coppock"     => Self::Coppock(Coppock::standard()),
            // ── Volatility ────────────────────────────────────────────────────
            "atr"               => Self::Atr(Atr::new(get_usize(cfg, "period", 14))),
            "bbands"            => Self::BBands(BBands::new(
                get_usize(cfg, "period", 20),
                get_f64(cfg, "multiplier", 2.0),
            )),
            "keltner"           => Self::Keltner(Keltner::new(
                get_usize(cfg, "period", 20),
                get_usize(cfg, "atr_period", 10),
                get_f64(cfg, "multiplier", 2.0),
            )),
            "supertrend"        => Self::SuperTrend(SuperTrend::new(
                get_usize(cfg, "period", 10),
                get_f64(cfg, "multiplier", 3.0),
            )),
            "donchian"          => Self::Donchian(Donchian::new(get_usize(cfg, "period", 20))),
            "chop"              => Self::Chop(Chop::new(get_usize(cfg, "period", 14))),
            "chop_zone"         => Self::ChopZone(ChopZone::new(
                get_usize(cfg, "ema_period", 34),
                get_f64(cfg, "threshold", 5.0),
            )),
            "chandelier_exit"   => Self::ChandelierExit(ChandelierExit::new(
                get_usize(cfg, "period", 22),
                get_f64(cfg, "multiplier", 3.0),
            )),
            "chande_kroll"      => Self::ChandeKroll(ChandeKrollStop::new(
                get_usize(cfg, "atr_period", 10),
                get_f64(cfg, "factor", 1.5),
                get_usize(cfg, "stop_period", 9),
            )),
            "volatility_ratio"  => {
                Self::VolatilityRatio(VolatilityRatio::new(get_usize(cfg, "lookback", 10)))
            }
            // ── Volume ────────────────────────────────────────────────────────
            "obv"  => Self::Obv(Obv::new()),
            "cmf"  => Self::Cmf(Cmf::new(get_usize(cfg, "period", 20))),
            "vwap" => Self::Vwap(Vwap::new(get_usize(cfg, "session_gap_mins", 390) as u64)),
            // ── Pattern ───────────────────────────────────────────────────────
            "ichimoku"         => Self::Ichimoku(Ichimoku::new(
                get_usize(cfg, "tenkan", 9),
                get_usize(cfg, "kijun", 26),
                get_usize(cfg, "senkou_b", 52),
            )),
            "parabolic_sar"    => Self::ParabolicSar(ParabolicSar::new(
                get_f64(cfg, "step", 0.02),
                get_f64(cfg, "max", 0.2),
            )),
            "rwi"              => Self::Rwi(Rwi::new(get_usize(cfg, "period", 14))),
            "fractal"          => Self::WilliamsFractal(WilliamsFractal::new()),
            other => bail!("unknown indicator type: '{other}'"),
        };
        Ok(ind)
    }

    /// Feed one bar and return named field values (None = not warmed up yet).
    pub fn update(&mut self, bar: &Bar) -> Option<HashMap<String, f64>> {
        match self {
            // ── Trend / MA ────────────────────────────────────────────────────
            Self::Sma(i) => scalar(i.update(bar.close)?),
            Self::Ema(i) => scalar(i.update(bar.close)?),
            Self::Wma(i) => scalar(i.update(bar.close)?),
            Self::Hma(i) => scalar(i.update(bar.close)?),
            Self::Dema(i) => scalar(i.update(bar.close)?),
            Self::Tema(i) => scalar(i.update(bar.close)?),
            Self::Smma(i) => scalar(i.update(bar.close)?),
            Self::Alma(i) => scalar(i.update(bar.close)?),
            Self::McGinley(i) => scalar(i.update(bar.close)?),
            Self::Lsma(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v.value);
                m.insert("slope".into(), v.slope);
                Some(m)
            }
            Self::Vwma(i) => scalar(i.update(bar.close, bar.volume)?),
            Self::Kama(i) => scalar(i.update(bar.close)?),
            Self::Macd(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("macd".into(), v.macd);
                m.insert("signal".into(), v.signal);
                m.insert("histogram".into(), v.histogram);
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
            Self::Adx(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("adx".into(), v.adx);
                m.insert("plus_di".into(), v.plus_di);
                m.insert("minus_di".into(), v.minus_di);
                Some(m)
            }
            Self::Dmi(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("plus_di".into(), v.plus_di);
                m.insert("minus_di".into(), v.minus_di);
                m.insert("dx".into(), v.dx);
                Some(m)
            }
            Self::Aroon(i) => {
                let v = i.update(bar.high, bar.low)?;
                let mut m = HashMap::new();
                m.insert("up".into(), v.up);
                m.insert("down".into(), v.down);
                m.insert("oscillator".into(), v.oscillator);
                Some(m)
            }
            Self::Vortex(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("plus_vi".into(), v.plus_vi);
                m.insert("minus_vi".into(), v.minus_vi);
                Some(m)
            }
            Self::Alligator(i) => {
                let v = i.update(bar.high, bar.low)?;
                let mut m = HashMap::new();
                m.insert("jaw".into(), v.jaw);
                m.insert("teeth".into(), v.teeth);
                m.insert("lips".into(), v.lips);
                m.insert("bullish".into(), if v.bullish { 1.0 } else { 0.0 });
                Some(m)
            }
            Self::Gmma(i) => {
                let v = i.update(bar.close)?;
                let short_avg = v.short.iter().sum::<f64>() / 6.0;
                let long_avg  = v.long.iter().sum::<f64>() / 6.0;
                let mut m = HashMap::new();
                m.insert("short_avg".into(), short_avg);
                m.insert("long_avg".into(), long_avg);
                m.insert("bullish".into(), if v.bullish { 1.0 } else { 0.0 });
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
            // ── Momentum / Oscillator ─────────────────────────────────────────
            Self::Rsi(i)      => scalar(i.update(bar.close)?),
            Self::Cci(i)      => scalar(i.update(bar.high, bar.low, bar.close)?),
            Self::Roc(i)      => scalar(i.update(bar.close)?),
            Self::Mom(i)      => scalar(i.update(bar.close)?),
            Self::Cmo(i)      => scalar(i.update(bar.close)?),
            Self::Dpo(i)      => scalar(i.update(bar.close)?),
            Self::Mfi(i)      => scalar(i.update(bar.high, bar.low, bar.close, bar.volume)?),
            Self::Bop(i)      => scalar(i.update(bar.open, bar.high, bar.low, bar.close)?),
            Self::WilliamsR(i) => scalar(i.update(bar.high, bar.low, bar.close)?),
            Self::Stochastic(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("k".into(), v.k);
                m.insert("d".into(), v.d);
                Some(m)
            }
            Self::StochRsi(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("k".into(), v.k);
                m.insert("d".into(), v.d);
                Some(m)
            }
            Self::Tsi(i)      => scalar(i.update(bar.close)?),
            Self::Rci(i)      => scalar(i.update(bar.close)?),
            Self::BullBear(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("bull".into(), v.bull);
                m.insert("bear".into(), v.bear);
                m.insert("ema".into(), v.ema);
                Some(m)
            }
            Self::Fisher(i) => {
                let v = i.update(bar.high, bar.low)?;
                let mut m = HashMap::new();
                m.insert("fisher".into(), v.fisher);
                m.insert("signal".into(), v.signal);
                Some(m)
            }
            Self::Kst(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("kst".into(), v.kst);
                m.insert("signal".into(), v.signal);
                m.insert("histogram".into(), v.histogram);
                Some(m)
            }
            Self::Pmo(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("pmo".into(), v.pmo);
                m.insert("signal".into(), v.signal);
                m.insert("histogram".into(), v.histogram);
                Some(m)
            }
            Self::Ppo(i) => {
                let v = i.update(bar.close)?;
                let mut m = HashMap::new();
                m.insert("ppo".into(), v.ppo);
                m.insert("signal".into(), v.signal);
                m.insert("histogram".into(), v.histogram);
                Some(m)
            }
            Self::Rvi(i) => {
                let v = i.update(bar.open, bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("rvi".into(), v.rvi);
                m.insert("signal".into(), v.signal);
                Some(m)
            }
            Self::Smi(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("smi".into(), v.smi);
                m.insert("signal".into(), v.signal);
                Some(m)
            }
            Self::Uo(i)         => scalar(i.update(bar.high, bar.low, bar.close)?),
            Self::ConnorsRsi(i) => scalar(i.update(bar.close)?),
            Self::Ao(i)         => scalar(i.update(bar.high, bar.low)?),
            Self::Coppock(i)    => scalar(i.update(bar.close)?),
            // ── Volatility ────────────────────────────────────────────────────
            Self::Atr(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("atr".into(), v.atr);
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
            Self::Keltner(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("upper".into(), v.upper);
                m.insert("middle".into(), v.middle);
                m.insert("lower".into(), v.lower);
                Some(m)
            }
            Self::SuperTrend(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("value".into(), v.value);
                m.insert("bullish".into(), if v.is_bullish { 1.0 } else { 0.0 });
                Some(m)
            }
            Self::Donchian(i) => {
                let v = i.update(bar.high, bar.low)?;
                let mut m = HashMap::new();
                m.insert("upper".into(), v.upper);
                m.insert("middle".into(), v.middle);
                m.insert("lower".into(), v.lower);
                Some(m)
            }
            Self::Chop(i) => scalar(i.update(bar.high, bar.low, bar.close)?),
            Self::ChopZone(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let zone_f = match v.zone {
                    ChopZoneClass::TrendingUp   =>  1.0,
                    ChopZoneClass::Choppy        =>  0.0,
                    ChopZoneClass::TrendingDown  => -1.0,
                };
                let mut m = HashMap::new();
                m.insert("angle".into(), v.angle);
                m.insert("zone".into(), zone_f);
                Some(m)
            }
            Self::ChandelierExit(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("long_stop".into(), v.long_stop);
                m.insert("short_stop".into(), v.short_stop);
                m.insert("atr".into(), v.atr);
                Some(m)
            }
            Self::ChandeKroll(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("stop_long".into(), v.stop_long);
                m.insert("stop_short".into(), v.stop_short);
                Some(m)
            }
            Self::VolatilityRatio(i) => scalar(i.update(bar.high, bar.low, bar.close)?),
            // ── Volume ────────────────────────────────────────────────────────
            Self::Obv(i) => {
                let v = i.update(bar.close, bar.volume);
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            Self::Cmf(i) => scalar(i.update(bar.high, bar.low, bar.close, bar.volume)?),
            Self::Vwap(i) => {
                let v = i.update(bar.timestamp, bar.high, bar.low, bar.close, bar.volume);
                let mut m = HashMap::new();
                m.insert("value".into(), v);
                Some(m)
            }
            // ── Pattern ───────────────────────────────────────────────────────
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
            Self::ParabolicSar(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("sar".into(), v.sar);
                m.insert("bullish".into(), if v.is_bullish { 1.0 } else { 0.0 });
                Some(m)
            }
            Self::Rwi(i) => {
                let v = i.update(bar.high, bar.low, bar.close)?;
                let mut m = HashMap::new();
                m.insert("rwi_high".into(), v.rwi_high);
                m.insert("rwi_low".into(), v.rwi_low);
                Some(m)
            }
            Self::WilliamsFractal(i) => {
                let v = i.update(bar.high, bar.low)?;
                let mut m = HashMap::new();
                m.insert("bullish".into(), if v.bullish { 1.0 } else { 0.0 });
                m.insert("bearish".into(), if v.bearish { 1.0 } else { 0.0 });
                m.insert("fractal_high".into(), v.high);
                m.insert("fractal_low".into(), v.low);
                Some(m)
            }
        }
    }

    pub fn reset(&mut self) {
        match self {
            Self::Sma(i)            => i.reset(),
            Self::Ema(i)            => i.reset(),
            Self::Wma(i)            => i.reset(),
            Self::Hma(i)            => i.reset(),
            Self::Dema(i)           => i.reset(),
            Self::Tema(i)           => i.reset(),
            Self::Smma(i)           => i.reset(),
            Self::Alma(i)           => i.reset(),
            Self::McGinley(i)       => i.reset(),
            Self::Lsma(i)           => i.reset(),
            Self::Vwma(i)           => i.reset(),
            Self::Kama(i)           => i.reset(),
            Self::Macd(i)           => i.reset(),
            Self::Trix(i)           => i.reset(),
            Self::Adx(i)            => i.reset(),
            Self::Dmi(i)            => i.reset(),
            Self::Aroon(i)          => i.reset(),
            Self::Vortex(i)         => i.reset(),
            Self::Alligator(i)      => i.reset(),
            Self::Gmma(i)           => i.reset(),
            Self::Kdj(i)            => i.reset(),
            Self::Rsi(i)            => i.reset(),
            Self::Cci(i)            => i.reset(),
            Self::Roc(i)            => i.reset(),
            Self::Mom(i)            => i.reset(),
            Self::Cmo(i)            => i.reset(),
            Self::Dpo(i)            => i.reset(),
            Self::Mfi(i)            => i.reset(),
            Self::Bop(_)            => {} // stateless, nothing to reset
            Self::WilliamsR(i)      => i.reset(),
            Self::Stochastic(i)     => i.reset(),
            Self::StochRsi(i)       => i.reset(),
            Self::Tsi(i)            => i.reset(),
            Self::Rci(i)            => i.reset(),
            Self::BullBear(i)       => i.reset(),
            Self::Fisher(i)         => i.reset(),
            Self::Kst(i)            => i.reset(),
            Self::Pmo(i)            => i.reset(),
            Self::Ppo(i)            => i.reset(),
            Self::Rvi(i)            => i.reset(),
            Self::Smi(i)            => i.reset(),
            Self::Uo(i)             => i.reset(),
            Self::ConnorsRsi(i)     => i.reset(),
            Self::Ao(i)             => i.reset(),
            Self::Coppock(i)        => i.reset(),
            Self::Atr(i)            => i.reset(),
            Self::BBands(i)         => i.reset(),
            Self::Keltner(i)        => i.reset(),
            Self::SuperTrend(i)     => i.reset(),
            Self::Donchian(i)       => i.reset(),
            Self::Chop(i)           => i.reset(),
            Self::ChopZone(i)       => i.reset(),
            Self::ChandelierExit(i) => i.reset(),
            Self::ChandeKroll(i)    => i.reset(),
            Self::VolatilityRatio(i) => i.reset(),
            Self::Obv(i)            => i.reset(),
            Self::Cmf(i)            => i.reset(),
            Self::Vwap(i)           => i.reset(),
            Self::Ichimoku(i)       => i.reset(),
            Self::ParabolicSar(i)   => i.reset(),
            Self::Rwi(i)            => i.reset(),
            Self::WilliamsFractal(i) => i.reset(),
        }
    }
}

/// Convenience: wrap a single f64 value in a `HashMap` under key `"value"`.
fn scalar(v: f64) -> Option<HashMap<String, f64>> {
    let mut m = HashMap::new();
    m.insert("value".into(), v);
    Some(m)
}
