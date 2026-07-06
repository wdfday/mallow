"""
Shared helpers for the `breakpoints_*.ipynb` deep-dive notebooks.

These notebooks zoom into the exact bars where alm_py (Rust) and Backtrader
disagree on a trade for Stochastic-%K/%D-based strategies (`stochastic_dk`,
`reversal_catcher`) — see `docs/stochastic-sma-drift-whipsaw.md` for the
narrative root-cause writeup this tooling backs up with live, re-runnable
numbers instead of a frozen snapshot.
"""
from __future__ import annotations

import numpy as np
import pandas as pd
import backtrader as bt


# ── Per-bar indicator series, both engines, index-aligned to `DF` ────────────

def run_alm_indicator_series(bars: dict, cfg: dict, symbol: str = "BTCUSDT") -> dict[str, np.ndarray]:
    """Wrap `alm_py.run_indicators` — flatten `{name: {field: [..]}}` into
    `{"name.field": np.ndarray}` with `None` (not-yet-warm) mapped to `NaN`."""
    import alm_py
    raw = alm_py.run_indicators(symbol, bars, cfg)
    out: dict[str, np.ndarray] = {}
    for name, fields in raw.items():
        for field, values in fields.items():
            out[f"{name}.{field}"] = np.array(
                [np.nan if v is None else v for v in values], dtype=float
            )
    return out


def run_bt_indicator_series(df: pd.DataFrame, build_fn) -> dict[str, np.ndarray]:
    """Run a throwaway Backtrader strategy that only records indicator lines
    every bar (no orders). `build_fn(self) -> {name: bt_line}` is called once
    in `__init__`; whatever lines it returns get one float recorded per bar.

    Backtrader defers calling `next()` until every declared indicator's
    warmup (`minperiod`) is satisfied, so the recorded history is shorter
    than `len(df)` — we pad the front with `NaN` to realign to `df`'s index.
    """
    class _Recorder(bt.Strategy):
        def __init__(self):
            self._lines = build_fn(self)
            self._hist = {k: [] for k in self._lines}

        def next(self):
            for k, line in self._lines.items():
                self._hist[k].append(float(line[0]))

    cerebro = bt.Cerebro()
    data = bt.feeds.PandasData(dataname=df, timeframe=bt.TimeFrame.Minutes, compression=1)
    cerebro.adddata(data)
    cerebro.addstrategy(_Recorder)
    strat = cerebro.run()[0]

    n = len(df)
    out: dict[str, np.ndarray] = {}
    for k, hist in strat._hist.items():
        offset = n - len(hist)
        arr = np.full(n, np.nan)
        arr[offset:] = hist
        out[k] = arr
    return out


# ── Trade-list alignment — find the discrete "breakpoints" ───────────────────

def bt_trade_entries_ms(bt_trades: list[dict]) -> list[int]:
    return [int(pd.Timestamp(t["entry_dt"]).tz_convert("UTC").timestamp() * 1000) for t in bt_trades]


def bt_trade_exits_ms(bt_trades: list[dict]) -> list[int]:
    return [int(pd.Timestamp(t["exit_dt"]).tz_convert("UTC").timestamp() * 1000) for t in bt_trades]


def align_trade_entries(alm_entries_ms: list[int], bt_entries_ms: list[int], lookahead: int = 8) -> list[dict]:
    """Two-pointer diff between the two entry-timestamp lists. Assumes both
    lists are otherwise identical (same underlying signal logic) except for
    a handful of discrete insert/delete events — exactly the "domino phase
    shift" pattern described in the doc. Each divergence tries to resync by
    scanning up to `lookahead` entries ahead on either side.
    """
    a, b = alm_entries_ms, bt_entries_ms
    i = j = 0
    events: list[dict] = []
    while i < len(a) and j < len(b):
        if a[i] == b[j]:
            i += 1
            j += 1
            continue
        resynced = False
        for k in range(1, lookahead + 1):
            if i + k < len(a) and a[i + k] == b[j]:
                events.append({"type": "rust_extra", "alm_idx": i, "ts": a[i:i + k]})
                i += k
                resynced = True
                break
            if j + k < len(b) and a[i] == b[j + k]:
                events.append({"type": "bt_extra", "bt_idx": j, "ts": b[j:j + k]})
                j += k
                resynced = True
                break
        if not resynced:
            events.append({
                "type": "shifted", "alm_idx": i, "bt_idx": j,
                "alm_ts": a[i], "bt_ts": b[j],
            })
            i += 1
            j += 1
    if i < len(a):
        events.append({"type": "tail_rust_extra", "alm_idx": i, "ts": a[i:]})
    if j < len(b):
        events.append({"type": "tail_bt_extra", "bt_idx": j, "ts": b[j:]})
    return events


