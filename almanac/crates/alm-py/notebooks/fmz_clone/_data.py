"""Helper load BTCUSDT testdata trong almanac/crates/data/testdata/."""
from __future__ import annotations
import glob, pathlib
import pandas as pd

# fmz_clone → notebooks → alm-py → crates → almanac → mallow
REPO_ROOT = pathlib.Path(__file__).resolve().parents[5]
TESTDATA  = REPO_ROOT / "almanac/crates/data/testdata"


def load_testdata(symbol: str = "BTCUSDT", tf: str = "M5", n: int | None = 5000):
    """Load BTCUSDT testdata. Returns (alm_bars dict, bt_df DataFrame).

    `n` = số bar cuối (None = full).
    """
    files = sorted(glob.glob(str(TESTDATA / symbol / tf / "*.parquet")))
    if not files:
        raise FileNotFoundError(f"No parquet found at {TESTDATA / symbol / tf}")
    df = pd.concat([pd.read_parquet(f) for f in files], ignore_index=True)
    df = df.drop_duplicates(subset="t").sort_values("t").reset_index(drop=True)
    keep = ["t", "o", "h", "l", "c", "v"]
    df = df[[c for c in keep if c in df.columns]].copy()
    if n is not None:
        df = df.tail(n).reset_index(drop=True)

    alm_bars = {
        "t": df["t"].astype("int64").tolist(),
        "o": df["o"].astype(float).tolist(),
        "h": df["h"].astype(float).tolist(),
        "l": df["l"].astype(float).tolist(),
        "c": df["c"].astype(float).tolist(),
        "v": df["v"].astype(float).tolist(),
    }
    bt_df = pd.DataFrame({
        "open":   df["o"].astype(float).values,
        "high":   df["h"].astype(float).values,
        "low":    df["l"].astype(float).values,
        "close":  df["c"].astype(float).values,
        "volume": df["v"].astype(float).values,
    }, index=pd.to_datetime(df["t"].values, unit="ms", utc=True))
    bt_df.index.name = "datetime"
    return alm_bars, bt_df
