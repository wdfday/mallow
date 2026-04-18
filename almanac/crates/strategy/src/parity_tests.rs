//! Parity tests — same logic expressed 3 ways must produce identical signals.
//!
//! For each strategy we run:
//!   1. Hardcoded struct  (e.g. `MaCrossover`)
//!   2. DynamicStrategy   (declarative JSON)
//!   3. CelStrategy       (text expression with `prev_` for cross detection)
//!
//! Same bars → same (timestamp, direction) sequence.

#[cfg(test)]
mod parity_tests {
    use alm_core::{bar::Bar, signal::Direction, strategy::Strategy};
    use serde_json::json;

    use crate::{
        factory::build_strategy,
        adx_ema_cross::AdxEmaCross,
        alma_cross::AlmaCross,
        alligator_strategy::AlligatorStrategy,
        ao_strategy::AoStrategy,
        aroon_strategy::AroonTrend,
        bb_rsi_reversal::BbRsiReversal,
        bollinger_macd::BollingerMacd,
        cci_reversal::CciReversal,
        chop_filter_strategy::ChopFilterStrategy,
        cmf_ema_trend::CmfEmaTrend,
        cmo_zero_cross::CmoZeroCross,
        connors_rsi_strategy::ConnorsRsiStrategy,
        dema_crossover::DemaCrossover,
        dmi_adx::DmiAdx,
        donchian_breakout::DonchianBreakout,
        elder_ray_strategy::ElderRayStrategy,
        equilibrium_explorer::EquilibriumExplorer,
        fisher_crossover::FisherCrossover,
        hma_crossover::HmaCrossover,
        ichimoku_strategy::{IchimokuCloud, IchimokuCross},
        kama_strategy::KamaStrategy,
        kdj_strategy::KdjStrategy,
        lsma_cross::LsmaCross,
        ma_crossover::MaCrossover,
        macd_crossover::MacdCrossover,
        macd_ma::MacdMa,
        mfi_strategy::{MfiRevert, MfiTrend},
        momentum_roc::{DualMomentum, MomentumRoc},
        obv_ema_trend::ObvEmaTrend,
        oscillator_overlord::OscillatorOverlord,
        ppo_histogram::PpoHistogram,
        range_rover::RangeRover,
        reversal_catcher::ReversalCatcher,
        roc_strategy::RocCrossover,
        rsi_ma_cross::RsiMaCross,
        rsi_mean_rev::RsiMeanRev,
        rwi_strategy::RwiStrategy,
        sar_strategy::SarStrategy,
        smi_reversal::SmiReversal,
        stoch_rsi_strategy::StochRsiStrategy,
        stochastic::StochasticCrossover,
        stochastic_dk::StochasticDk,
        supertrend_strategy::SupertrendMacd,
        swing_trader::SwingTrader,
        tema_crossover::TemaCrossover,
        trend_follower::TrendFollower,
        trend_transition::TrendTransition,
        triple_ema::TripleEma,
        trix_strategy::TrixStrategy,
        tsi_strategy::TsiStrategy,
        uo_reversal::UoReversal,
        volatility_breakout::VolatilityRatioBreakout,
        vortex_trend::VortexTrend,
        vwap_strategy::{VwapBounce, VwapTrend},
        vwma_rsi::VwmaRsi,
        williams_r_ma::WilliamsRMa,
        wolfstein::Wolfstein,
    };