def event_anchor_ts(event: dict) -> int:
    """Rough timestamp near a breakpoint — the entry-list mismatch itself.
    This is a *downstream* symptom (e.g. a spurious re-entry after a phantom
    exit), not the bar where %K/%D actually tangented — use
    `refine_anchor_to_tangency` to find that."""
    if event["type"] in ("rust_extra", "bt_extra", "tail_rust_extra", "tail_bt_extra"):
        return event["ts"][0]
    return min(event["alm_ts"], event["bt_ts"])


def refine_anchor_to_tangency(rough_anchor_ts: int, ts_ms: np.ndarray, k_rust: np.ndarray, d_rust: np.ndarray,
                               k_bt: np.ndarray, d_bt: np.ndarray, search_back: int = 15) -> int:
    """The entry-list mismatch (`event_anchor_ts`) fires 1-2 bars *after* the
    real cause — a phantom exit/entry earlier, at the bar where %K and %D
    actually went near-tangent (see doc §7: exit at 07:59, re-entry at 08:01,
    the mismatch we detect is the 08:01 entry but the root cause is 07:59).
    Scan backward from the rough anchor for the bar with the smallest
    `min(|k-d| rust, |k-d| bt)` — that's the true tangency point.
    """
    gap = np.minimum(np.abs(k_rust - d_rust), np.abs(k_bt - d_bt))
    center = int(np.searchsorted(ts_ms, rough_anchor_ts))
    lo = max(0, center - search_back)
    window = gap[lo:center + 1]
    if len(window) == 0 or np.all(np.isnan(window)):
        return rough_anchor_ts
    best = lo + int(np.nanargmin(window))
    return int(ts_ms[best])


# ── Position-state lookup (for the detail table) ──────────────────────────────

def position_at(ts_ms: int, trades: list[dict], entry_key: str, exit_key: str, ts_getter) -> bool:
    """True if `ts_ms` falls inside [entry, exit) of any trade in `trades`."""
    for t in trades:
        e, x = ts_getter(t[entry_key]), ts_getter(t[exit_key])
        if e <= ts_ms < x:
            return True
    return False


def _ms(v) -> int:
    if isinstance(v, (int, np.integer)):
        return int(v)
    return int(pd.Timestamp(v).tz_convert("UTC").timestamp() * 1000)


# ── Build the detailed per-breakpoint window table ────────────────────────────

def build_window_table(
    df: pd.DataFrame,
    ts_ms: np.ndarray,
    anchor_ts: int,
    series: dict[str, np.ndarray],
    alm_trades: list[dict],
    bt_trades: list[dict],
    half_window: int = 6,
) -> pd.DataFrame:
    """`series` maps column name -> full-length array (e.g. 'k_rust', 'd_rust',
    'k_bt', 'd_bt', optionally 'rsi_rust', 'rsi_bt'). Returns a DataFrame
    windowed to `anchor_ts +/- half_window` bars with position-state columns
    for both engines appended.
    """
    center = int(np.searchsorted(ts_ms, anchor_ts))
    lo = max(0, center - half_window)
    hi = min(len(df), center + half_window + 1)

    out = pd.DataFrame({"ts_ms": ts_ms[lo:hi]})
    out["time_utc"] = pd.to_datetime(out["ts_ms"], unit="ms", utc=True)
    out["close"] = df["close"].to_numpy()[lo:hi]
    for name, arr in series.items():
        out[name] = arr[lo:hi]

    out["position_rust"] = [
        position_at(t, alm_trades, "entry_ts", "exit_ts", lambda v: int(v))
        for t in out["ts_ms"]
    ]
    out["position_bt"] = [
        position_at(t, bt_trades, "entry_dt", "exit_dt", _ms)
        for t in out["ts_ms"]
    ]
    out["marker"] = ["<<<" if t == anchor_ts else "" for t in out["ts_ms"]]
    return out.reset_index(drop=True)


