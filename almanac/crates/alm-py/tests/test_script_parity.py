"""Named strategy ↔ Rhai script parity tests (Python layer).

Each parametrized case feeds the **same 10k real BTCUSDT M1 bars** to both
``alm_py.run_backtest`` (named strategy) and ``alm_py.run_script_backtest``
(hand-translated Rhai script) and asserts the signal list is identical:
  * same total trade count
  * each trade has the same entry timestamp and side

The scripts mirror the translations in
``crates/strategy/src/named_real_data_parity_tests.rs``.

Run:
    cd almanac/crates/alm-py
    maturin develop --release
    pytest tests/test_script_parity.py -v
"""

from __future__ import annotations

import pytest
import alm_py as alm


# ── helpers ───────────────────────────────────────────────────────────────────

def _sig_key(trade: dict) -> tuple:
    """(entry_ts, side) — used for order-independent set comparison."""
    return (trade.get("entry_ts", 0), trade.get("side", ""))


def assert_parity(named_result: dict, script_result: dict, label: str) -> None:
    n_named  = named_result["total_trades"]
    n_script = script_result["total_trades"]

    named_sigs  = sorted(_sig_key(t) for t in named_result.get("trades", []))
    script_sigs = sorted(_sig_key(t) for t in script_result.get("trades", []))

    assert named_sigs == script_sigs, (
        f"{label}: signal mismatch — named={n_named} script={n_script}\n"
        f"  named  trades: {named_sigs[:5]}...\n"
        f"  script trades: {script_sigs[:5]}..."
    )


# ── parametrized strategy → script pairs ────────────────────────────────────
# Format: (strategy_name, params_dict, rhai_script)
# Scripts mirror named_real_data_parity_tests.rs translation_rows().

