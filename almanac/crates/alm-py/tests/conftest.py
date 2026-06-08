"""Shared fixtures for alm_py tests.

Build & install before running:
    cd almanac/crates/alm-py
    maturin develop --release
    pytest tests/
"""

import math
import pathlib
import numpy as np
import pytest

# ── Real-data Parquet path ────────────────────────────────────────────────────
# 2M-row M1 file shipped in crates/data/testdata/
_PARQUET_PATH = (
    pathlib.Path(__file__).parent.parent.parent  # almanac/crates/
    / "data/testdata/BTCUSDT/M1"
    / "BTCUSDT_M1_2026-01.parquet"
)


# ── Synthetic bar factories ──────────────────────────────────────────────────

def _make_bars(n: int, base: float, *, seed: int = 42, trend: float = 0.0,
               noise: float = 0.005, period: float = 30.0) -> dict:
    """
    Generate `n` OHLCV bars.

    Parameters
    ----------
    trend  : price gain per bar (e.g. 0.01 = +1% drift per bar)
    noise  : fractional random noise on top of close
    period : sine-wave period in bars
    """
    rng = np.random.default_rng(seed)
    t_start = 1_700_000_000_000   # 2023-11-14 UTC in ms
    bar_ms = 3_600_000            # 1-hour bars

    t = [t_start + i * bar_ms for i in range(n)]
    close = np.empty(n)
    close[0] = base
    for i in range(1, n):
        sine = math.sin(2 * math.pi * i / period) * noise
        drift = trend
        shock = rng.normal(0, noise)
        close[i] = close[i - 1] * (1 + drift + sine + shock)

    # Derive OHLV from close with small realistic spreads
    spread = close * 0.002
    high  = close + rng.uniform(0, 1, n) * spread
    low   = close - rng.uniform(0, 1, n) * spread
    open_ = np.roll(close, 1); open_[0] = base
    volume = rng.uniform(50, 500, n)

    return {
        "t": [int(x) for x in t],
        "o": open_.tolist(),
        "h": high.tolist(),
        "l": low.tolist(),
        "c": close.tolist(),
        "v": volume.tolist(),
    }


@pytest.fixture(scope="session")
def flat_bars():
    """500 bars around a flat price — for smoke-testing all strategies."""
    return _make_bars(500, base=100.0, trend=0.0, noise=0.003)


@pytest.fixture(scope="session")
def trending_bars():
    """500 bars with a clear uptrend (+0.05% per bar)."""
    return _make_bars(500, base=100.0, trend=0.0005, noise=0.002, seed=7)


@pytest.fixture(scope="session")
def crypto_bars():
    """500 crypto-style bars (high volatility, fractional sizing)."""
    return _make_bars(500, base=30_000.0, trend=0.001, noise=0.01, seed=99)


@pytest.fixture(scope="session")
def real_bars():
    """10 000 real BTCUSDT M1 bars loaded from the committed Parquet testdata.

    These are the most-recent 10k rows from the 2M-row file so they cover a
    realistic range of market regimes.  Skips the test if the file is absent
    (e.g. bare-clone CI without LFS).
    """
    if not _PARQUET_PATH.exists():
        pytest.skip(f"real-data parquet missing: {_PARQUET_PATH}")
    try:
        import pyarrow.parquet as pq  # type: ignore
    except ImportError:
        pytest.skip("pyarrow not installed — skipping real-data tests")

    tbl = pq.read_table(_PARQUET_PATH, columns=["t", "o", "h", "l", "c", "v"])
    df  = tbl.to_pandas().tail(10_000)
    return {
        "t": df["t"].tolist(),
        "o": df["o"].tolist(),
        "h": df["h"].tolist(),
        "l": df["l"].tolist(),
        "c": df["c"].tolist(),
        "v": df["v"].tolist(),
    }
