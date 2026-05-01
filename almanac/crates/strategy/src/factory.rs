//! Strategy factory — build any strategy by name from a flat JSON params object.
//!
//! Used by both `backtest-api` (offline backtest) and `herald` (live NATS).
//!
//! # Params format
//! A flat `serde_json::Value::Object` of scalar values, e.g.:
//! ```json
//! { "fast": 12, "slow": 26, "signal": 9 }
//! ```
//! Unknown keys are silently ignored; missing keys fall back to sensible defaults.
//!
//! `CelStrategy` uses a simple expression format — see [`crate::expr`] docs.

use anyhow::{bail, Result};
use alm_core::strategy::Strategy;
use serde_json::Value;

use crate::{
    expr::cel::CelStrategy,
    expr::rhai_strategy::RhaiStrategy,
    // dynamic::DynamicStrategy,  // deprecated
    AdxEmaCross, AlmaCross, AlligatorStrategy, AroonTrend, AtrTrailingStop, BbKeltnerSqueeze,
    BbRsiReversal, BbSqueeze, BollingerMacd, CciReversal, ChandelierExit, ChopFilterStrategy,
    CmfEmaTrend, CmoZeroCross, ConnorsRsiStrategy, DemaCrossover, DmiAdx, DonchianBreakout,
    DualMomentum, ElderRayStrategy, EquilibriumExplorer, FisherCrossover, GmmaCrossover,
    HaBreakout, HaColor, HaHarmonizer, HighestBreakout, HmaCrossover, IchimokuCloud, IchimokuCross,
    KamaStrategy, KeltnerBreakout, LsmaCross, MaCrossover, MaPullback, MacdCrossover, MacdMa,
    MeanReversion, MfiRevert, MfiTrend, MomentumRoc, ObvEmaTrend, OrbBreakout, OscillatorOverlord,
    PatternBreakoutStrategy, Pixel3, PpoHistogram, PriceActionSwing, RangeRover, ReversalCatcher,
    RocCrossover, RsiMaCross, RsiMeanRev, RwiStrategy, SarStrategy, ScalpingEma, SmiReversal,
    StochRsiStrategy, StochasticCrossover, StochasticDk, SupertrendMacd, SupertrendStrategy,
    SwingTrader, TrendFollower, TrendTransition, TripleEma, TrixStrategy,
    TsiStrategy, UoReversal, VolatilityRatioBreakout, VolatilitySqueezer, VolatilityVanguard,
    VortexTrend, VwapBounce, VwapTrend, VwmaRsi, WaddahAttar, WilliamsRMa, Wolfstein,
};

// ── Parameter helpers ──────────────────────────────────────────────────────────

pub fn pf64(p: &Value, key: &str, default: f64) -> f64 {
    p.get(key).and_then(Value::as_f64).unwrap_or(default)
}

pub fn pu(p: &Value, key: &str, default: usize) -> usize {
    p.get(key)
        .and_then(Value::as_u64)
        .map(|v| v as usize)
        .unwrap_or(default)
}

// ── Factory ────────────────────────────────────────────────────────────────────