TRANSLATION_ROWS: list[tuple[str, dict, str]] = [

    # ── MA / EMA cross ───────────────────────────────────────────────────────
    ("ma_crossover", {"fast": 20, "slow": 50}, """
let e20 = ind.ema(20);
let e50 = ind.ema(50);
if cross_above(e20, e50) { entry = true; }
if cross_below(e20, e50) { exit  = true; }
"""),
    ("triple_ema", {"ema1": 10, "ema2": 20, "ema3": 50}, """
let e10 = ind.ema(10);
let e20 = ind.ema(20);
let e50 = ind.ema(50);
let bull_now  = e10[0] > e20[0] && e20[0] > e50[0];
let bull_prev = e10[1] > e20[1] && e20[1] > e50[1];
if !bull_prev && bull_now  { entry = true; }
if bull_prev  && !bull_now { exit  = true; }
"""),
    ("hma_crossover", {"fast": 16, "slow": 49}, """
let hma16 = ind.hma(16);
let hma49 = ind.hma(49);
if cross_above(hma16, hma49) { entry = true; }
if cross_below(hma16, hma49) { exit  = true; }
"""),
    ("dema_crossover", {"fast": 12, "slow": 26}, """
let dema12 = ind.dema(12);
let dema26 = ind.dema(26);
if cross_above(dema12, dema26) { entry = true; }
if cross_below(dema12, dema26) { exit  = true; }
"""),
    ("tema_crossover", {"fast": 8, "slow": 21}, """
let tf = ind.tema(8);
let ts = ind.tema(21);
if cross_above(tf, ts) { entry = true; }
if tf[1] > ts[1] && tf[0] <= ts[0] { exit = true; }
"""),
    ("alma_cross", {"fast": 9, "slow": 21}, """
let a9  = ind.alma(9);
let a21 = ind.alma(21);
if cross_above(a9, a21) { entry = true; }
if cross_below(a9, a21) { exit  = true; }
"""),
    ("lsma_cross", {"fast": 20, "slow": 50}, """
let l20 = ind.lsma(20);
let l50 = ind.lsma(50);
if cross_above(l20, l50) { entry = true; }
if cross_below(l20, l50) { exit  = true; }
"""),

    # ── RSI ──────────────────────────────────────────────────────────────────
    ("rsi_mean_rev", {"period": 14, "oversold": 30.0, "overbought": 70.0}, """
let rsi14 = ind.rsi(14, buf=1);
if rsi14[0] < 30.0 { entry = true; }
if rsi14[0] > 70.0 { exit  = true; }
"""),
    ("rsi_ma_cross", {"fast": 20, "slow": 50, "rsi_period": 14,
                      "rsi_entry": 50.0, "rsi_exit": 45.0}, """
let e20   = ind.ema(20);
let e50   = ind.ema(50);
let rsi14 = ind.rsi(14, buf=1);
if cross_above(e20, e50) && rsi14[0] > 50.0 { entry = true; }
if cross_below(e20, e50) || rsi14[0] < 45.0 { exit  = true; }
"""),

    # ── MACD ─────────────────────────────────────────────────────────────────
    ("macd_crossover", {"fast": 12, "slow": 26, "signal": 9}, """
let mh = ind.macd(12);
if mh[1].histogram <= 0.0 && mh[0].histogram > 0.0 { entry = true; }
if mh[1].histogram >= 0.0 && mh[0].histogram < 0.0 { exit  = true; }
"""),
    ("macd_ma", {"fast": 12, "slow": 26, "signal": 9, "ma": 50}, """
let mh    = ind.macd(12, buf=1);
let sma50 = ind.sma(50, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos && mh[0].histogram > 0.0 && close[0] > sma50[0] {
    entry = true; state["in_position"] = true;
}
if in_pos && mh[0].histogram < 0.0 { exit = true; state["in_position"] = false; }
"""),
    ("ppo_histogram", {"fast": 12, "slow": 26, "signal": 9}, """
let pp = ind.ppo(12);
if pp[1].histogram <= 0.0 && pp[0].histogram > 0.0 { entry = true; }
if pp[1].histogram >= 0.0 && pp[0].histogram < 0.0 { exit  = true; }
"""),

    # ── Stochastic ───────────────────────────────────────────────────────────
    ("stochastic_crossover", {"k_period": 14, "d_period": 3,
                               "oversold": 20.0, "overbought": 80.0}, """
let st = ind.stochastic(14);
if st[1].k <= st[1].d && st[0].k > st[0].d && st[0].d < 20.0 { entry = true; }
if st[1].k >= st[1].d && st[0].k < st[0].d && st[0].d > 80.0 { exit  = true; }
"""),
    ("stochastic_dk", {"k": 14, "d": 3}, """
let st = ind.stochastic(14);
if st[1].k <= st[1].d && st[0].k > st[0].d { entry = true; }
if st[1].k >= st[1].d && st[0].k < st[0].d { exit  = true; }
"""),
    ("stoch_rsi", {"period": 14, "smooth_d": 3,
                   "oversold": 0.2, "overbought": 0.8}, """
let sk = ind.stoch_rsi(14);
if sk[1].k >= 0.2 && sk[0].k < 0.2 { entry = true; }
if sk[1].k <= 0.8 && sk[0].k > 0.8 { exit  = true; }
"""),
    ("range_rover", {"k": 14, "d": 3, "ma": 50,
                     "oversold": 20.0, "overbought": 80.0}, """
let st    = ind.stochastic(14, buf=1);
let sma50 = ind.sma(50, buf=1);
if st[0].k < 20.0 && close[0] > sma50[0] { entry = true; }
if st[0].k > 80.0 { exit = true; }
"""),
    ("reversal_catcher", {"k": 14, "d": 3, "rsi_period": 14}, """
let st    = ind.stochastic(14);
let rsi14 = ind.rsi(14, buf=1);
if st[1].k <= st[1].d && st[0].k > st[0].d && rsi14[0] < 50.0 { entry = true; }
if (st[1].k >= st[1].d && st[0].k < st[0].d) || rsi14[0] > 70.0 { exit = true; }
"""),

    # ── ADX / DMI / Aroon / Vortex ───────────────────────────────────────────
    ("adx_ema_cross", {"fast": 20, "slow": 50,
                       "adx_period": 14, "adx_threshold": 25.0}, """
let e20   = ind.ema(20);
let e50   = ind.ema(50);
let adx14 = ind.adx(14, buf=1);
if cross_above(e20, e50) && adx14[0] > 25.0 { entry = true; }
if cross_below(e20, e50) { exit = true; }
"""),
    ("aroon_trend", {"period": 25, "bull_threshold": 70.0,
                     "bear_threshold": 30.0}, """
let ar = ind.aroon(25, buf=1);
if ar[0].up > 70.0 && ar[0].down < 30.0 { entry = true; }
if ar[0].up < ar[0].down { exit = true; }
"""),
    ("vortex_trend", {"period": 14}, """
let vx = ind.vortex(14);
if vx[1].plus_vi <= vx[1].minus_vi && vx[0].plus_vi > vx[0].minus_vi { entry = true; }
if vx[1].plus_vi >= vx[1].minus_vi && vx[0].plus_vi < vx[0].minus_vi { exit  = true; }
"""),
    ("dmi_adx", {"period": 14, "adx_threshold": 25.0}, """
let adx14 = ind.adx(14);
let dmi14 = ind.dmi(14);
if dmi14[1].plus_di <= dmi14[1].minus_di && dmi14[0].plus_di > dmi14[0].minus_di && adx14[0] > 25.0 { entry = true; }
if dmi14[1].minus_di <= dmi14[1].plus_di && dmi14[0].minus_di > dmi14[0].plus_di { exit = true; }
"""),
    ("wolfstein", {"adx_period": 14, "long_threshold": 27.5,
                   "short_threshold": 20.5}, """
let adx14 = ind.adx(14, buf=1);
let dmi14 = ind.dmi(14, buf=1);
if adx14[0] > 27.5 && dmi14[0].plus_di > dmi14[0].minus_di { entry = true; }
if adx14[0] < 20.5 { exit = true; }
"""),
    ("swing_trader", {"cci_period": 20, "adx_period": 14,
                      "adx_threshold": 25.0}, """
let cci20 = ind.cci(20);
let adx14 = ind.adx(14, buf=1);
if cci20[1] <= 100.0  && cci20[0] >  100.0 && adx14[0] > 25.0 { entry = true; }
if cci20[1] >= -100.0 && cci20[0] < -100.0 { exit = true; }
"""),

    # ── Momentum / oscillators ────────────────────────────────────────────────
    ("cci_reversal", {"period": 20, "entry_level": -100.0, "exit_level": 100.0}, """
let cci20 = ind.cci(20);
if cci20[1] <= -100.0 && cci20[0] > -100.0 { entry = true; }
if cci20[1] <= 100.0  && cci20[0] > 100.0  { exit  = true; }
"""),
    ("cmo_zero_cross", {"cmo_period": 14, "ema_period": 50}, """
let cmo14 = ind.cmo(14, buf=2);
let ema50 = ind.ema(50, buf=1);
if cmo14[1] <= 0.0 && cmo14[0] > 0.0 && close[0] > ema50[0] { entry = true; }
if (cmo14[1] >= 0.0 && cmo14[0] < 0.0) || close[0] < ema50[0] { exit = true; }
"""),
    ("fisher_crossover", {"period": 10}, """
let fi = ind.fisher(10);
if fi[1].fisher <= fi[1].signal && fi[0].fisher > fi[0].signal { entry = true; }
if fi[1].fisher >= fi[1].signal && fi[0].fisher < fi[0].signal { exit  = true; }
"""),
    ("roc", {"period": 10}, """
let roc10 = ind.roc(10);
if roc10[1] <= 0.0 && roc10[0] > 0.0 { entry = true; }
if roc10[1] >= 0.0 && roc10[0] < 0.0 { exit  = true; }
"""),
    ("trix", {"period": 18, "signal": 9}, """
let th = ind.trix(18);
if th[1].histogram <= 0.0 && th[0].histogram > 0.0 { entry = true; }
if th[1].histogram >= 0.0 && th[0].histogram < 0.0 { exit  = true; }
"""),
    ("tsi", {"first": 25, "second": 13,
              "entry_threshold": -25.0, "exit_threshold": 25.0}, """
let tsi25 = ind.tsi(25);
if tsi25[1] < -25.0 && tsi25[0] >= -25.0 { entry = true; }
if tsi25[1] >= 25.0 && tsi25[0] <  25.0  { exit  = true; }
"""),
    ("uo_reversal", {"fast": 7, "medium": 14, "slow": 28,
                     "oversold": 30.0, "overbought": 70.0}, """
let uo = ind.uo(0);
if uo[1] <= 30.0 && uo[0] > 30.0 { entry = true; }
if uo[0] > 70.0 { exit = true; }
"""),
    ("connors_rsi", {"rsi_period": 3, "streak_period": 2, "rank_period": 100,
                     "oversold": 10.0, "overbought": 70.0}, """
let crsi = ind.connors_rsi(3, buf=1);
if crsi[0] < 10.0 { entry = true; }
if crsi[0] > 70.0 { exit  = true; }
"""),
    ("ao", {"fast": 5, "slow": 34}, """
let ao = ind.ao(0);
if ao[1] <= 0.0 && ao[0] > 0.0 { entry = true; }
if ao[1] >= 0.0 && ao[0] < 0.0 { exit  = true; }
"""),
    ("williams_r_ma", {"wr_period": 14, "ema_period": 50,
                       "oversold": -80.0, "overbought": -20.0}, """
let wr14  = ind.williams_r(14);
let ema50 = ind.ema(50, buf=1);
if wr14[1] <= -80.0 && wr14[0] > -80.0 && close[0] > ema50[0] { entry = true; }
if wr14[1] >= -20.0 && wr14[0] < -20.0 { exit = true; }
"""),
    ("smi_reversal", {"period": 13, "smooth1": 25, "smooth2": 2,
                      "signal_period": 9, "oversold": -40.0, "overbought": 40.0}, """
let smi13 = ind.smi(13);
if smi13[1].smi <= smi13[1].signal && smi13[0].smi > smi13[0].signal && smi13[1].smi < -40.0 { entry = true; }
if smi13[0].smi > 40.0 || (smi13[1].smi >= smi13[1].signal && smi13[0].smi < smi13[0].signal) { exit = true; }
"""),

    # ── Volume ────────────────────────────────────────────────────────────────
    ("mfi_trend", {"period": 14, "bull_threshold": 50.0, "bear_threshold": 40.0}, """
let mfi14 = ind.mfi(14);
if mfi14[1] <= 50.0 && mfi14[0] > 50.0 { entry = true; }
if mfi14[0] < 40.0  { exit  = true; }
"""),
    ("mfi_revert", {"period": 14, "oversold": 20.0, "overbought": 80.0}, """
let mfi14 = ind.mfi(14);
if mfi14[1] <= 20.0 && mfi14[0] > 20.0 { entry = true; }
if mfi14[1] <= 80.0 && mfi14[0] > 80.0 { exit  = true; }
"""),
    ("cmf_ema_trend", {"cmf_period": 20, "ema_period": 50,
                       "bull_threshold": 0.1, "bear_threshold": 0.1}, """
let cmf20 = ind.cmf(20, buf=1);
let ema50 = ind.ema(50, buf=1);
if cmf20[0] > 0.1 && close[0] > ema50[0] { entry = true; }
if cmf20[0] < -0.1 { exit = true; }
"""),
    ("vwma_rsi", {"vwma_period": 20, "rsi_period": 14,
                  "rsi_entry": 50.0, "rsi_exit": 45.0}, """
let vwma20 = ind.vwma(20, buf=1);
let rsi14  = ind.rsi(14, buf=1);
if close[0] > vwma20[0] && rsi14[0] > 50.0 { entry = true; }
if rsi14[0] < 45.0 { exit = true; }
"""),

    # ── Bollinger / Keltner / Donchian ────────────────────────────────────────
    ("bollinger_macd", {"bb_period": 20, "bb_std": 2.0,
                        "fast": 12, "slow": 26, "signal": 9}, """
let bb20 = ind.bbands(20, buf=1);
let mh   = ind.macd(12, buf=1);
if close[0] > bb20[0].upper && mh[0].histogram > 0.0 { entry = true; }
if close[0] < bb20[0].middle || mh[0].histogram < 0.0 { exit = true; }
"""),
    ("bb_rsi_reversal", {"bb_period": 20, "bb_std": 2.0, "rsi_period": 14,
                         "oversold": 35.0, "overbought": 65.0}, """
let bb20  = ind.bbands(20, buf=1);
let rsi14 = ind.rsi(14, buf=1);
if close[0] < bb20[0].lower && rsi14[0] < 35.0 { entry = true; }
if close[0] > bb20[0].middle || rsi14[0] > 65.0 { exit  = true; }
"""),
    ("donchian_breakout", {"entry": 20, "exit": 10}, """
let du20 = ind.donchian(20);
let dl10 = ind.donchian(10);
if close[0] > du20[1].upper { entry = true; }
if close[0] < dl10[1].lower { exit  = true; }
"""),
    ("keltner_breakout", {"period": 20, "atr_period": 10, "multiplier": 2.0}, """
let kc20 = ind.keltner(20, buf=1);
if close[0] > kc20[0].upper { entry = true; }
if close[0] < kc20[0].lower { exit  = true; }
"""),
    ("chop_filter", {"chop_period": 14, "fast_ema": 8, "slow_ema": 21,
                     "chop_threshold": 61.8}, """
let ema8   = ind.ema(8);
let ema21  = ind.ema(21);
let chop14 = ind.chop(14, buf=1);
if ema8[1] <= ema21[1] && ema8[0] > ema21[0] && chop14[0] < 61.8 { entry = true; }
if ema8[1] >= ema21[1] && ema8[0] < ema21[0] { exit = true; }
"""),

    # ── KAMA / SAR / SuperTrend ───────────────────────────────────────────────
    ("kama", {"er_period": 10, "fast": 2, "slow": 30}, """
let kama10 = ind.kama(10);
let was_above = close[1] > kama10[1];
let is_above  = close[0] > kama10[0];
if !was_above && is_above  { entry = true; }
if  was_above && !is_above { exit  = true; }
"""),
    ("parabolic_sar", {"step": 0.02, "max": 0.2}, """
let ps = ind.parabolic_sar(0);
let was_bull = ps[1].bullish >= 0.5;
let now_bull = ps[0].bullish >= 0.5;
if !was_bull && now_bull  { entry = true; }
if  was_bull && !now_bull { exit  = true; }
"""),
    ("supertrend", {"period": 10, "multiplier": 3.0}, """
let st = ind.supertrend(10);
let was_bull = st[1].bullish >= 0.5;
let now_bull = st[0].bullish >= 0.5;
if !was_bull && now_bull  { entry = true; }
if  was_bull && !now_bull { exit  = true; }
"""),
    ("supertrend_macd", {"period": 10, "multiplier": 3.0,
                         "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}, """
let st = ind.supertrend(10, buf=1);
let mh = ind.macd(12, buf=1);
if st[0].bullish >= 0.5 && mh[0].histogram > 0.0 { entry = true; }
if st[0].bullish < 0.5  { exit = true; }
"""),

    # ── GMMA / Alligator / RWI / Vortex ──────────────────────────────────────
    ("gmma_crossover", {}, """
let gm = ind.gmma(0);
let bull_now  = gm[0].short_avg > gm[0].long_avg;
let bull_prev = gm[1].short_avg > gm[1].long_avg;
if !bull_prev && bull_now  { entry = true; }
if bull_prev  && !bull_now { exit  = true; }
"""),
    ("alligator", {"jaw": 13, "teeth": 8, "lips": 5}, """
let al = ind.alligator(13);
if al[1].bullish < 0.5 && al[0].bullish >= 0.5 { entry = true; }
if al[1].bullish >= 0.5 && al[0].bullish < 0.5 { exit  = true; }
"""),
    ("rwi", {"period": 14, "threshold": 1.0}, """
let rwi14 = ind.rwi(14, buf=1);
if rwi14[0].rwi_high > 1.0 { entry = true; }
if rwi14[0].rwi_low  > 1.0 { exit  = true; }
"""),
    ("volatility_ratio", {"lookback": 10, "threshold": 0.5}, """
let vr = ind.volatility_ratio(10, buf=1);
if vr[0] > 0.5 && close[0] > close[1] { entry = true; }
if vr[0] <= 0.5 { exit = true; }
"""),

    # ── Ichimoku ──────────────────────────────────────────────────────────────
    ("ichimoku_cloud", {"tenkan": 9, "kijun": 26, "senkou_b": 52}, """
let ic = ind.ichimoku(9, buf=1);
if ic[0].above_cloud >= 0.5 { entry = true; }
if ic[0].below_cloud >= 0.5 { exit  = true; }
"""),
    ("ichimoku_cross", {"tenkan": 9, "kijun": 26, "senkou_b": 52}, """
let ic = ind.ichimoku(9);
if ic[1].tenkan <= ic[1].kijun && ic[0].tenkan > ic[0].kijun && ic[0].above_cloud >= 0.5 { entry = true; }
if ic[1].tenkan >= ic[1].kijun && ic[0].tenkan < ic[0].kijun { exit = true; }
"""),

    # ── Momentum ROC variants ─────────────────────────────────────────────────
    ("momentum_roc", {"roc_period": 10, "ema_period": 50,
                      "entry_threshold": 0.5, "exit_threshold": 0.0}, """
let roc10 = ind.roc(10, buf=1);
let ema50 = ind.ema(50, buf=1);
if roc10[0] > 0.5 && close[0] > ema50[0] { entry = true; }
if roc10[0] < 0.0 || close[0] < ema50[0] { exit  = true; }
"""),
    ("dual_momentum", {"fast": 10, "slow": 30}, """
let roc10 = ind.roc(10, buf=1);
let roc30 = ind.roc(30, buf=1);
if roc10[0] > 0.0 && roc30[0] > 0.0 { entry = true; }
if roc10[0] < 0.0 || roc30[0] < 0.0 { exit  = true; }
"""),
    ("kst", {"period": 10}, """
let roc10 = ind.roc(10);
if roc10[1] <= 0.0 && roc10[0] > 0.0 { entry = true; }
if roc10[1] >= 0.0 && roc10[0] < 0.0 { exit  = true; }
"""),

    # ── Trend Transition / Follower ───────────────────────────────────────────
    ("trend_transition", {"fast": 50, "slow": 200,
                          "adx_period": 14, "adx_threshold": 25.0}, """
let e50  = ind.ema(50);
let e200 = ind.ema(200);
let adx14 = ind.adx(14, buf=1);
if cross_above(e50, e200) && adx14[0] > 25.0 { entry = true; }
if cross_below(e50, e200) { exit = true; }
"""),
    ("trend_follower", {"fast_ma": 50, "slow_ma": 200,
                        "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}, """
let s50  = ind.sma(50, buf=1);
let s200 = ind.sma(200, buf=1);
let mh   = ind.macd(12, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos && s50[0] > s200[0] && mh[0].histogram > 0.0 {
    entry = true; state["in_position"] = true;
}
if in_pos && (s50[0] < s200[0] || mh[0].histogram < 0.0) {
    exit = true; state["in_position"] = false;
}
"""),

    # ── Equilibrium / Composite combos ────────────────────────────────────────
    ("equilibrium_explorer", {"ema_period": 200, "stoch_k": 14, "stoch_d": 3,
                               "stoch_oversold": 20.0, "stoch_overbought": 80.0,
                               "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}, """
let ema200 = ind.ema(200, buf=1);
let st     = ind.stochastic(14, buf=1);
let mh     = ind.macd(12, buf=1);
if close[0] > ema200[0] && st[0].k < 20.0 && mh[0].histogram > 0.0 { entry = true; }
if st[0].k > 80.0 || mh[0].histogram < 0.0 { exit = true; }
"""),
]


# ── pytest parametrize ────────────────────────────────────────────────────────

_ids = [row[0] for row in TRANSLATION_ROWS]


@pytest.mark.parametrize("name,params,script", TRANSLATION_ROWS, ids=_ids)
def test_named_vs_script_parity(real_bars, name, params, script):
    """Named strategy and its Rhai script equivalent must produce identical trades."""
    named_result  = alm.run_backtest(
        "BTCUSDT", name, params, real_bars,
        initial_capital=10_000.0, commission_pct=0.0, slippage_pct=0.0,
        lot_size=0.0, strength_sizing=False,
    )
    script_result = alm.run_script_backtest(
        "BTCUSDT", script, real_bars,
        initial_capital=10_000.0, commission_pct=0.0, slippage_pct=0.0,
    )

    # Must have at least 1 signal to be a meaningful parity test
    assert named_result["total_trades"] >= 1, (
        f"{name}: named strategy produced 0 trades on 10k bars — "
        "check params or bar fixture"
    )

    assert_parity(named_result, script_result, name)


@pytest.mark.parametrize("name,params,script", TRANSLATION_ROWS, ids=_ids)
def test_named_vs_script_min_trades(real_bars, name, params, script):
    """Each strategy must fire at least 5 trades on 10k bars (signal richness check)."""
    r = alm.run_backtest(
        "BTCUSDT", name, params, real_bars,
        initial_capital=10_000.0, commission_pct=0.0, slippage_pct=0.0,
        lot_size=0.0, strength_sizing=False,
    )
    assert r["total_trades"] >= 5, (
        f"{name}: only {r['total_trades']} trades on 10k real bars — "
        "params may be too slow; consider relaxing thresholds in TRANSLATION_ROWS"
    )