# ── Summary across all breakpoints ────────────────────────────────────────────

def resolve_anchor(event: dict, ts_ms: np.ndarray, series: dict[str, np.ndarray], search_back: int = 15) -> int:
    """Rough entry-mismatch timestamp, refined back to the real tangency bar
    (see `refine_anchor_to_tangency`) when %K/%D series are available."""
    rough = event["ts"][0] if event["type"] in ("tail_rust_extra", "tail_bt_extra") else event_anchor_ts(event)
    have_kd = all(k in series for k in ("k_rust", "d_rust", "k_bt", "d_bt"))
    if not have_kd:
        return rough
    return refine_anchor_to_tangency(
        rough, ts_ms, series["k_rust"], series["d_rust"], series["k_bt"], series["d_bt"], search_back
    )


def summarize_events(events: list[dict], ts_ms: np.ndarray, series: dict[str, np.ndarray]) -> pd.DataFrame:
    """One row per breakpoint: refined tangency-bar time + the %K/%D gap on
    both engines at that bar (evidence of near-tangency at the moment of
    divergence, not just the downstream entry/exit mismatch)."""
    rows = []
    for i, ev in enumerate(events):
        anchor = resolve_anchor(ev, ts_ms, series)
        idx = int(np.searchsorted(ts_ms, anchor))
        idx = min(idx, len(ts_ms) - 1)
        row = {
            "event_idx": i,
            "type": ev["type"],
            "time_utc": pd.to_datetime(anchor, unit="ms", utc=True),
        }
        if "k_rust" in series and "d_rust" in series:
            row["gap_rust"] = abs(series["k_rust"][idx] - series["d_rust"][idx])
        if "k_bt" in series and "d_bt" in series:
            row["gap_bt"] = abs(series["k_bt"][idx] - series["d_bt"][idx])
        rows.append(row)
    return pd.DataFrame(rows)


# ── Plotly visualizer ──────────────────────────────────────────────────────────