/// Build a boxed `Strategy` from a strategy key and a flat JSON params object.
///
/// `params` is `serde_json::Value::Object` (or `Value::Null` for defaults).
pub fn build_strategy(name: &str, params: &Value) -> Result<Box<dyn Strategy>> {
    let p = params;

    let s: Box<dyn Strategy> = match name {
        // ── MA / EMA ──────────────────────────────────────────────────────────
        "ma_crossover" => Box::new(MaCrossover::new(pu(p, "fast", 20), pu(p, "slow", 50))),

        "triple_ema" => Box::new(TripleEma::new(
            pu(p, "ema1", 10),
            pu(p, "ema2", 20),
            pu(p, "ema3", 50),
        )),

        "hma_crossover" => Box::new(HmaCrossover::new(pu(p, "fast", 16), pu(p, "slow", 49))),

        // ── RSI ───────────────────────────────────────────────────────────────
        "rsi_mean_rev" => Box::new(RsiMeanRev::new(
            pu(p, "period", 14),
            pf64(p, "oversold", 30.0),
            pf64(p, "overbought", 70.0),
        )),

        // ── MACD ──────────────────────────────────────────────────────────────
        "macd_crossover" => Box::new(MacdCrossover::new(
            pu(p, "fast", 12),
            pu(p, "slow", 26),
            pu(p, "signal", 9),
        )),

        "macd_ma" => Box::new(MacdMa::new(
            pu(p, "fast", 12),
            pu(p, "slow", 26),
            pu(p, "signal", 9),
            pu(p, "ma", 50),
        )),

        "waddah_attar" => Box::new(WaddahAttar::new(
            pu(p, "fast", 20),
            pu(p, "slow", 40),
            pu(p, "bb_period", 20),
            pf64(p, "bb_std", 2.0),
        )),

        // ── Stochastic ────────────────────────────────────────────────────────
        "stochastic_crossover" => Box::new(StochasticCrossover::new(
            pu(p, "k_period", 14),
            pu(p, "d_period", 3),
            pf64(p, "oversold", 20.0),
            pf64(p, "overbought", 80.0),
        )),

        "stochastic_dk" => Box::new(StochasticDk::new(pu(p, "k", 14), pu(p, "d", 3))),

        "range_rover" => Box::new(RangeRover::new(
            pu(p, "k", 14),
            pu(p, "d", 3),
            pu(p, "ma", 50),
            pf64(p, "oversold", 20.0),
            pf64(p, "overbought", 80.0),
        )),

        "reversal_catcher" => Box::new(ReversalCatcher::new(
            pu(p, "k", 14),
            pu(p, "d", 3),
            pu(p, "rsi_period", 14),
        )),

        // ── ATR / Volatility ──────────────────────────────────────────────────
        "atr_trailing" => Box::new(AtrTrailingStop::new(
            pu(p, "ema_period", 20),
            pu(p, "atr_period", 14),
            pf64(p, "atr_multiplier", 2.0),
        )),

        "volatility_squeezer" => Box::new(VolatilitySqueezer::new(
            pu(p, "atr_period", 14),
            pu(p, "ma_period", 50),
        )),

        "volatility_vanguard" => Box::new(VolatilityVanguard::new(
            pu(p, "bb_period", 20),
            pf64(p, "bb_std", 2.0),
            pu(p, "atr_period", 14),
        )),

        "volatility_ratio" => Box::new(VolatilityRatioBreakout::new(
            pu(p, "lookback", 10),
            pf64(p, "threshold", 0.5),
        )),

        // ── Bollinger Bands ───────────────────────────────────────────────────
        "bollinger_macd" => Box::new(BollingerMacd::new(
            pu(p, "bb_period", 20),
            pf64(p, "bb_std", 2.0),
            pu(p, "fast", 12),
            pu(p, "slow", 26),
            pu(p, "signal", 9),
        )),

        "bb_squeeze" => Box::new(BbSqueeze::new(pu(p, "period", 20), pf64(p, "std", 2.0))),

        "mean_reversion" => Box::new(MeanReversion::new(
            pu(p, "bb_period", 20),
            pf64(p, "bb_std", 2.0),
            pu(p, "rsi_period", 14),
            pu(p, "bars", 4),
        )),

        "bb_keltner_squeeze" => Box::new(BbKeltnerSqueeze::new(
            pu(p, "bb_period", 20),
            pf64(p, "bb_std", 2.0),
            pu(p, "kc_period", 20),
            pu(p, "kc_atr", 10),
            pf64(p, "kc_mult", 1.5),
        )),

        // ── ADX / DMI ─────────────────────────────────────────────────────────
        "dmi_adx" => Box::new(DmiAdx::new(pu(p, "period", 14), pf64(p, "adx_threshold", 25.0))),

        "wolfstein" => Box::new(Wolfstein::new(
            pu(p, "adx_period", 14),
            pf64(p, "long_threshold", 27.5),
            pf64(p, "short_threshold", 20.5),
        )),

        "trend_transition" => Box::new(TrendTransition::new(
            pu(p, "fast", 50),
            pu(p, "slow", 200),
            pu(p, "adx_period", 14),
            pf64(p, "adx_threshold", 25.0),
        )),

        "swing_trader" => Box::new(SwingTrader::new(
            pu(p, "cci_period", 20),
            pu(p, "adx_period", 14),
            pf64(p, "adx_threshold", 25.0),
        )),

        // ── Multi-indicator ───────────────────────────────────────────────────
        "oscillator_overlord" => Box::new(OscillatorOverlord::new(
            pu(p, "rsi_period", 14),
            pu(p, "stoch_k", 14),
            pu(p, "stoch_d", 3),
            pu(p, "cci_period", 20),
        )),

        "equilibrium_explorer" => Box::new(EquilibriumExplorer::new(
            pu(p, "ema_period", 200),
            pu(p, "stoch_k", 14),
            pu(p, "stoch_d", 3),
            pf64(p, "stoch_oversold", 20.0),
            pf64(p, "stoch_overbought", 80.0),
            pu(p, "macd_fast", 12),
            pu(p, "macd_slow", 26),
            pu(p, "macd_signal", 9),
        )),

        "trend_follower" => Box::new(TrendFollower::new(
            pu(p, "fast_ma", 50),
            pu(p, "slow_ma", 200),
            pu(p, "macd_fast", 12),
            pu(p, "macd_slow", 26),
            pu(p, "macd_signal", 9),
        )),

        "ma_pullback" => Box::new(MaPullback::new(pu(p, "ma_period", 50))),

        // ── CCI ───────────────────────────────────────────────────────────────
        "cci_reversal" => Box::new(CciReversal::new(
            pu(p, "period", 20),
            pf64(p, "entry_level", -100.0),
            pf64(p, "exit_level", 100.0),
        )),

        // ── SuperTrend ────────────────────────────────────────────────────────
        "supertrend" => Box::new(SupertrendStrategy::new(
            pu(p, "period", 10),
            pf64(p, "multiplier", 3.0),
        )),

        "supertrend_macd" => Box::new(SupertrendMacd::new(
            pu(p, "period", 10),
            pf64(p, "multiplier", 3.0),
            pu(p, "macd_fast", 12),
            pu(p, "macd_slow", 26),
            pu(p, "macd_signal", 9),
        )),

        // ── Parabolic SAR ─────────────────────────────────────────────────────
        "parabolic_sar" => Box::new(SarStrategy::new(pf64(p, "step", 0.02), pf64(p, "max", 0.2))),

        // ── Heiken Ashi ───────────────────────────────────────────────────────
        "heiken_ashi_color" => Box::new(HaColor::new(pu(p, "smooth", 1))),

        "heiken_ashi_breakout" => {
            Box::new(HaBreakout::new(pu(p, "smooth", 1), pu(p, "consecutive_bars", 2)))
        }

        "heiken_ashi_harmonizer" => {
            Box::new(HaHarmonizer::new(pu(p, "smooth", 1), pu(p, "ema_period", 50)))
        }

        // ── GMMA ──────────────────────────────────────────────────────────────
        "gmma_crossover" => Box::new(GmmaCrossover::new()),

        // ── Ichimoku ──────────────────────────────────────────────────────────
        "ichimoku_cloud" => Box::new(IchimokuCloud::new(
            pu(p, "tenkan", 9),
            pu(p, "kijun", 26),
            pu(p, "senkou_b", 52),
        )),

        "ichimoku_cross" => Box::new(IchimokuCross::new(
            pu(p, "tenkan", 9),
            pu(p, "kijun", 26),
            pu(p, "senkou_b", 52),
        )),

        // ── Scalping ──────────────────────────────────────────────────────────
        "scalping_ema" => Box::new(ScalpingEma::new(
            pu(p, "fast", 8),
            pu(p, "slow", 21),
            pu(p, "atr_period", 14),
            pu(p, "atr_ma_period", 20),
        )),

        // ── DEMA ──────────────────────────────────────────────────────────────
        "dema_crossover" => Box::new(DemaCrossover::new(
            pu(p, "fast", 12),
            pu(p, "slow", 26),
        )),

        // ── Donchian ──────────────────────────────────────────────────────────
        "donchian_breakout" => Box::new(DonchianBreakout::new(
            pu(p, "entry", 20),
            pu(p, "exit", 10),
        )),

        // ── Elder Ray ─────────────────────────────────────────────────────────
        "elder_ray" => Box::new(ElderRayStrategy::new(pu(p, "period", 13))),

        // ── Aroon ─────────────────────────────────────────────────────────────
        "aroon_trend" => Box::new(AroonTrend::new(
            pu(p, "period", 25),
            pf64(p, "bull_threshold", 70.0),
            pf64(p, "bear_threshold", 30.0),
        )),

        // ── Chandelier Exit ───────────────────────────────────────────────────
        "chandelier_exit" => Box::new(ChandelierExit::new(
            pu(p, "period", 22),
            pf64(p, "multiplier", 3.0),
        )),

        // ── VWAP ──────────────────────────────────────────────────────────────
        "vwap_bounce" => Box::new(VwapBounce::new(
            pu(p, "rsi_period", 14),
            pf64(p, "oversold", 40.0),
            pf64(p, "overbought", 65.0),
            pu(p, "session_gap_mins", 60) as u64,
        )),
        "vwap_trend" => Box::new(VwapTrend::new(pu(p, "session_gap_mins", 60) as u64)),

        // ── Momentum ──────────────────────────────────────────────────────────
        "momentum_roc" => Box::new(MomentumRoc::new(
            pu(p, "roc_period", 10),
            pu(p, "ema_period", 50),
            pf64(p, "entry_threshold", 2.0),
            pf64(p, "exit_threshold", 0.0),
        )),
        "dual_momentum" => Box::new(DualMomentum::new(
            pu(p, "fast", 10),
            pu(p, "slow", 30),
        )),

        // ── Price Action ──────────────────────────────────────────────────────
        "price_action_swing" => Box::new(PriceActionSwing::new(
            pu(p, "swing_bars", 5),
            pu(p, "ema_trend", 50),
            pu(p, "atr_period", 14),
        )),

        // ── Breakout ──────────────────────────────────────────────────────────
        "orb_breakout" => Box::new(OrbBreakout::new(
            pu(p, "range_bars", 30),
            pu(p, "session_gap_mins", 60),
        )),

        "highest_breakout" => Box::new(HighestBreakout::new(pu(p, "period", 20))),

        "keltner_breakout" => Box::new(KeltnerBreakout::new(
            pu(p, "period", 20),
            pu(p, "atr_period", 10),
            pf64(p, "multiplier", 2.0),
        )),

        // ── MFI ───────────────────────────────────────────────────────────────
        "mfi_trend" => Box::new(MfiTrend::new(
            pu(p, "period", 14),
            pf64(p, "bull_threshold", 50.0),
            pf64(p, "bear_threshold", 40.0),
        )),

        "mfi_revert" => Box::new(MfiRevert::new(
            pu(p, "period", 14),
            pf64(p, "oversold", 20.0),
            pf64(p, "overbought", 80.0),
        )),

        // ── ROC / TRIX ────────────────────────────────────────────────────────
        "roc" | "kst" => Box::new(RocCrossover::new(pu(p, "period", 10))),

        "trix" => Box::new(TrixStrategy::new(pu(p, "period", 18), pu(p, "signal", 9))),

        // ── Alligator ─────────────────────────────────────────────────────────
        "alligator" => Box::new(AlligatorStrategy::new(
            pu(p, "jaw", 13),
            pu(p, "teeth", 8),
            pu(p, "lips", 5),
        )),

        // ── RWI ───────────────────────────────────────────────────────────────
        "rwi" => Box::new(RwiStrategy::new(pu(p, "period", 14), pf64(p, "threshold", 1.0))),

        // ── Pattern breakout ──────────────────────────────────────────────────
        "pattern_breakout" => Box::new(PatternBreakoutStrategy::default_setup(
            pu(p, "window", 60),
        )),

        // ── KAMA ──────────────────────────────────────────────────────────────
        "kama" => Box::new(KamaStrategy::new(
            pu(p, "er_period", 10),
            pu(p, "fast", 2),
            pu(p, "slow", 30),
        )),

        // ── TSI ───────────────────────────────────────────────────────────────
        "tsi" => Box::new(TsiStrategy::new(
            pu(p, "first", 25),
            pu(p, "second", 13),
            pf64(p, "entry_threshold", -25.0),
            pf64(p, "exit_threshold", 25.0),
        )),

        // ── Stochastic RSI ────────────────────────────────────────────────────
        "stoch_rsi" => Box::new(StochRsiStrategy::new(
            pu(p, "period", 14),
            pu(p, "smooth_d", 3),
            pf64(p, "oversold", 0.2),
            pf64(p, "overbought", 0.8),
        )),

        // ── Chop Filter ───────────────────────────────────────────────────────
        "chop_filter" => Box::new(ChopFilterStrategy::new(
            pu(p, "chop_period", 14),
            pu(p, "fast_ema", 8),
            pu(p, "slow_ema", 21),
            pf64(p, "chop_threshold", 61.8),
        )),

        // ── ConnorsRSI ────────────────────────────────────────────────────────
        "connors_rsi" => Box::new(ConnorsRsiStrategy::new(
            pu(p, "rsi_period", 3),
            pu(p, "streak_period", 2),
            pu(p, "rank_period", 100),
            pf64(p, "oversold", 10.0),
            pf64(p, "overbought", 70.0),
        )),

        // ── KDJ (popular in VN / Asian markets) ──────────────────────────────
        "kdj" => Box::new(crate::kdj_strategy::KdjStrategy::new(
            pu(p, "period", 9),
            pu(p, "k_period", 3),
            pu(p, "d_period", 3),
            pf64(p, "oversold", 20.0),
            pf64(p, "overbought", 80.0),
        )),

        // ── Awesome Oscillator ────────────────────────────────────────────────
        "ao" => Box::new(crate::ao_strategy::AoStrategy::new(
            pu(p, "fast", 5),
            pu(p, "slow", 34),
        )),

        // ── TEMA Crossover ────────────────────────────────────────────────────
        "tema_crossover" => Box::new(crate::tema_crossover::TemaCrossover::new(
            pu(p, "fast", 8),
            pu(p, "slow", 21),
        )),

        // ── Williams %R ───────────────────────────────────────────────────────
        "williams_r_ma" => Box::new(WilliamsRMa::new(
            pu(p, "wr_period", 14),
            pu(p, "ema_period", 50),
            pf64(p, "oversold", -80.0),
            pf64(p, "overbought", -20.0),
        )),

        // ── Fisher Transform ──────────────────────────────────────────────────
        "fisher_crossover" => Box::new(FisherCrossover::new(pu(p, "period", 10))),

        // ── Ultimate Oscillator ───────────────────────────────────────────────
        "uo_reversal" => Box::new(UoReversal::new(
            pu(p, "fast", 7),
            pu(p, "medium", 14),
            pu(p, "slow", 28),
            pf64(p, "oversold", 30.0),
            pf64(p, "overbought", 70.0),
        )),

        // ── Chande Momentum Oscillator ────────────────────────────────────────
        "cmo_zero_cross" => Box::new(CmoZeroCross::new(
            pu(p, "cmo_period", 14),
            pu(p, "ema_period", 50),
        )),

        // ── Vortex Indicator ──────────────────────────────────────────────────
        "vortex_trend" => Box::new(VortexTrend::new(pu(p, "period", 14))),

        // ── Chaikin Money Flow ────────────────────────────────────────────────
        "cmf_ema_trend" => Box::new(CmfEmaTrend::new(
            pu(p, "cmf_period", 20),
            pu(p, "ema_period", 50),
            pf64(p, "bull_threshold", 0.1),
            pf64(p, "bear_threshold", 0.1),
        )),

        // ── OBV + EMA ─────────────────────────────────────────────────────────
        "obv_ema_trend" => Box::new(ObvEmaTrend::new(
            pu(p, "obv_ema_period", 20),
            pu(p, "price_ema_period", 50),
        )),

        // ── RSI + EMA Cross ───────────────────────────────────────────────────
        "rsi_ma_cross" => Box::new(RsiMaCross::new(
            pu(p, "fast", 20),
            pu(p, "slow", 50),
            pu(p, "rsi_period", 14),
            pf64(p, "rsi_entry", 50.0),
            pf64(p, "rsi_exit", 45.0),
        )),

        // ── BB + RSI reversal ─────────────────────────────────────────────────
        "bb_rsi_reversal" => Box::new(BbRsiReversal::new(
            pu(p, "bb_period", 20),
            pf64(p, "bb_std", 2.0),
            pu(p, "rsi_period", 14),
            pf64(p, "oversold", 35.0),
            pf64(p, "overbought", 65.0),
        )),

        // ── ADX + EMA Cross ───────────────────────────────────────────────────
        "adx_ema_cross" => Box::new(AdxEmaCross::new(
            pu(p, "fast", 20),
            pu(p, "slow", 50),
            pu(p, "adx_period", 14),
            pf64(p, "adx_threshold", 25.0),
        )),

        // ── LSMA Cross ───────────────────────────────────────────────────────
        "lsma_cross" => Box::new(LsmaCross::new(pu(p, "fast", 20), pu(p, "slow", 50))),

        // ── ALMA Cross ───────────────────────────────────────────────────────
        "alma_cross" => Box::new(AlmaCross::new(
            pu(p, "fast", 9),
            pu(p, "slow", 21),
            pf64(p, "offset", 0.85),
            pf64(p, "sigma", 6.0),
        )),

        // ── VWMA + RSI ────────────────────────────────────────────────────────
        "vwma_rsi" => Box::new(VwmaRsi::new(
            pu(p, "vwma_period", 20),
            pu(p, "rsi_period", 14),
            pf64(p, "rsi_entry", 50.0),
            pf64(p, "rsi_exit", 45.0),
        )),

        // ── SMI Reversal ─────────────────────────────────────────────────────
        "smi_reversal" => Box::new(SmiReversal::new(
            pu(p, "period", 13),
            pu(p, "smooth1", 25),
            pu(p, "smooth2", 2),
            pu(p, "signal_period", 9),
            pf64(p, "oversold", -40.0),
            pf64(p, "overbought", 40.0),
        )),

        // ── PPO Histogram ─────────────────────────────────────────────────────
        "ppo_histogram" => Box::new(PpoHistogram::new(
            pu(p, "fast", 12),
            pu(p, "slow", 26),
            pu(p, "signal", 9),
        )),

        // ── CEL (cel-interpreter, ema(9) function-call syntax) ───────────────
        // params: { "entry": "<cel expr>", "exit": "<cel expr>" }
        "cel" | "cel2" => Box::new(CelStrategy::from_params(p)?),

        // ── Rhai scripting strategy ───────────────────────────────────────────
        // params: { "script": "<rhai script>" }
        "rhai" => Box::new(RhaiStrategy::from_params(p)?),

        // ── Dynamic (declarative JSON conditions) — DEPRECATED ────────────────
        // "dynamic" => Box::new(DynamicStrategy::from_params(p)?),

        // ── Multi-timeframe midpoint ──────────────────────────────────────────
        "pixel_3" => Box::new(Pixel3::with_periods(
            "",
            pu(p, "short", 5),
            pu(p, "medium", 20),
            pu(p, "long", 60),
        )),

        // ── Proprietary / platform-specific ──────────────────────────────────
        "mcdx" => bail!(
            "strategy 'mcdx' uses a proprietary VN indicator (not implemented)"
        ),
        "crypto_rider" => bail!("strategy 'crypto_rider' has no defined formula"),

        other => bail!(
            "unknown strategy '{other}'. \
             See afl/strategy.md for the full list of strategy keys."
        ),
    };

    Ok(s)
}

