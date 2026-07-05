import time
import backtrader as bt
import pandas as pd
import alm_py
from _shared import load_parquet, bt_run

# ── Backtrader Indicator & Strategy Definitions ───────────────────────────────

class StochasticRaw(bt.Indicator):
    lines = ('k',)
    params = (('period', 14),)
    def __init__(self):
        hh = bt.indicators.Highest(self.data.high, period=self.p.period)
        ll = bt.indicators.Lowest(self.data.low, period=self.p.period)
        self.lines.k = 100.0 * (self.data.close - ll) / (hh - ll + 1e-10)

class StandardCCI(bt.Indicator):
    lines = ('cci',)
    params = (('period', 20), ('factor', 0.015),)
    def __init__(self):
        self.tp = (self.data.high + self.data.low + self.data.close) / 3.0
        self.tp_sma = bt.indicators.SMA(self.tp, period=self.p.period)
    def next(self):
        tps = [self.tp[-i] for i in range(self.p.period)]
        sma = self.tp_sma[0]
        mad = sum(abs(x - sma) for x in tps) / self.p.period
        if mad == 0.0:
            self.lines.cci[0] = 0.0
        else:
            self.lines.cci[0] = (self.tp[0] - sma) / (self.p.factor * mad)

class OscillatorOverlord(bt.Strategy):
    params = (
        ("rsi_period", 14),
        ("stoch_k", 14),
        ("cci_period", 20),
    )
    def __init__(self):
        self.rsi  = bt.indicators.RSI(self.data.close, period=self.p.rsi_period)
        self.stk_ind = StochasticRaw(self.data, period=self.p.stoch_k)
        self.stk  = self.stk_ind.lines.k
        self.cci  = StandardCCI(self.data, period=self.p.cci_period)
    def next(self):
        os_count = int(self.rsi[0] < 30.0) + int(self.stk[0] < 20.0) + int(self.cci[0] < -100.0)
        ob_count = int(self.rsi[0] > 70.0) + int(self.stk[0] > 80.0) + int(self.cci[0] > 100.0)
        if not self.position and os_count >= 2:
            self.buy()
        elif self.position and ob_count >= 2:
            self.close()

def main():
    print("=== Loading real BTCUSDT M1 bar data (20,000 bars) ===")
    try:
        # Load from committed testdata
        bars, df = load_parquet("BTCUSDT", "M1", source="testdata", n=20000)
        print(f"Loaded {len(df)} bars successfully.")
    except Exception as e:
        print(f"Failed to load testdata: {e}")
        return

    print("\n=== Running Complex Strategy Benchmark (oscillator_overlord) ===")

    # 1. Benchmark alm_py Named (Native Rust Engine)
    params = {"rsi_period": 14.0, "stoch_k": 14.0, "stoch_d": 3.0, "cci_period": 20.0}
    
    _ = alm_py.run_backtest(
        "BTCUSDT", "oscillator_overlord", params, bars,
        initial_capital=10000.0, commission_pct=0.0, slippage_pct=0.0,
        lot_size=0.0, strength_sizing=False
    )
    
    t_alm_named_start = time.perf_counter()
    alm_named_res = alm_py.run_backtest(
        "BTCUSDT", "oscillator_overlord", params, bars,
        initial_capital=10000.0, commission_pct=0.0, slippage_pct=0.0,
        lot_size=0.0, strength_sizing=False
    )
    d_alm_named = time.perf_counter() - t_alm_named_start
    print(f"- alm_py (Named - Native Rust): {d_alm_named * 1000:.3f} ms")

    # 2. Benchmark alm_py Script (Rhai Script Engine)
    script = """
    let rsi14 = ind.rsi(14, buf=1);
    let st14 = ind.stochastic(14, buf=1);
    let cci20 = ind.cci(20, buf=1);
    if state["in_position"] == () {
        state["in_position"] = false;
    }
    let os_count = 0;
    if rsi14[0] < 30.0 { os_count = os_count + 1; }
    if st14[0].k < 20.0 { os_count = os_count + 1; }
    if cci20[0] < -100.0 { os_count = os_count + 1; }
    let ob_count = 0;
    if rsi14[0] > 70.0 { ob_count = ob_count + 1; }
    if st14[0].k > 80.0 { ob_count = ob_count + 1; }
    if cci20[0] > 100.0 { ob_count = ob_count + 1; }
    if !state["in_position"] {
        if os_count >= 2 {
            state["in_position"] = true;
            entry = true;
        }
    } else {
        if ob_count >= 2 {
            state["in_position"] = false;
            exit = true;
        }
    }
    """
    
    # Warm-up script run
    _ = alm_py.run_script_backtest(
        "BTCUSDT", script, bars,
        initial_capital=10000.0, commission_pct=0.0, slippage_pct=0.0
    )
    
    t_alm_script_start = time.perf_counter()
    alm_script_res = alm_py.run_script_backtest(
        "BTCUSDT", script, bars,
        initial_capital=10000.0, commission_pct=0.0, slippage_pct=0.0
    )
    d_alm_script = time.perf_counter() - t_alm_script_start
    print(f"- alm_py (Script - Rhai VM): {d_alm_script * 1000:.3f} ms")

    # 3. Benchmark Backtrader (Python)
    # Warm up run
    _ = bt_run(OscillatorOverlord, df, capital=10000.0, commission=0.0, slippage=0.0, timeframe=bt.TimeFrame.Minutes, compression=1)
    
    t_bt_start = time.perf_counter()
    bt_res = bt_run(OscillatorOverlord, df, capital=10000.0, commission=0.0, slippage=0.0, timeframe=bt.TimeFrame.Minutes, compression=1)
    d_bt = time.perf_counter() - t_bt_start
    print(f"- Backtrader (Python): {d_bt * 1000:.3f} ms")

    # 4. Print Comparison Results
    ratio_named = d_bt / d_alm_named
    ratio_script = d_bt / d_alm_script
    print("\n=== Summary ===")
    print(f"alm_py (Named) total trades: {len(alm_named_res.get('trades', []))}")
    print(f"alm_py (Script) total trades: {len(alm_script_res.get('trades', []))}")
    print(f"Backtrader total trades: {bt_res['total_trades']}")
    print(f"--> alm_py (Named - Native Rust) is {ratio_named:.1f}x FASTER than Backtrader!")
    print(f"--> alm_py (Script - Rhai VM) is {ratio_script:.1f}x FASTER than Backtrader!")

if __name__ == "__main__":
    main()
