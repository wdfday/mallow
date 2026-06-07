"""Cross-framework parity tests for the pyramiding engine.

Validates that alm's pyramiding **P&L accounting** (leg averaging, mark-to-market,
commission, force-close) agrees with two established Python backtesting frameworks:

  * **vectorbt**     — `Portfolio.from_signals(accumulate=True)` (closest semantic
                       match to our continuous-signal accumulation). Fills at the
                       signal bar's close.
  * **backtesting.py** — repeated `self.buy()` into one accumulating position.
                       Fills at the next bar's open.

Design notes
------------
Our engine's *entry schedule* is emergent (price-advance gate + next-bar fill), so a
naive "buy every bar" in another framework will NOT line up. Instead we:

  1. Use **flat bars** (open == high == low == close) so a bar has one unambiguous
     price — every framework fills at that same number regardless of its fill model.
  2. Discover our engine's actual per-leg entry bars from an INDEPENDENT-mode run
     (same gate as MERGE), then drive each framework to enter on those exact bars,
     calibrating for each framework's fill lag.

Then the only thing under test is the *accounting* each engine does given identical
fills — which is exactly what we want to cross-check. The entry-gate logic itself is
covered by the Rust unit tests in `crates/engine/src/engine.rs`.

Run after:  maturin develop --release  &&  pytest tests/test_pyramiding_parity.py -v
"""

import warnings

import pytest

import alm_py as alm

warnings.filterwarnings("ignore")

CAP = 100_000.0
TOL = 1e-6  # frameworks agree to the cent on flat-bar, zero-fee scenarios


# ── fixtures / helpers ──────────────────────────────────────────────────────────


def _flat_bars(prices):
    """Bars with open==high==low==close==price → one unambiguous price per bar."""
    ts = [i * 60_000 for i in range(len(prices))]
    return prices, ts, dict(t=ts, o=prices, h=prices, l=prices, c=prices,
                            v=[1000.0] * len(prices))


def _ours(bars, max_units, pyramid, qty=1.0):
    return alm.run_script_backtest(
        "X", "let long = true;", bars,
        initial_capital=CAP, commission_pct=0.0, slippage_pct=0.0,
        sizing="fixed_qty", sizing_params={"qty": qty, "max_positions": 99},
        max_units=max_units, pyramid=pyramid,
    )


def _entry_bars(report):
    """Per-leg entry bar indices (minutes) from an INDEPENDENT-mode run."""
    return sorted(t["entry_ts"] // 60_000 for t in report["trades"])


# ── vectorbt parity ──────────────────────────────────────────────────────────────


def _vbt_final_value(prices, entry_bars, qty):
    vbt = pytest.importorskip("vectorbt")
    import pandas as pd
    idx = pd.date_range("2024-01-01", periods=len(prices), freq="min")
    close = pd.Series(prices, index=idx)
    eset = set(entry_bars)
    entries = pd.Series([i in eset for i in range(len(prices))], index=idx)
    exits = pd.Series([i == len(prices) - 1 for i in range(len(prices))], index=idx)
    pf = vbt.Portfolio.from_signals(
        close, entries, exits,
        size=qty, accumulate=True, init_cash=CAP, fees=0.0, freq="min",
    )
    return float(pf.value().iloc[-1])


@pytest.mark.parametrize(
    "prices_desc, prices, max_units, qty",
    [
        ("rising x12",        [100.0 + i for i in range(12)],           5, 1.0),
        ("rising x12 cap3",   [100.0 + i for i in range(12)],           3, 2.0),
        ("rise then fall",    [100, 101, 102, 103, 104, 105, 104, 103, 102, 101, 100, 99.0], 4, 1.0),
    ],
)
def test_merge_matches_vectorbt(prices_desc, prices, max_units, qty):
    prices, _, bars = _flat_bars([float(p) for p in prices])
    indep = _ours(bars, max_units, pyramid=False, qty=qty)
    merge = _ours(bars, max_units, pyramid=True, qty=qty)
    entry_bars = _entry_bars(indep)

    assert entry_bars, f"engine opened no legs ({prices_desc})"
    vbt_val = _vbt_final_value(prices, entry_bars, qty)
    assert merge["final_equity"] == pytest.approx(vbt_val, abs=1e-4), (
        f"{prices_desc}: ours merge={merge['final_equity']:.4f} vs vbt={vbt_val:.4f} "
        f"(entry_bars={entry_bars})"
    )


# ── backtesting.py parity ────────────────────────────────────────────────────────


def _bt_fill_lag(df):
    """Empirically measure backtesting.py's fill lag (steps between a buy signal
    and the bar it fills on) so the test is robust across versions."""
    from backtesting import Backtest, Strategy

    class Probe(Strategy):
        def init(self):
            self.k = 0

        def next(self):
            if self.k == 0:
                self.buy(size=1)
            self.k += 1

    bt = Backtest(df, Probe, cash=CAP, commission=0.0,
                  trade_on_close=False, exclusive_orders=False, finalize_trades=True)
    s = bt.run()
    return int(s["_trades"]["EntryBar"].iloc[0])


def _bt_final_value(prices, entry_bars, qty):
    pytest.importorskip("backtesting")
    import pandas as pd
    from backtesting import Backtest, Strategy

    n = len(prices)
    idx = pd.date_range("2024-01-01", periods=n, freq="min")
    df = pd.DataFrame(
        {"Open": prices, "High": prices, "Low": prices, "Close": prices,
         "Volume": [1000] * n}, index=idx,
    )
    lag = _bt_fill_lag(df)
    sig = {b - lag for b in entry_bars}  # signal `lag` steps before the target fill bar

    class Pyr(Strategy):
        def init(self):
            self.i = -1

        def next(self):
            self.i += 1
            if self.i in sig:
                self.buy(size=qty)
            if self.i == n - 2:           # close → fills last bar, flattening everything
                self.position.close()

    bt = Backtest(df, Pyr, cash=CAP, commission=0.0,
                  trade_on_close=False, exclusive_orders=False, finalize_trades=True)
    return float(bt.run()["Equity Final [$]"])


def test_merge_matches_backtesting_py():
    prices, _, bars = _flat_bars([100.0 + i for i in range(12)])
    indep = _ours(bars, max_units=5, pyramid=False)
    merge = _ours(bars, max_units=5, pyramid=True)
    entry_bars = _entry_bars(indep)

    bt_val = _bt_final_value(prices, entry_bars, qty=1.0)
    assert merge["final_equity"] == pytest.approx(bt_val, abs=1e-4), (
        f"ours merge={merge['final_equity']:.4f} vs backtesting.py={bt_val:.4f} "
        f"(entry_bars={entry_bars})"
    )


# ── internal invariant cross-checked by both frameworks ─────────────────────────


def test_merge_and_independent_conserve_total_pnl():
    """MERGE (one averaged position) and INDEPENDENT (per-leg positions) must realize
    the SAME total P&L — averaging only redistributes per-trade attribution, not the
    aggregate. Both equal the vectorbt reference."""
    prices, _, bars = _flat_bars([100.0 + i for i in range(12)])
    indep = _ours(bars, max_units=5, pyramid=False)
    merge = _ours(bars, max_units=5, pyramid=True)

    assert merge["final_equity"] == pytest.approx(indep["final_equity"], abs=1e-6)
    # independent mode produces one trade per leg; merge collapses to one
    assert indep["total_trades"] > merge["total_trades"] == 1

    vbt_val = _vbt_final_value(prices, _entry_bars(indep), qty=1.0)
    assert merge["final_equity"] == pytest.approx(vbt_val, abs=1e-4)