// ── Indicator dependency declaration ─────────────────────────────────────────

/// A single indicator dependency declared by a strategy.
///
/// Used by `herald::Registry` to acquire live indicator handles from the ledger
/// at registration time. This keeps live indicators warm for the lifetime of
/// the bot — and makes the same values accessible to HTTP / chart overlays.
///
/// # Phase A scope
///
/// Only base-timeframe indicators are declared. Multi-timeframe (MTF) indicators
/// declared in CEL (`H1.ema(200)`) remain
/// internal to the strategy — the ledger does not currently resample bars.
#[derive(Debug, Clone)]
pub struct IndicatorDep {
    /// JSON config accepted by `alm_indicator::IndicatorBox::from_config`.
    /// Example: `{"type":"ema","period":9}` or `{"type":"macd","fast":12,"slow":26,"signal":9}`.
    pub config: Value,
    /// MTF source timeframe. Phase A: always `None`.
    ///
    /// Reserved for when the ledger grows MTF resampling — at that point CEL's
    /// `H1.ema(200)` would emit `source_tf = Some(Timeframe::H1)` and read from
    /// the ledger instead of its internal resampler.
    pub source_tf: Option<alm_core::Timeframe>,
}

/// Extract the indicator dependencies declared by a strategy's params.
///
/// Returns an empty vec for strategies that do not expose their dependency list
/// (all 57 named strategies currently fall into this category — they instantiate
/// indicators internally).
pub fn indicator_deps(name: &str, params: &Value) -> Vec<IndicatorDep> {
    match name {
        "cel" | "cel2" => crate::expr::cel::cel_indicator_deps(params),
        "rhai" => crate::expr::rhai_strategy::rhai_indicator_deps(params),
        // "dynamic" => crate::dynamic::dynamic_indicator_deps(params),  // deprecated
        _ => Vec::new(),
    }
}