def plot_breakpoint_window(window_df: pd.DataFrame, event: dict, rsi_cols: tuple[str, str] | None = None,
                            title_suffix: str = ""):
    import plotly.graph_objects as go
    from plotly.subplots import make_subplots

    nrows = 4 if rsi_cols else 3
    heights = [0.32, 0.28, 0.2, 0.2] if rsi_cols else [0.4, 0.35, 0.25]
    titles = ["Close price + vị thế 2 engine", "%K / %D — Rust vs Backtrader",
              "|K-D| gap (log scale) — sàn nhiễu ~1e-10 đến 1e-13"]
    if rsi_cols:
        titles.append("RSI(14) — Rust vs Backtrader")

    fig = make_subplots(rows=nrows, cols=1, shared_xaxes=True, row_heights=heights,
                         vertical_spacing=0.05, subplot_titles=titles)

    x = window_df["time_utc"]

    fig.add_trace(go.Scatter(x=x, y=window_df["close"], name="close", mode="lines+markers",
                              line=dict(color="#999", width=1), marker=dict(size=4)), row=1, col=1)
    rust_long = window_df[window_df["position_rust"]]
    bt_long = window_df[window_df["position_bt"]]
    fig.add_trace(go.Scatter(x=rust_long["time_utc"], y=rust_long["close"], mode="markers",
                              name="Rust: đang long", marker=dict(symbol="circle-open", size=16,
                              color="#1f77b4", line=dict(width=2))), row=1, col=1)
    fig.add_trace(go.Scatter(x=bt_long["time_utc"], y=bt_long["close"], mode="markers",
                              name="BT: đang long", marker=dict(symbol="x", size=10,
                              color="#ff7f0e", line=dict(width=2))), row=1, col=1)

    anchor_rows = window_df[window_df["marker"] == "<<<"]
    if len(anchor_rows):
        for r in range(1, nrows + 1):
            fig.add_vline(x=anchor_rows["time_utc"].iloc[0], line_dash="dot", line_color="red",
                          opacity=0.6, row=r, col=1)

    fig.add_trace(go.Scatter(x=x, y=window_df["k_rust"], name="%K (rust)", line=dict(color="#1f77b4")), row=2, col=1)
    fig.add_trace(go.Scatter(x=x, y=window_df["d_rust"], name="%D (rust)",
                              line=dict(color="#1f77b4", dash="dot")), row=2, col=1)
    fig.add_trace(go.Scatter(x=x, y=window_df["k_bt"], name="%K (bt)", line=dict(color="#ff7f0e")), row=2, col=1)
    fig.add_trace(go.Scatter(x=x, y=window_df["d_bt"], name="%D (bt)",
                              line=dict(color="#ff7f0e", dash="dot")), row=2, col=1)

    gap_rust = (window_df["k_rust"] - window_df["d_rust"]).abs().clip(lower=1e-16)
    gap_bt = (window_df["k_bt"] - window_df["d_bt"]).abs().clip(lower=1e-16)
    fig.add_trace(go.Scatter(x=x, y=gap_rust, name="|K-D| rust", line=dict(color="#1f77b4")), row=3, col=1)
    fig.add_trace(go.Scatter(x=x, y=gap_bt, name="|K-D| bt", line=dict(color="#ff7f0e")), row=3, col=1)
    fig.add_hline(y=1e-10, line_dash="dash", line_color="red", opacity=0.7,
                  annotation_text="sàn nhiễu Sma ~1e-10", row=3, col=1)
    fig.update_yaxes(type="log", row=3, col=1)

    if rsi_cols:
        rk, bk = rsi_cols
        fig.add_trace(go.Scatter(x=x, y=window_df[rk], name="RSI (rust)", line=dict(color="#1f77b4")), row=4, col=1)
        fig.add_trace(go.Scatter(x=x, y=window_df[bk], name="RSI (bt)", line=dict(color="#ff7f0e")), row=4, col=1)
        fig.add_hline(y=50, line_dash="dash", line_color="green", opacity=0.5, row=4, col=1)
        fig.add_hline(y=70, line_dash="dash", line_color="purple", opacity=0.5, row=4, col=1)

    fig.update_layout(height=250 * nrows + 100,
                       title=f"Breakpoint {title_suffix} — {event['type']}",
                       hovermode="x unified", legend=dict(orientation="h", y=-0.05))
    return fig


def make_visualizer(events: list[dict], df: pd.DataFrame, ts_ms: np.ndarray, series: dict[str, np.ndarray],
                     alm_trades: list[dict], bt_trades: list[dict], half_window: int = 6,
                     rsi_cols: tuple[str, str] | None = None):
    """Dropdown-driven interactive viewer — pick a breakpoint, see its
    price/%K%D/gap (and RSI, if given) zoom plot plus the raw numeric table.
    Stays interactive when the notebook is reopened in a live Jupyter session;
    `nbconvert --execute` bakes in the first breakpoint's rendered output."""
    import ipywidgets as widgets
    from IPython.display import display

    anchors = [resolve_anchor(ev, ts_ms, series) for ev in events]
    options = [
        (f"#{i}: {ev['type']} @ {pd.to_datetime(anchors[i], unit='ms', utc=True)} (tangency bar)", i)
        for i, ev in enumerate(events)
    ]
    dropdown = widgets.Dropdown(options=options, description="Breakpoint:", layout=widgets.Layout(width="70%"))
    out = widgets.Output()

    def _redraw(change=None):
        with out:
            out.clear_output(wait=True)
            ev = events[dropdown.value]
            anchor = anchors[dropdown.value]
            tbl = build_window_table(df, ts_ms, anchor, series, alm_trades, bt_trades, half_window)
            fig = plot_breakpoint_window(tbl, ev, rsi_cols=rsi_cols, title_suffix=f"#{dropdown.value}")
            fig.show()
            display(tbl.style.format(precision=15))

    dropdown.observe(_redraw, names="value")
    display(dropdown, out)
    _redraw()
