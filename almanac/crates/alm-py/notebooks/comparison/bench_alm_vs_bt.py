import time
import backtrader as bt
import pandas as pd
import alm_py
from _shared import load_parquet, bt_run

# ── Backtrader Strategy Definition ────────────────────────────────────────────

class MaCrossover(bt.Strategy):
    params = (
        ("fast", 20),
        ("slow", 50),
    )

    def __init__(self):
        fast = bt.indicators.EMA(self.data.close, period=self.p.fast)
        slow = bt.indicators.EMA(self.data.close, period=self.p.slow)
        self.cross = bt.indicators.CrossOver(fast, slow)

    def next(self):
        if not self.position and self.cross > 0:
            self.buy()
        elif self.position and self.cross < 0:
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

    print("\n=== Running Benchmarks ===")

    # 1. Benchmark alm_py Named (Native Rust Engine)
    params = {"fast": 20.0, "slow": 50.0}
    _ = alm_py.run_backtest(
        "BTCUSDT", "ma_crossover", params, bars,
        initial_capital=10000.0, commission_pct=0.0, slippage_pct=0.0,
        lot_size=0.0, strength_sizing=False
    )
    
    t_alm_named_start = time.perf_counter()
    alm_named_res = alm_py.run_backtest(
        "BTCUSDT", "ma_crossover", params, bars,
        initial_capital=10000.0, commission_pct=0.0, slippage_pct=0.0,
        lot_size=0.0, strength_sizing=False
    )
    d_alm_named = time.perf_counter() - t_alm_named_start
    print(f"- alm_py (Named - Native Rust): {d_alm_named * 1000:.3f} ms")

    # 2. Benchmark alm_py Script (Rhai Script Engine)
    script = """
    let e20 = ind.ema(20);
    let e50 = ind.ema(50);
    if cross_above(e20, e50) { entry = true; }
    if cross_below(e20, e50) { exit  = true; }
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
    _ = bt_run(MaCrossover, df, capital=10000.0, commission=0.0, slippage=0.0, timeframe=bt.TimeFrame.Minutes, compression=1)
    
    t_bt_start = time.perf_counter()
    bt_res = bt_run(MaCrossover, df, capital=10000.0, commission=0.0, slippage=0.0, timeframe=bt.TimeFrame.Minutes, compression=1)
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