/// Build a strategy **and** extract its declared indicator dependencies in one call.
///
/// Used by live systems (herald) that need to register bot + warm corresponding
/// indicators atomically. Backtesting can stick with `build_strategy`.
pub fn build_strategy_with_deps(
    name: &str,
    params: &Value,
) -> Result<(Box<dyn Strategy>, Vec<IndicatorDep>)> {
    let strat = build_strategy(name, params)?;
    let deps = indicator_deps(name, params);
    Ok((strat, deps))
}

/// Convert a `HashMap<String, f64>` (proto ConfigMsg.params) to a flat `Value::Object`.
/// This lets the live herald reuse the same factory.
pub fn params_from_map(map: &std::collections::HashMap<String, f64>) -> Value {
    let obj: serde_json::Map<String, Value> = map
        .iter()
        .map(|(k, v)| (k.clone(), Value::from(*v)))
        .collect();
    Value::Object(obj)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn test_build_all_named_strategies_with_defaults() {
        let strategies = [
            "ma_crossover",
            "triple_ema",
            "hma_crossover",
            "rsi_mean_rev",
            "macd_crossover",
            "macd_ma",
            "waddah_attar",
            "stochastic_crossover",
            "stochastic_dk",
            "range_rover",
            "reversal_catcher",
            "atr_trailing",
            "volatility_squeezer",
            "volatility_vanguard",
            "volatility_ratio",
            "bollinger_macd",
            "bb_squeeze",
            "mean_reversion",
            "bb_keltner_squeeze",
            "dmi_adx",
            "wolfstein",
            "trend_transition",
            "swing_trader",
            "oscillator_overlord",
            "equilibrium_explorer",
            "trend_follower",
            "ma_pullback",
            "cci_reversal",
            "supertrend",
            "supertrend_macd",
            "parabolic_sar",
            "heiken_ashi_color",
            "heiken_ashi_breakout",
            "heiken_ashi_harmonizer",
            "gmma_crossover",
            "ichimoku_cloud",
            "ichimoku_cross",
            "highest_breakout",
            "keltner_breakout",
            "mfi_trend",
            "mfi_revert",
            "roc",
            "kst",
            "trix",
            "alligator",
            "rwi",
            "pattern_breakout",
            "orb_breakout",
            "scalping_ema",
            "dema_crossover",
            "donchian_breakout",
            "elder_ray",
            "aroon_trend",
            "chandelier_exit",
            "vwap_bounce",
            "vwap_trend",
            "momentum_roc",
            "dual_momentum",
            "price_action_swing",
            "pixel_3",
            "kama",
            "tsi",
            "stoch_rsi",
            "chop_filter",
            "connors_rsi",
            "kdj",
            "ao",
            "tema_crossover",
            "williams_r_ma",
            "fisher_crossover",
            "uo_reversal",
            "cmo_zero_cross",
            "vortex_trend",
            "cmf_ema_trend",
            "obv_ema_trend",
            "rsi_ma_cross",
            "bb_rsi_reversal",
            "adx_ema_cross",
            "lsma_cross",
            "alma_cross",
            "vwma_rsi",
            "smi_reversal",
            "ppo_histogram",
        ];
        for name in &strategies {
            let result = build_strategy(name, &json!({}));
            assert!(result.is_ok(), "build_strategy({name}) failed: {:?}", result.err());
        }
    }

    #[test]
    fn test_all_strategies_run_bars_without_panic() {
        use alm_core::bar::Bar;
        let strategies = [
            "ma_crossover", "triple_ema", "hma_crossover", "rsi_mean_rev",
            "macd_crossover", "macd_ma", "waddah_attar", "stochastic_crossover",
            "stochastic_dk", "range_rover", "reversal_catcher", "atr_trailing",
            "volatility_squeezer", "volatility_vanguard", "volatility_ratio",
            "bollinger_macd", "bb_squeeze", "mean_reversion", "bb_keltner_squeeze",
            "dmi_adx", "wolfstein", "trend_transition", "swing_trader",
            "oscillator_overlord", "equilibrium_explorer", "trend_follower",
            "ma_pullback", "cci_reversal", "supertrend", "supertrend_macd",
            "parabolic_sar", "heiken_ashi_color", "heiken_ashi_breakout",
            "heiken_ashi_harmonizer", "gmma_crossover", "ichimoku_cloud",
            "ichimoku_cross", "highest_breakout", "keltner_breakout",
            "mfi_trend", "mfi_revert", "roc", "trix", "alligator", "rwi",
            "pattern_breakout", "orb_breakout", "scalping_ema", "dema_crossover",
            "donchian_breakout", "elder_ray", "aroon_trend", "chandelier_exit",
            "vwap_bounce", "vwap_trend", "momentum_roc", "dual_momentum",
            "price_action_swing", "pixel_3", "kama", "tsi", "stoch_rsi",
            "chop_filter", "connors_rsi", "kdj", "ao", "tema_crossover",
            "williams_r_ma", "fisher_crossover", "uo_reversal", "cmo_zero_cross",
            "vortex_trend", "cmf_ema_trend", "obv_ema_trend", "rsi_ma_cross",
            "bb_rsi_reversal", "adx_ema_cross", "lsma_cross", "alma_cross",
            "vwma_rsi", "smi_reversal", "ppo_histogram",
        ];

        // Trending-up then trending-down synthetic price series (200 bars)
        let prices: Vec<f64> = (0..200)
            .map(|i| if i < 100 { 100.0 + i as f64 * 0.5 } else { 150.0 - (i - 100) as f64 * 0.5 })
            .collect();

        for name in &strategies {
            let mut s = build_strategy(name, &json!({}))
                .unwrap_or_else(|e| panic!("build {name}: {e}"));
            for (i, &c) in prices.iter().enumerate() {
                let bar = Bar::new(
                    i as i64 * 60_000,
                    "TEST",
                    c, c * 1.005, c * 0.995, c,
                    1000.0 + (i % 10) as f64 * 100.0,
                );
                let _ = s.on_bar(&bar); // must not panic
            }
            s.reset();
            // After reset, first bar must not crash
            let bar = Bar::new(0, "TEST", 100.0, 101.0, 99.0, 100.0, 1000.0);
            let _ = s.on_bar(&bar);
        }
    }

    #[test]
    fn test_build_unknown_strategy_errors() {
        assert!(build_strategy("totally_unknown_xyz", &json!({})).is_err());
    }

    #[test]
    fn test_build_proprietary_strategies_error() {
        assert!(build_strategy("mcdx", &json!({})).is_err());
        assert!(build_strategy("crypto_rider", &json!({})).is_err());
    }

    #[test]
    fn test_params_from_map_roundtrip() {
        let mut map = std::collections::HashMap::new();
        map.insert("fast".into(), 12.0_f64);
        map.insert("slow".into(), 26.0_f64);
        let v = params_from_map(&map);
        assert_eq!(pf64(&v, "fast", 0.0), 12.0);
        assert_eq!(pf64(&v, "slow", 0.0), 26.0);
    }

    // ── indicator_deps ───────────────────────────────────────────────────────

    /// Extract the `"type"` field from each dep config, sorted, for stable asserts.
    fn dep_types(deps: &[IndicatorDep]) -> Vec<String> {
        let mut t: Vec<String> = deps.iter()
            .filter_map(|d| d.config.get("type").and_then(Value::as_str).map(str::to_string))
            .collect();
        t.sort();
        t
    }

    #[test]
    fn named_strategies_declare_no_deps() {
        // Named strategies own their indicators internally and must not leak
        // dependencies to the ledger (would double-allocate).
        let (_, deps) = build_strategy_with_deps(
            "ma_crossover",
            &json!({"fast": 10, "slow": 30}),
        ).unwrap();
        assert!(deps.is_empty(), "named strategy must declare zero deps: {deps:?}");
    }

    // #[test]  // deprecated — DynamicStrategy removed
    // fn dynamic_declares_base_tf_deps_only() { ... }

    #[test]
    fn cel_extracts_deps_from_entry_and_exit() {
        let params = json!({
            "entry": "rsi(14) < 30 && close > ema(50)",
            "exit":  "rsi(14) > 70 || macd_hist(12) < 0"
        });
        let (_, deps) = build_strategy_with_deps("cel", &params).unwrap();
        // Expect: rsi, ema, macd (dedup — one rsi even though used twice).
        assert_eq!(dep_types(&deps), vec!["ema", "macd", "rsi"]);
    }

    #[test]
    fn cel_skips_mtf_indicators() {
        let params = json!({
            "entry": "H1.ema(200) > close && rsi(14) < 30",
            "exit":  "rsi(14) > 70"
        });
        let (_, deps) = build_strategy_with_deps("cel", &params).unwrap();
        // Only base-TF rsi. H1.ema(200) is MTF → skipped.
        assert_eq!(dep_types(&deps), vec!["rsi"]);
    }

    #[test]
    fn cel_dedups_same_indicator() {
        let params = json!({
            "entry": "ema(9) > ema(21) && close > ema(9)",
            "exit":  "ema(9) < ema(21)"
        });
        let (_, deps) = build_strategy_with_deps("cel", &params).unwrap();
        // ema(9) and ema(21) are two distinct specs — ema(9) used 3× collapses to one.
        assert_eq!(deps.len(), 2);
        let periods: Vec<i64> = deps.iter()
            .filter_map(|d| d.config.get("period").and_then(Value::as_i64))
            .collect();
        let mut p = periods; p.sort();
        assert_eq!(p, vec![9, 21]);
    }

    #[test]
    fn cel_script_form_extracts_deps() {
        let params = json!({
            "script": "entry: rsi(14) < 30\nexit: rsi(14) > 70"
        });
        let (_, deps) = build_strategy_with_deps("cel", &params).unwrap();
        assert_eq!(dep_types(&deps), vec!["rsi"]);
    }
}