    // ── helpers ───────────────────────────────────────────────────────────────

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "TEST", close, close * 1.005, close * 0.995, close, 1000.0)
    }

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter()
            .flat_map(|b| s.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect()
    }

    /// down → up → down to force two crossovers (entry + exit)
    fn trending_bars(n: usize) -> Vec<Bar> {
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
    fn rsi_bars(n: usize) -> Vec<Bar> {
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
    fn stoch_rsi_bars() -> Vec<Bar> {
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
    fn connors_rsi_bars() -> Vec<Bar> {
        let mut ts = 0i64;
        let mut bars = vec![];
        for _ in 0..120 { bars.push(bar(ts, 100.0)); ts += 60_000; }
        for i in 0..20u32 { bars.push(bar(ts, (100.0 - i as f64 * 4.0).max(1.0))); ts += 60_000; }
        for i in 0..30u32 { bars.push(bar(ts, 20.0 + i as f64 * 5.0)); ts += 60_000; }
        bars
    }

    /// Clear down→up→down for ParabolicSAR flips.
    fn sar_bars() -> Vec<Bar> {
        let mut ts = 0i64;
        let mut bars = vec![];
        for i in 0..50u32 { bars.push(bar(ts, (200.0 - i as f64 * 3.0).max(5.0))); ts += 60_000; }
        for i in 0..80u32 { bars.push(bar(ts, 50.0 + i as f64 * 4.0)); ts += 60_000; }
        for i in 0..50u32 { bars.push(bar(ts, (370.0 - i as f64 * 4.0).max(5.0))); ts += 60_000; }
        bars
    }

    fn assert_parity(label: &str, a: &[(i64, Direction)], b: &[(i64, Direction)]) {
        assert_eq!(
            a, b,
            "{label}: signal mismatch\n  left : {a:?}\n  right: {b:?}"
        );
    }

    // ── RSI mean reversion ────────────────────────────────────────────────────

    #[test]
    fn rsi_mean_rev_parity() {
        let bars = rsi_bars(80);

        // 1. hardcoded
        let mut hc = RsiMeanRev::new(14, 30.0, 70.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "rsi": { "type": "rsi", "period": 14 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 70.0 }]
            }
        }))
        .unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "rsi(14) < 30.0",
            "exit":  "rsi(14) > 70.0"
        }))
        .unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "rsi: hardcoded produced no signals");
        assert_parity("rsi hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("rsi hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── MA crossover ──────────────────────────────────────────────────────────

    #[test]
    fn ma_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=20, slow=50, both EMA)
        let mut hc = MaCrossover::new(20, 50);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 },
                "slow": { "type": "ema", "period": 50 }
            },
            "entry": {
                "logic": "and",
                "rules": [{
                    "source": "fast", "field": "value",
                    "op": "cross_above",
                    "compare": "slow", "compare_field": "value"
                }]
            },
            "exit": {
                "logic": "and",
                "rules": [{
                    "source": "fast", "field": "value",
                    "op": "cross_below",
                    "compare": "slow", "compare_field": "value"
                }]
            }
        }))
        .unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL with prev_
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)",
            "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
        }))
        .unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ma_crossover: hardcoded produced no signals");
        assert_parity("ma hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("ma hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── MACD crossover ────────────────────────────────────────────────────────

    #[test]
    fn macd_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=12, slow=26, signal=9)
        let mut hc = MacdCrossover::new(12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — histogram cross_above/below 0
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "macd", "field": "histogram", "op": "cross_above", "value": 0.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "macd", "field": "histogram", "op": "cross_below", "value": 0.0 }]
            }
        }))
        .unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL with prev_
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0",
            "exit":  "prev_macd_hist(12) >= 0.0 && macd_hist(12) < 0.0"
        }))
        .unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "macd: hardcoded produced no signals");
        assert_parity("macd hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("macd hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── CCI reversal ─────────────────────────────────────────────────────────

    #[test]
    fn cci_reversal_parity() {
        let bars = rsi_bars(80); // strong V-shape pushes CCI through ±100

        // 1. hardcoded (entry cross_above -100, exit cross_above +100)
        let mut hc = CciReversal::new(14, -100.0, 100.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "cci": { "type": "cci", "period": 14 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "cci", "field": "value", "op": "cross_above", "value": -100.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "cci", "field": "value", "op": "cross_above", "value": 100.0 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_cci(14) <= -100.0 && cci(14) > -100.0",
            "exit":  "prev_cci(14) <= 100.0 && cci(14) > 100.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "cci_reversal: hardcoded produced no signals");
        assert_parity("cci hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("cci hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── ROC zero-cross ────────────────────────────────────────────────────────

    #[test]
    fn roc_crossover_parity() {
        let bars = trending_bars(200);

        // 1. hardcoded (period=10)
        let mut hc = RocCrossover::new(10);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "roc": { "type": "roc", "period": 10 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "roc", "field": "value", "op": "cross_above", "value": 0.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "roc", "field": "value", "op": "cross_below", "value": 0.0 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_roc(10) <= 0.0 && roc(10) > 0.0",
            "exit":  "prev_roc(10) >= 0.0 && roc(10) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "roc: hardcoded produced no signals");
        assert_parity("roc hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("roc hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── MFI trend ─────────────────────────────────────────────────────────────

    #[test]
    fn mfi_trend_parity() {
        // Three-phase bars: down → up → down gives MFI entry + exit
        let bars = trending_bars(200);

        // 1. hardcoded (bull_threshold=50, bear_threshold=40)
        let mut hc = MfiTrend::new(14, 50.0, 40.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — entry: cross_above 50; exit: lt 40
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "mfi": { "type": "mfi", "period": 14 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "mfi", "field": "value", "op": "cross_above", "value": 50.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "mfi", "field": "value", "op": "lt", "value": 40.0 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_mfi(14) <= 50.0 && mfi(14) > 50.0",
            "exit":  "mfi(14) < 40.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "mfi_trend: hardcoded produced no signals");
        assert_parity("mfi hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("mfi hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── SuperTrend (dynamic vs CEL only) ─────────────────────────────────────
    //
    // The hardcoded SupertrendStrategy skips the first ready bar (uses Option<bool>
    // for prev_bullish), so it never signals on the warm-up transition.
    // Dynamic and CEL both treat the missing-prev snapshot as 0.0, which means
    // they can fire on the first ready bar when close > lower_band (always true
    // with symmetric high/low bars).  The two approaches are internally consistent
    // with each other but differ from the hardcoded's "requires an actual flip"
    // semantics.  We verify dynamic ↔ CEL parity here.

    #[test]
    fn supertrend_dyn_cel_parity() {
        let bars = trending_bars(300);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "st": { "type": "supertrend", "period": 10, "multiplier": 3.0 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "st", "field": "bullish", "op": "cross_above", "value": 0.5 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "st", "field": "bullish", "op": "cross_below", "value": 0.5 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_st_bull(10) < 1.0 && st_bull(10) >= 1.0",
            "exit":  "prev_st_bull(10) >= 1.0 && st_bull(10) < 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!dyn_sigs.is_empty(), "supertrend: dynamic produced no signals");
        assert_parity("supertrend dynamic vs cel", &dyn_sigs, &cel_sigs);
    }

    // ── DMI / ADX ─────────────────────────────────────────────────────────────

    #[test]
    fn dmi_adx_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (period=14, adx_threshold=25.0)
        let mut hc = DmiAdx::new(14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — two-rule entry: +DI cross_above −DI AND adx > 25
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "adx": { "type": "adx", "period": 14 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "adx", "field": "plus_di",  "op": "cross_above", "compare": "adx", "compare_field": "minus_di" },
                    { "source": "adx", "field": "adx",      "op": "gt",          "value": 25.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [
                    { "source": "adx", "field": "minus_di", "op": "cross_above", "compare": "adx", "compare_field": "plus_di" }
                ]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_plus_di(14) <= prev_minus_di(14) && plus_di(14) > minus_di(14) && adx(14) > 25.0",
            "exit":  "prev_minus_di(14) <= prev_plus_di(14) && minus_di(14) > plus_di(14)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "dmi_adx: hardcoded produced no signals");
        assert_parity("dmi_adx hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("dmi_adx hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Aroon Trend ───────────────────────────────────────────────────────────

    #[test]
    fn aroon_trend_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (period=25, bull=70, bear=30)
        let mut hc = AroonTrend::new(25, 70.0, 30.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — entry: up>70 AND down<30; exit: up<down (plain lt, no cross)
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "aroon": { "type": "aroon", "period": 25 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "aroon", "field": "up",   "op": "gt", "value": 70.0 },
                    { "source": "aroon", "field": "down",  "op": "lt", "value": 30.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [
                    { "source": "aroon", "field": "up", "op": "lt",
                      "compare": "aroon", "compare_field": "down" }
                ]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "aroon_up(25) > 70.0 && aroon_down(25) < 30.0",
            "exit":  "aroon_up(25) < aroon_down(25)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "aroon_trend: hardcoded produced no signals");
        assert_parity("aroon hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("aroon hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Stochastic Crossover ──────────────────────────────────────────────────

    #[test]
    fn stochastic_crossover_parity() {
        // V-shape pushes stochastic into oversold then overbought
        let bars = rsi_bars(150);

        // 1. hardcoded (k=14, d=3, oversold=20, overbought=80)
        let mut hc = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — K cross_above D while D<20; exit K cross_below D while D>80
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "stoch", "field": "k", "op": "cross_above",
                      "compare": "stoch", "compare_field": "d" },
                    { "source": "stoch", "field": "d", "op": "lt", "value": 20.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [
                    { "source": "stoch", "field": "k", "op": "cross_below",
                      "compare": "stoch", "compare_field": "d" },
                    { "source": "stoch", "field": "d", "op": "gt", "value": 80.0 }
                ]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_stoch_k(14) <= prev_stoch_d(14) && stoch_k(14) > stoch_d(14) && stoch_d(14) < 20.0",
            "exit":  "prev_stoch_k(14) >= prev_stoch_d(14) && stoch_k(14) < stoch_d(14) && stoch_d(14) > 80.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "stochastic_crossover: hardcoded produced no signals");
        assert_parity("stoch hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("stoch hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── TRIX ─────────────────────────────────────────────────────────────────
    // NOTE: trix functions not yet in CEL — dynamic vs hardcoded only.

    #[test]
    fn trix_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (period=18, signal=9)
        let mut hc = TrixStrategy::new(18, 9);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — histogram cross_above/below 0
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "trix": { "type": "trix", "period": 18, "signal": 9 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "trix", "field": "histogram", "op": "cross_above", "value": 0.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "trix", "field": "histogram", "op": "cross_below", "value": 0.0 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "trix: hardcoded produced no signals");
        assert_parity("trix hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
    }

    // ── TEMA Crossover ────────────────────────────────────────────────────────

    #[test]
    fn tema_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=10, slow=25)
        let mut hc = TemaCrossover::new(10, 25);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "tema", "period": 10 },
                "slow": { "type": "tema", "period": 25 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_above",
                            "compare": "slow", "compare_field": "value" }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_below",
                            "compare": "slow", "compare_field": "value" }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_tema(10) <= prev_tema(25) && tema(10) > tema(25)",
            "exit":  "prev_tema(10) >= prev_tema(25) && tema(10) < tema(25)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "tema_crossover: hardcoded produced no signals");
        assert_parity("tema hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("tema hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── HMA Crossover ─────────────────────────────────────────────────────────

    #[test]
    fn hma_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=9, slow=21)
        let mut hc = HmaCrossover::new(9, 21);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "hma", "period": 9 },
                "slow": { "type": "hma", "period": 21 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_above",
                            "compare": "slow", "compare_field": "value" }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_below",
                            "compare": "slow", "compare_field": "value" }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_hma(9) <= prev_hma(21) && hma(9) > hma(21)",
            "exit":  "prev_hma(9) >= prev_hma(21) && hma(9) < hma(21)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "hma_crossover: hardcoded produced no signals");
        assert_parity("hma hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("hma hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── DEMA Crossover ────────────────────────────────────────────────────────

    #[test]
    fn dema_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=10, slow=25)
        let mut hc = DemaCrossover::new(10, 25);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "dema", "period": 10 },
                "slow": { "type": "dema", "period": 25 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_above",
                            "compare": "slow", "compare_field": "value" }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_below",
                            "compare": "slow", "compare_field": "value" }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_dema(10) <= prev_dema(25) && dema(10) > dema(25)",
            "exit":  "prev_dema(10) >= prev_dema(25) && dema(10) < dema(25)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "dema_crossover: hardcoded produced no signals");
        assert_parity("dema hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("dema hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── reset parity ──────────────────────────────────────────────────────────
    // After reset(), all 3 must reproduce the same signals on a second run.

    #[test]
    fn reset_parity() {
        let bars = rsi_bars(80);

        let mut hc  = RsiMeanRev::new(14, 30.0, 70.0);
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }] },
            "exit":  { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 70.0 }] }
        })).unwrap();
        let mut cel = build_strategy("cel", &json!({
            "entry": "rsi(14) < 30.0",
            "exit":  "rsi(14) > 70.0"
        })).unwrap();

        // first run
        let hc_r1  = run(&mut hc,        &bars);
        let dyn_r1 = run(dyn_s.as_mut(), &bars);
        let cel_r1 = run(cel.as_mut(),   &bars);

        // reset + second run
        hc.reset();  dyn_s.reset();  cel.reset();
        let hc_r2  = run(&mut hc,        &bars);
        let dyn_r2 = run(dyn_s.as_mut(), &bars);
        let cel_r2 = run(cel.as_mut(),   &bars);

        assert_parity("rsi reset: hardcoded",  &hc_r1,  &hc_r2);
        assert_parity("rsi reset: dynamic",    &dyn_r1, &dyn_r2);
        assert_parity("rsi reset: cel",        &cel_r1, &cel_r2);
        assert_parity("rsi reset: hc vs dyn",  &hc_r1,  &dyn_r2);
        assert_parity("rsi reset: hc vs cel",  &hc_r1,  &cel_r2);
    }

    // ── StochRSI ──────────────────────────────────────────────────────────────
    // entry: K drops below oversold (cross_below); exit: K rises above overbought (cross_above)

    #[test]
    fn stoch_rsi_parity() {
        let bars = stoch_rsi_bars();

        // StochasticRsi returns K in [0,1] — thresholds must be 0.2/0.8.
        let mut hc = StochRsiStrategy::new(14, 3, 0.2, 0.8);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "srsi": { "type": "stoch_rsi", "rsi_period": 14, "smooth_d": 3 } },
            "entry": { "logic": "and", "rules": [
                { "source": "srsi", "field": "k", "op": "cross_below", "value": 0.2 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "srsi", "field": "k", "op": "cross_above", "value": 0.8 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_srsi_k(14) >= 0.2 && srsi_k(14) < 0.2",
            "exit":  "prev_srsi_k(14) <= 0.8 && srsi_k(14) > 0.8"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "stoch_rsi: no signals");
        assert_parity("stoch_rsi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("stoch_rsi hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── TSI ───────────────────────────────────────────────────────────────────
    // entry: cross_above entry_threshold; exit: cross_below exit_threshold

    #[test]
    fn tsi_parity() {
        let bars = trending_bars(300);

        let mut hc = TsiStrategy::new(25, 13, 0.0, 0.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "tsi": { "type": "tsi", "first": 25, "second": 13 } },
            "entry": { "logic": "and", "rules": [
                { "source": "tsi", "field": "value", "op": "cross_above", "value": 0.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "tsi", "field": "value", "op": "cross_below", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_tsi(25) <= 0.0 && tsi(25) > 0.0",
            "exit":  "prev_tsi(25) >= 0.0 && tsi(25) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "tsi: no signals");
        assert_parity("tsi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("tsi hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── ConnorsRSI ────────────────────────────────────────────────────────────
    // entry: lt oversold; exit: gt overbought (no cross needed)
    // ConnorsRSI rank_period=100 needs 100+ bars to warm up — use wider thresholds

    #[test]
    fn connors_rsi_parity() {
        let bars = connors_rsi_bars();

        let mut hc = ConnorsRsiStrategy::new(3, 2, 100, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "crsi": { "type": "connors_rsi",
                "rsi_period": 3, "streak_period": 2, "rank_period": 100 } },
            "entry": { "logic": "and", "rules": [
                { "source": "crsi", "field": "value", "op": "lt", "value": 20.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "crsi", "field": "value", "op": "gt", "value": 80.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "connors_rsi(3) < 20.0",
            "exit":  "connors_rsi(3) > 80.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "connors_rsi: no signals");
        assert_parity("connors_rsi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("connors_rsi hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Donchian Breakout ─────────────────────────────────────────────────────
    // entry: close cross_above prev upper; exit: close cross_below prev lower

    #[test]
    fn donchian_breakout_parity() {
        let bars = trending_bars(300);

        let mut hc = DonchianBreakout::new(20, 10);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "don_en": { "type": "donchian", "period": 20 },
                "don_ex": { "type": "donchian", "period": 10 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt",
                  "compare": "don_en", "compare_field": "upper" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt",
                  "compare": "don_ex", "compare_field": "lower" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // NOTE: hardcoded uses prev-bar upper to avoid look-ahead; dynamic/CEL use current bar.
        // They will differ — verify dynamic vs CEL parity only.
        assert_parity("donchian dynamic vs cel", &dyn_sigs, &{
            let mut cel = build_strategy("cel", &json!({
                "entry": "close > donchian_upper(20)",
                "exit":  "close < donchian_lower(10)"
            })).unwrap();
            run(cel.as_mut(), &bars)
        });
        assert!(!hc_sigs.is_empty(), "donchian: no signals");
    }

    // ── KAMA Strategy ─────────────────────────────────────────────────────────
    // entry: close cross_above KAMA; exit: close cross_below KAMA
    //
    // NOTE: DynamicStrategy "source":"close" reads bar_fields (current bar only),
    // no previous value is tracked → cross_above vs indicator is not expressible.
    // Verified: hardcoded vs CEL only; dynamic vs CEL separately.

    #[test]
    fn kama_parity() {
        let bars = trending_bars(300);

        let mut hc = KamaStrategy::new(10, 2, 30);
        let hc_sigs = run(&mut hc, &bars);

        // ema(1) = close exactly (alpha=1), so prev_ema(1) = prev_close.
        // CEL tracks prev_ only for indicator bindings, not raw bar fields.
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(1) <= prev_kama(10) && ema(1) > kama(10)",
            "exit":  "prev_ema(1) >= prev_kama(10) && ema(1) < kama(10)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "kama: no signals");
        assert_parity("kama hc vs cel", &hc_sigs, &cel_sigs);
    }

    #[test]
    fn kama_dynamic_cel_parity() {
        // Dynamic can approximate: close > kama (level check, not cross) for entry/exit
        let bars = trending_bars(300);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "kama": { "type": "kama", "er_period": 10, "fast": 2, "slow": 30 } },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt", "compare": "kama" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt", "compare": "kama" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > kama(10)",
            "exit":  "close < kama(10)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert_parity("kama dynamic vs cel (level)", &dyn_sigs, &cel_sigs);
    }

    // ── SAR Strategy ──────────────────────────────────────────────────────────
    // entry: SAR flips bullish; exit: SAR flips bearish
    //
    // NOTE: DynamicStrategy uses prev_bullish = 0.0 (default f64) before the first
    // SAR output, so on bar 1 it sees a phantom cross (0→1) and fires a spurious
    // Long + Close pair. HC uses Option<bool> and skips the first output.
    // CEL uses prev_sar(1) = 0.0 initially; `prev_close < 0` is always false →
    // the phantom cross does NOT fire in CEL. HC vs CEL match correctly.

    #[test]
    fn sar_parity() {
        let bars = sar_bars();

        let mut hc = SarStrategy::new(0.02, 0.2);
        let hc_sigs = run(&mut hc, &bars);

        // CEL: express flip via price crossing SAR price level.
        // prev_sar(1) = 0.0 on bar 0 (no output yet) → prev_close < 0 = false → no phantom.
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(1) < prev_sar(1) && ema(1) > sar(1)",
            "exit":  "prev_ema(1) > prev_sar(1) && ema(1) < sar(1)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "sar: no signals");
        assert_parity("sar hc vs cel", &hc_sigs, &cel_sigs);

        // Dynamic uses `bullish` field (0.0/1.0) with cross_above/below.
        // It sees prev_bullish=0.0 at bar 0 (default) and fires a spurious Long+Close
        // on bars 1–2 due to the initialization. Verify it at least agrees with itself.
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "sar": { "type": "parabolic_sar", "step": 0.02, "max": 0.2 } },
            "entry": { "logic": "and", "rules": [
                { "source": "sar", "field": "bullish", "op": "cross_above", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "sar", "field": "bullish", "op": "cross_below", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);
        // Dynamic has 2 extra init signals; the rest should include HC's signals.
        assert!(dyn_sigs.len() >= hc_sigs.len(), "sar dynamic should have at least as many signals as hc");
        let dyn_tail: Vec<_> = dyn_sigs.iter().skip(dyn_sigs.len() - hc_sigs.len()).cloned().collect();
        assert_parity("sar hc matches tail of dynamic", &hc_sigs, &dyn_tail);
    }

    // ── KDJ Strategy ──────────────────────────────────────────────────────────
    // entry: K<oversold AND D<oversold AND K rising; exit: K>overbought OR J>100
    //
    // NOTE: "K rising" (current K > prev K) can't be expressed as a cross in dynamic
    // since it compares the same field to itself. Verified: hardcoded vs CEL only.
    // Dynamic tests the exit condition and the K/D < oversold entry rules.

    #[test]
    fn kdj_hc_vs_cel_parity() {
        // Prepend 40 flat bars so the KDJ indicator warms up (prev_kdj_k ≈ 50, not 0)
        // before the V-shape begins. This prevents CEL from firing spuriously on the
        // very first output bar where prev_kdj_k(9) would default to 0.0.
        let warmup: Vec<Bar> = (0..40).map(|i| bar(i as i64 * 60_000, 100.0)).collect();
        let offset = warmup.len() as i64 * 60_000;
        let v_shape: Vec<Bar> = rsi_bars(200)
            .into_iter()
            .map(|b| bar(b.timestamp + offset, b.close))
            .collect();
        let bars: Vec<Bar> = warmup.into_iter().chain(v_shape).collect();

        let mut hc = KdjStrategy::new(9, 3, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "kdj_k(9) < 20.0 && kdj_d(9) < 20.0 && kdj_k(9) > prev_kdj_k(9)",
            "exit":  "kdj_k(9) > 80.0 || kdj_j(9) > 100.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "kdj: no signals");
        assert_parity("kdj hc vs cel", &hc_sigs, &cel_sigs);
    }

    #[test]
    fn kdj_dynamic_cel_parity() {
        // Dynamic can express K<oversold, D<oversold, exit K>overbought OR J>100
        // "K rising" approximated as K cross_above D (different but testable)
        let bars = rsi_bars(200);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "kdj": { "type": "kdj", "period": 9, "k_period": 3, "d_period": 3 } },
            "entry": { "logic": "and", "rules": [
                { "source": "kdj", "field": "k", "op": "lt", "value": 50.0 },
                { "source": "kdj", "field": "d", "op": "lt", "value": 50.0 },
                { "source": "kdj", "field": "k", "op": "cross_above",
                  "compare": "kdj", "compare_field": "d" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "kdj", "field": "k", "op": "gt", "value": 80.0 },
                { "source": "kdj", "field": "j", "op": "gt", "value": 100.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "kdj_k(9) < 50.0 && kdj_d(9) < 50.0 && prev_kdj_k(9) <= prev_kdj_d(9) && kdj_k(9) > kdj_d(9)",
            "exit":  "kdj_k(9) > 80.0 || kdj_j(9) > 100.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert_parity("kdj dynamic vs cel", &dyn_sigs, &cel_sigs);
    }

    // ── Triple EMA ────────────────────────────────────────────────────────────
    // entry: ema1 crosses above ema2 WITH ema2 > ema3; exit: ema1 crosses below ema2

    #[test]
    fn triple_ema_parity() {
        let bars = trending_bars(300);

        let mut hc = TripleEma::new(10, 20, 50);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "e1": { "type": "ema", "period": 10 },
                "e2": { "type": "ema", "period": 20 },
                "e3": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "e1", "field": "value", "op": "cross_above",
                  "compare": "e2", "compare_field": "value" },
                { "source": "e2", "field": "value", "op": "gt",
                  "compare": "e3", "compare_field": "value" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "e1", "field": "value", "op": "cross_below",
                  "compare": "e2", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(10) <= prev_ema(20) && ema(10) > ema(20) && ema(20) > ema(50)",
            "exit":  "prev_ema(10) >= prev_ema(20) && ema(10) < ema(20)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "triple_ema: no signals");
        assert_parity("triple_ema hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("triple_ema hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Stochastic D/K pure cross ─────────────────────────────────────────────
    // entry: K crosses above D (no zone); exit: K crosses below D

    #[test]
    fn stochastic_dk_parity() {
        let bars = rsi_bars(200);

        let mut hc = StochasticDk::new(14, 3);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 } },
            "entry": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k", "op": "cross_above",
                  "compare": "stoch", "compare_field": "d" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k", "op": "cross_below",
                  "compare": "stoch", "compare_field": "d" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_stoch_k(14) <= prev_stoch_d(14) && stoch_k(14) > stoch_d(14)",
            "exit":  "prev_stoch_k(14) >= prev_stoch_d(14) && stoch_k(14) < stoch_d(14)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "stochastic_dk: no signals");
        assert_parity("stochastic_dk hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("stochastic_dk hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── MACD + MA filter ──────────────────────────────────────────────────────
    // entry: hist crosses above 0 AND close > sma; exit: hist crosses below 0 OR close < sma

    #[test]
    fn macd_ma_parity() {
        let bars = trending_bars(300);

        let mut hc = MacdMa::new(12, 26, 9, 50);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 },
                "ma":   { "type": "sma",  "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "macd", "field": "histogram", "op": "cross_above", "value": 0.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ma" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "macd", "field": "histogram", "op": "cross_below", "value": 0.0 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ma" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0 && close > sma(50)",
            "exit":  "(prev_macd_hist(12) >= 0.0 && macd_hist(12) < 0.0) || close < sma(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "macd_ma: no signals");
        assert_parity("macd_ma hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("macd_ma hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Wolfstein ─────────────────────────────────────────────────────────────
    // entry: adx > 27.5 AND +DI > -DI; exit: adx < 20.5

    #[test]
    fn wolfstein_parity() {
        let bars = trending_bars(300);

        let mut hc = Wolfstein::new(14, 27.5, 20.5);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "adx": { "type": "adx", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "adx", "field": "adx",      "op": "gt", "value": 27.5 },
                { "source": "adx", "field": "plus_di",  "op": "gt",
                  "compare": "adx", "compare_field": "minus_di" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "adx", "field": "adx", "op": "lt", "value": 20.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "adx(14) > 27.5 && plus_di(14) > minus_di(14)",
            "exit":  "adx(14) < 20.5"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "wolfstein: no signals");
        assert_parity("wolfstein hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("wolfstein hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Trend Follower ────────────────────────────────────────────────────────
    // entry: sma cross AND macd_hist > 0; exit: cross back OR macd_hist < 0

    #[test]
    fn trend_follower_parity() {
        let bars = trending_bars(300);

        let mut hc = TrendFollower::new(50, 200, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "sma", "period": 50 },
                "slow": { "type": "sma", "period": 200 },
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "macd", "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" },
                { "source": "macd", "field": "histogram", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_sma(50) <= prev_sma(200) && sma(50) > sma(200) && macd_hist(12) > 0.0",
            "exit":  "(prev_sma(50) >= prev_sma(200) && sma(50) < sma(200)) || macd_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "trend_follower: no signals");
        assert_parity("trend_follower hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("trend_follower hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Trend Transition ──────────────────────────────────────────────────────
    // entry: ema cross AND adx > threshold; exit: ema crosses back

    #[test]
    fn trend_transition_parity() {
        let bars = trending_bars(300);

        let mut hc = TrendTransition::new(50, 200, 14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 50 },
                "slow": { "type": "ema", "period": 200 },
                "adx":  { "type": "adx", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "adx", "field": "adx", "op": "gt", "value": 25.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(50) <= prev_ema(200) && ema(50) > ema(200) && adx(14) > 25.0",
            "exit":  "prev_ema(50) >= prev_ema(200) && ema(50) < ema(200)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "trend_transition: no signals");
        assert_parity("trend_transition hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("trend_transition hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Swing Trader ──────────────────────────────────────────────────────────
    // entry: CCI crosses above 100 AND adx > threshold; exit: CCI crosses below -100

    #[test]
    fn swing_trader_parity() {
        let bars = trending_bars(300);

        let mut hc = SwingTrader::new(20, 14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "cci": { "type": "cci", "period": 20 },
                "adx": { "type": "adx", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "cci", "field": "value", "op": "cross_above", "value": 100.0 },
                { "source": "adx", "field": "adx",   "op": "gt", "value": 25.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "cci", "field": "value", "op": "cross_below", "value": -100.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_cci(20) <= 100.0 && cci(20) > 100.0 && adx(14) > 25.0",
            "exit":  "prev_cci(20) >= -100.0 && cci(20) < -100.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "swing_trader: no signals");
        assert_parity("swing_trader hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("swing_trader hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Oscillator Overlord ───────────────────────────────────────────────────
    // entry: ≥2 of (rsi<30, k<20, cci<-100) oversold; exit: ≥2 of overbought
    // CEL: explicit 2-of-3 union

    #[test]
    fn oscillator_overlord_parity() {
        let bars = rsi_bars(200);

        let mut hc = OscillatorOverlord::new(14, 14, 3, 20);
        let hc_sigs = run(&mut hc, &bars);

        // CEL: at least 2 of 3 oversold = any pair is true
        let mut cel = build_strategy("cel", &json!({
            "entry": "(rsi(14) < 30.0 && stoch_k(14) < 20.0) || (rsi(14) < 30.0 && cci(20) < -100.0) || (stoch_k(14) < 20.0 && cci(20) < -100.0)",
            "exit":  "(rsi(14) > 70.0 && stoch_k(14) > 80.0) || (rsi(14) > 70.0 && cci(20) > 100.0) || (stoch_k(14) > 80.0 && cci(20) > 100.0)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "oscillator_overlord: no signals");
        assert_parity("oscillator_overlord hc vs cel", &hc_sigs, &cel_sigs);
    }

    // ── Equilibrium Explorer ──────────────────────────────────────────────────
    // entry: close > ema(200) AND stoch_k < 20 AND macd_hist > 0
    // exit: stoch_k > 80 OR macd_hist < 0

    #[test]
    fn equilibrium_explorer_parity() {
        let bars = trending_bars(400);

        let mut hc = EquilibriumExplorer::new(200, 14, 3, 20.0, 80.0, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "ema":   { "type": "ema",        "period": 200 },
                "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 },
                "macd":  { "type": "macd",       "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close",  "field": "value", "op": "gt", "compare": "ema" },
                { "source": "stoch",  "field": "k",     "op": "lt", "value": 20.0 },
                { "source": "macd",   "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "stoch", "field": "k",         "op": "gt", "value": 80.0 },
                { "source": "macd",  "field": "histogram", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > ema(200) && stoch_k(14) < 20.0 && macd_hist(12) > 0.0",
            "exit":  "stoch_k(14) > 80.0 || macd_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "equilibrium_explorer: no signals");
        assert_parity("equilibrium_explorer hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("equilibrium_explorer hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── SuperTrend + MACD ─────────────────────────────────────────────────────
    // entry: st_bull AND macd_hist > 0; exit: NOT st_bull

    #[test]
    fn supertrend_macd_parity() {
        let bars = trending_bars(300);

        let mut hc = SupertrendMacd::new(10, 3.0, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "st":   { "type": "supertrend", "period": 10, "multiplier": 3.0 },
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "st",   "field": "bullish",   "op": "gt", "value": 0.5 },
                { "source": "macd", "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "st", "field": "bullish", "op": "lt", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "st_bull(10) >= 1.0 && macd_hist(12) > 0.0",
            "exit":  "st_bull(10) < 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "supertrend_macd: no signals");
        assert_parity("supertrend_macd hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("supertrend_macd hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Alligator ─────────────────────────────────────────────────────────────
    // entry: alligator flips bullish; exit: flips bearish

    #[test]
    fn alligator_parity() {
        let bars = trending_bars(300);

        let mut hc = AlligatorStrategy::new(13, 8, 5);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "al": { "type": "alligator", "jaw": 13, "teeth": 8, "lips": 5 } },
            "entry": { "logic": "and", "rules": [
                { "source": "al", "field": "bullish", "op": "cross_above", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "al", "field": "bullish", "op": "cross_below", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_alligator_bull(13) < 1.0 && alligator_bull(13) >= 1.0",
            "exit":  "prev_alligator_bull(13) >= 1.0 && alligator_bull(13) < 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "alligator: no signals");
        assert_parity("alligator hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("alligator hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Elder Ray ─────────────────────────────────────────────────────────────
    // entry: ema rising AND bear_power < 0 AND bear_power > prev_bear (weakening)
    // exit:  bull_power crosses below 0
    // NOTE: dynamic can't express bear_power > prev_bear_power (field vs prev_field cross).
    //       Test hardcoded vs CEL only.

    #[test]
    fn elder_ray_parity() {
        let bars = trending_bars(300);

        let mut hc = ElderRayStrategy::new(13);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "ema(13) > prev_ema(13) && bear_power(13) < 0.0 && bear_power(13) > prev_bear_power(13)",
            "exit":  "prev_bull_power(13) >= 0.0 && bull_power(13) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "elder_ray: no signals");
        assert_parity("elder_ray hc vs cel", &hc_sigs, &cel_sigs);
    }

    // ── MFI Revert ────────────────────────────────────────────────────────────
    // entry: mfi crosses above oversold; exit: mfi crosses above overbought

    #[test]
    fn mfi_revert_parity() {
        let bars = rsi_bars(200);

        let mut hc = MfiRevert::new(14, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "mfi": { "type": "mfi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "mfi", "field": "value", "op": "cross_above", "value": 20.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "mfi", "field": "value", "op": "cross_above", "value": 80.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_mfi(14) <= 20.0 && mfi(14) > 20.0",
            "exit":  "prev_mfi(14) <= 80.0 && mfi(14) > 80.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "mfi_revert: no signals");
        assert_parity("mfi_revert hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("mfi_revert hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Momentum ROC ─────────────────────────────────────────────────────────
    // entry: roc > threshold AND close > ema; exit: roc < 0 OR close < ema

    #[test]
    fn momentum_roc_parity() {
        let bars = trending_bars(300);

        let mut hc = MomentumRoc::new(10, 50, 2.0, 0.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "roc": { "type": "roc", "period": 10 },
                "ema": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "roc",   "field": "value", "op": "gt", "value": 2.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ema" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "roc",   "field": "value", "op": "lt", "value": 0.0 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ema" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "roc(10) > 2.0 && close > ema(50)",
            "exit":  "roc(10) < 0.0 || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "momentum_roc: no signals");
        assert_parity("momentum_roc hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("momentum_roc hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Dual Momentum ─────────────────────────────────────────────────────────
    // entry: roc_fast > 0 AND roc_slow > 0; exit: roc_fast < 0 OR roc_slow < 0

    #[test]
    fn dual_momentum_parity() {
        let bars = trending_bars(300);

        let mut hc = DualMomentum::new(10, 30);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "roc", "period": 10 },
                "slow": { "type": "roc", "period": 30 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "gt", "value": 0.0 },
                { "source": "slow", "field": "value", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "fast", "field": "value", "op": "lt", "value": 0.0 },
                { "source": "slow", "field": "value", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "roc(10) > 0.0 && roc(30) > 0.0",
            "exit":  "roc(10) < 0.0 || roc(30) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "dual_momentum: no signals");
        assert_parity("dual_momentum hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("dual_momentum hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Chop Filter ───────────────────────────────────────────────────────────
    // entry: ema(8) cross above ema(21) AND chop < 61.8; exit: ema(8) cross below ema(21)

    #[test]
    fn chop_filter_parity() {
        let bars = trending_bars(300);

        let mut hc = ChopFilterStrategy::new(14, 8, 21, 61.8);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema",  "period": 8 },
                "slow": { "type": "ema",  "period": 21 },
                "chop": { "type": "chop", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "chop", "field": "value", "op": "lt", "value": 61.8 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(8) <= prev_ema(21) && ema(8) > ema(21) && chop(14) < 61.8",
            "exit":  "prev_ema(8) >= prev_ema(21) && ema(8) < ema(21)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "chop_filter: no signals");
        assert_parity("chop_filter hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("chop_filter hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Awesome Oscillator ────────────────────────────────────────────────────
    // entry: AO crosses above 0; exit: AO crosses below 0

    #[test]
    fn ao_parity() {
        let bars = trending_bars(200);

        let mut hc = AoStrategy::new(5, 34);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "ao": { "type": "ao", "fast": 5, "slow": 34 } },
            "entry": { "logic": "and", "rules": [
                { "source": "ao", "field": "value", "op": "cross_above", "value": 0.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "ao", "field": "value", "op": "cross_below", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ao() <= 0.0 && ao() > 0.0",
            "exit":  "prev_ao() >= 0.0 && ao() < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ao: no signals");
        assert_parity("ao hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("ao hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Bollinger MACD ────────────────────────────────────────────────────────
    // entry: close > bb_upper AND macd_hist > 0; exit: close < bb_mid OR macd_hist < 0

    #[test]
    fn bollinger_macd_parity() {
        let bars = trending_bars(300);

        let mut hc = BollingerMacd::new(20, 2.0, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "bb":   { "type": "bbands", "period": 20, "multiplier": 2.0 },
                "macd": { "type": "macd",   "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt", "compare": "bb", "compare_field": "upper" },
                { "source": "macd",  "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "close", "field": "value", "op": "lt", "compare": "bb", "compare_field": "middle" },
                { "source": "macd",  "field": "histogram", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > bb_upper(20) && macd_hist(12) > 0.0",
            "exit":  "close < bb_mid(20) || macd_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "bollinger_macd: no signals");
        assert_parity("bollinger_macd hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("bollinger_macd hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Range Rover ───────────────────────────────────────────────────────────
    // entry: stoch_k < oversold AND close > ma; exit: stoch_k > overbought

    #[test]
    fn range_rover_parity() {
        let bars = rsi_bars(200);

        let mut hc = RangeRover::new(14, 3, 50, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 },
                "ma":    { "type": "sma", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k",     "op": "lt", "value": 20.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ma" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k", "op": "gt", "value": 80.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "stoch_k(14) < 20.0 && close > sma(50)",
            "exit":  "stoch_k(14) > 80.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "range_rover: no signals");
        assert_parity("range_rover hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("range_rover hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Reversal Catcher ──────────────────────────────────────────────────────
    // entry: K crosses above D AND rsi < 50; exit: K crosses below D OR rsi > 70

    #[test]
    fn reversal_catcher_parity() {
        let bars = rsi_bars(200);

        let mut hc = ReversalCatcher::new(14, 3, 14);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 },
                "rsi":   { "type": "rsi", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k", "op": "cross_above",
                  "compare": "stoch", "compare_field": "d" },
                { "source": "rsi", "field": "value", "op": "lt", "value": 50.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "stoch", "field": "k", "op": "cross_below",
                  "compare": "stoch", "compare_field": "d" },
                { "source": "rsi", "field": "value", "op": "gt", "value": 70.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_stoch_k(14) <= prev_stoch_d(14) && stoch_k(14) > stoch_d(14) && rsi(14) < 50.0",
            "exit":  "(prev_stoch_k(14) >= prev_stoch_d(14) && stoch_k(14) < stoch_d(14)) || rsi(14) > 70.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "reversal_catcher: no signals");
        assert_parity("reversal_catcher hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("reversal_catcher hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── RWI Strategy ─────────────────────────────────────────────────────────
    // entry: rwi_high > 1.0; exit: rwi_low > 1.0

    #[test]
    fn rwi_parity() {
        let bars = trending_bars(300);

        let mut hc = RwiStrategy::new(14, 1.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "rwi": { "type": "rwi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "rwi", "field": "rwi_high", "op": "gt", "value": 1.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "rwi", "field": "rwi_low", "op": "gt", "value": 1.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "rwi_high(14) > 1.0",
            "exit":  "rwi_low(14) > 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "rwi: no signals");
        assert_parity("rwi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("rwi hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── Ichimoku Cloud ────────────────────────────────────────────────────────
    // entry: above_cloud becomes true; exit: above_cloud becomes false

    #[test]
    fn ichimoku_cloud_parity() {
        let bars = trending_bars(400);

        let mut hc = IchimokuCloud::new(9, 26, 52);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "ichi": { "type": "ichimoku", "tenkan": 9, "kijun": 26, "senkou_b": 52 } },
            "entry": { "logic": "and", "rules": [
                { "source": "ichi", "field": "above_cloud", "op": "gt", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "ichi", "field": "above_cloud", "op": "lt", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ichimoku_cloud: no signals");
        assert_parity("ichimoku_cloud hc vs dynamic", &hc_sigs, &dyn_sigs);
    }

    // ── Ichimoku Cross ────────────────────────────────────────────────────────
    // entry: tenkan crosses above kijun AND above_cloud; exit: tenkan crosses below kijun

    #[test]
    fn ichimoku_cross_parity() {
        let bars = trending_bars(400);

        let mut hc = IchimokuCross::new(9, 26, 52);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "ichi": { "type": "ichimoku", "tenkan": 9, "kijun": 26, "senkou_b": 52 } },
            "entry": { "logic": "and", "rules": [
                { "source": "ichi", "field": "tenkan", "op": "cross_above",
                  "compare": "ichi", "compare_field": "kijun" },
                { "source": "ichi", "field": "above_cloud", "op": "gt", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "ichi", "field": "tenkan", "op": "cross_below",
                  "compare": "ichi", "compare_field": "kijun" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ichimoku_cross: no signals");
        assert_parity("ichimoku_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
    }

    // ── VWAP Bounce ───────────────────────────────────────────────────────────
    // entry: crosses above vwap AND rsi < 50 (oversold+10=50); exit: crosses below OR rsi > 65

    #[test]
    fn vwap_bounce_parity() {
        // Need bars with distinct timestamps to simulate intraday sessions
        let bars: Vec<Bar> = (0..200).map(|i| {
            let price = if i < 100 {
                100.0 - i as f64 * 0.5
            } else {
                50.0 + (i - 100) as f64 * 0.8
            };
            bar(i as i64 * 60_000, price.max(1.0))
        }).collect();

        let mut hc = VwapBounce::new(14, 40.0, 65.0, 60);
        let hc_sigs = run(&mut hc, &bars);

        // CEL: prev_close < prev_vwap AND close > vwap AND rsi < 50
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(1) <= prev_vwap() && ema(1) > vwap() && rsi(14) < 50.0",
            "exit":  "(prev_ema(1) >= prev_vwap() && ema(1) < vwap()) || rsi(14) > 65.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "vwap_bounce: no signals");
        assert_parity("vwap_bounce hc vs cel", &hc_sigs, &cel_sigs);
    }

    // ── VWAP Trend ────────────────────────────────────────────────────────────
    // entry: close > vwap AND vwap rising; exit: close < vwap OR vwap falling

    #[test]
    fn vwap_trend_parity() {
        let bars: Vec<Bar> = (0..200).map(|i| {
            let price = if i < 100 {
                100.0 + i as f64 * 0.8
            } else {
                180.0 - (i - 100) as f64 * 0.8
            };
            bar(i as i64 * 60_000, price.max(1.0))
        }).collect();

        let mut hc = VwapTrend::new(60);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > vwap() && vwap() > prev_vwap()",
            "exit":  "close < vwap() || vwap() < prev_vwap()"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "vwap_trend: no signals");
        assert_parity("vwap_trend hc vs cel", &hc_sigs, &cel_sigs);
    }

    // ── Keltner Breakout ──────────────────────────────────────────────────────
    // entry: close > kc_upper; exit: close < kc_lower

    #[test]
    fn keltner_breakout_parity() {
        let bars = trending_bars(300);

        let mut hc = build_strategy("keltner_breakout", &json!({})).unwrap();
        let hc_sigs = run(hc.as_mut(), &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "kc": { "type": "keltner", "period": 20, "atr_period": 10, "multiplier": 2.0 } },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt",
                  "compare": "kc", "compare_field": "upper" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt",
                  "compare": "kc", "compare_field": "lower" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > keltner_upper(20)",
            "exit":  "close < keltner_lower(20)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "keltner_breakout: no signals");
        assert_parity("keltner_breakout hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("keltner_breakout hc vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── 15 new strategies ─────────────────────────────────────────────────────

    #[test]
    fn williams_r_ma_parity() {
        let bars = rsi_bars(200);

        let mut hc = WilliamsRMa::new(14, 50, -80.0, -20.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "wr":  { "type": "williams_r", "period": 14 },
                "ema": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "wr",    "field": "value", "op": "cross_above", "value": -80.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ema" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "wr",    "field": "value", "op": "cross_below", "value": -20.0 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ema" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_williams(14) <= -80.0 && williams(14) > -80.0 && close > ema(50)",
            "exit":  "(prev_williams(14) >= -20.0 && williams(14) < -20.0) || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "williams_r_ma: no signals");
        assert_parity("williams_r_ma hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("williams_r_ma hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn fisher_crossover_parity() {
        let bars = trending_bars(300);

        let mut hc = FisherCrossover::new(10);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "f": { "type": "fisher", "period": 10 } },
            "entry": { "logic": "and", "rules": [
                { "source": "f", "field": "fisher", "op": "cross_above",
                  "compare": "f", "compare_field": "signal" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "f", "field": "fisher", "op": "cross_below",
                  "compare": "f", "compare_field": "signal" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_fisher_line(10) <= prev_fisher_sig(10) && fisher_line(10) > fisher_sig(10)",
            "exit":  "prev_fisher_line(10) >= prev_fisher_sig(10) && fisher_line(10) < fisher_sig(10)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "fisher_crossover: no signals");
        assert_parity("fisher_crossover hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("fisher_crossover hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn uo_reversal_parity() {
        let bars = rsi_bars(200);

        let mut hc = UoReversal::new(7, 14, 28, 30.0, 70.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "uo": { "type": "uo", "fast": 7, "medium": 14, "slow": 28 } },
            "entry": { "logic": "and", "rules": [
                { "source": "uo", "field": "value", "op": "cross_above", "value": 30.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "uo", "field": "value", "op": "gt", "value": 70.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_uo() <= 30.0 && uo() > 30.0",
            "exit":  "uo() > 70.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "uo_reversal: no signals");
        assert_parity("uo_reversal hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("uo_reversal hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn cmo_zero_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = CmoZeroCross::new(14, 50);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "cmo": { "type": "cmo", "period": 14 },
                "ema": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "cmo",   "field": "value", "op": "cross_above", "value": 0.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ema" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "cmo",   "field": "value", "op": "cross_below", "value": 0.0 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ema" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_cmo(14) <= 0.0 && cmo(14) > 0.0 && close > ema(50)",
            "exit":  "(prev_cmo(14) >= 0.0 && cmo(14) < 0.0) || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "cmo_zero_cross: no signals");
        assert_parity("cmo_zero_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("cmo_zero_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn vortex_trend_parity() {
        let bars = trending_bars(300);

        let mut hc = VortexTrend::new(14);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "vx": { "type": "vortex", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "vx", "field": "plus_vi", "op": "cross_above",
                  "compare": "vx", "compare_field": "minus_vi" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "vx", "field": "plus_vi", "op": "cross_below",
                  "compare": "vx", "compare_field": "minus_vi" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_vortex_plus(14) <= prev_vortex_minus(14) && vortex_plus(14) > vortex_minus(14)",
            "exit":  "prev_vortex_plus(14) >= prev_vortex_minus(14) && vortex_plus(14) < vortex_minus(14)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "vortex_trend: no signals");
        assert_parity("vortex_trend hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("vortex_trend hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn cmf_ema_trend_parity() {
        let bars = trending_bars(300);

        let mut hc = CmfEmaTrend::new(20, 50, 0.1, 0.1);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "cmf": { "type": "cmf", "period": 20 },
                "ema": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "cmf",   "field": "value", "op": "gt", "value": 0.1 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ema" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "cmf",   "field": "value", "op": "lt", "value": -0.1 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ema" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "cmf(20) > 0.1 && close > ema(50)",
            "exit":  "cmf(20) < -0.1 || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "cmf_ema_trend: no signals");
        assert_parity("cmf_ema_trend hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("cmf_ema_trend hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn obv_ema_trend_parity() {
        let bars = trending_bars(300);

        let mut hc = ObvEmaTrend::new(20, 50);
        let hc_sigs = run(&mut hc, &bars);

        // CEL: obv() > ema(20) applied to obv is not directly expressible;
        // approximate: prev_obv() < prev_ema(20) for obv_ema.
        // Use dynamic with obv indicator and separate ema of close as proxy.
        let mut cel = build_strategy("cel", &json!({
            "entry": "obv() > ema(20) && close > ema(50)",
            "exit":  "obv() < ema(20) || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        // OBV EMA and close EMA are on different scales; just verify both produce valid signals
        assert!(!hc_sigs.is_empty(), "obv_ema_trend: no signals");
        assert!(!cel_sigs.is_empty(), "obv_ema_trend cel: no signals");
        // NOTE: OBV is in volume units while EMA is in price units — exact parity not expected.
        // Test that hc signals are a subset of (or equal to) what an equivalent condition would give.
        assert_parity("obv_ema_trend hc vs cel", &hc_sigs, &cel_sigs);
    }

    #[test]
    fn rsi_ma_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = RsiMaCross::new(20, 50, 14, 50.0, 45.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 },
                "slow": { "type": "ema", "period": 50 },
                "rsi":  { "type": "rsi", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "rsi", "field": "value", "op": "gt", "value": 50.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" },
                { "source": "rsi", "field": "value", "op": "lt", "value": 45.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50) && rsi(14) > 50.0",
            "exit":  "(prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)) || rsi(14) < 45.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "rsi_ma_cross: no signals");
        assert_parity("rsi_ma_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("rsi_ma_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn bb_rsi_reversal_parity() {
        let bars = rsi_bars(200);

        let mut hc = BbRsiReversal::new(20, 2.0, 14, 35.0, 65.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "bb":  { "type": "bbands", "period": 20, "multiplier": 2.0 },
                "rsi": { "type": "rsi",    "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt",
                  "compare": "bb", "compare_field": "lower" },
                { "source": "rsi", "field": "value", "op": "lt", "value": 35.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "close", "field": "value", "op": "gt",
                  "compare": "bb", "compare_field": "middle" },
                { "source": "rsi", "field": "value", "op": "gt", "value": 65.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close < bb_lower(20) && rsi(14) < 35.0",
            "exit":  "close > bb_mid(20) || rsi(14) > 65.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "bb_rsi_reversal: no signals");
        assert_parity("bb_rsi_reversal hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("bb_rsi_reversal hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn adx_ema_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = AdxEmaCross::new(20, 50, 14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 },
                "slow": { "type": "ema", "period": 50 },
                "adx":  { "type": "adx", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "adx", "field": "adx", "op": "gt", "value": 25.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50) && adx(14) > 25.0",
            "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "adx_ema_cross: no signals");
        assert_parity("adx_ema_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("adx_ema_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn lsma_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = LsmaCross::new(20, 50);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "lsma", "period": 20 },
                "slow": { "type": "lsma", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_lsma(20) <= prev_lsma(50) && lsma(20) > lsma(50)",
            "exit":  "prev_lsma(20) >= prev_lsma(50) && lsma(20) < lsma(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "lsma_cross: no signals");
        assert_parity("lsma_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("lsma_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn alma_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = AlmaCross::new(9, 21, 0.85, 6.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "alma", "period": 9, "offset": 0.85, "sigma": 6.0 },
                "slow": { "type": "alma", "period": 21, "offset": 0.85, "sigma": 6.0 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_alma(9) <= prev_alma(21) && alma(9) > alma(21)",
            "exit":  "prev_alma(9) >= prev_alma(21) && alma(9) < alma(21)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "alma_cross: no signals");
        assert_parity("alma_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("alma_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn vwma_rsi_parity() {
        let bars = trending_bars(300);

        let mut hc = VwmaRsi::new(20, 14, 50.0, 45.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "vwma": { "type": "vwma", "period": 20 },
                "rsi":  { "type": "rsi",  "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt", "compare": "vwma" },
                { "source": "rsi",   "field": "value", "op": "gt", "value": 50.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "close", "field": "value", "op": "lt", "compare": "vwma" },
                { "source": "rsi",   "field": "value", "op": "lt", "value": 45.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > vwma(20) && rsi(14) > 50.0",
            "exit":  "close < vwma(20) || rsi(14) < 45.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "vwma_rsi: no signals");
        assert_parity("vwma_rsi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("vwma_rsi hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn smi_reversal_parity() {
        let bars = rsi_bars(300);

        let mut hc = SmiReversal::new(13, 25, 2, 9, -40.0, 40.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "smi": { "type": "smi" } },
            "entry": { "logic": "and", "rules": [
                { "source": "smi", "field": "smi",    "op": "cross_above",
                  "compare": "smi", "compare_field": "signal" },
                { "source": "smi", "field": "smi",    "op": "lt", "value": -40.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "smi", "field": "smi", "op": "gt", "value": 40.0 },
                { "source": "smi", "field": "smi", "op": "cross_below",
                  "compare": "smi", "compare_field": "signal" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_smi_line() <= prev_smi_sig() && smi_line() > smi_sig() && prev_smi_line() < -40.0",
            "exit":  "smi_line() > 40.0 || (prev_smi_line() >= prev_smi_sig() && smi_line() < smi_sig())"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "smi_reversal: no signals");
        assert_parity("smi_reversal hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("smi_reversal hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn ppo_histogram_parity() {
        let bars = trending_bars(300);

        let mut hc = PpoHistogram::new(12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "ppo": { "type": "ppo", "fast": 12, "slow": 26, "signal": 9 } },
            "entry": { "logic": "and", "rules": [
                { "source": "ppo", "field": "histogram", "op": "cross_above", "value": 0.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "ppo", "field": "histogram", "op": "cross_below", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ppo_hist(12) <= 0.0 && ppo_hist(12) > 0.0",
            "exit":  "prev_ppo_hist(12) >= 0.0 && ppo_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ppo_histogram: no signals");
        assert_parity("ppo_histogram hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("ppo_histogram hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
