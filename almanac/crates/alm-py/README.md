# alm-py

PyO3 Python bindings for the almanac backtesting engine.

## Build & install

```bash
# Install maturin (once)
pip install maturin

# Editable install into the active venv (fastest for development)
cd almanac/crates/alm-py
maturin develop --release

# Build a distributable wheel
maturin build --release
# → target/wheels/alm_py-0.1.0-*.whl
```

## API

### `run_backtest`

```python
import alm_py as alm

bars = {
    "t": [1_700_000_000_000, ...],   # Unix ms timestamps
    "o": [42000.0, ...],
    "h": [42500.0, ...],
    "l": [41800.0, ...],
    "c": [42300.0, ...],
    "v": [1.23, ...],
}

result = alm.run_backtest(
    symbol     = "BTCUSDT",
    strategy   = "rsi_mean_rev",
    params     = {"period": 14, "oversold": 30, "overbought": 70},
    bars       = bars,
    capital    = 10_000.0,
    commission = 0.001,
    slippage   = 0.0005,
    # lot_size = -1.0  # auto: 0.0 for crypto, 1.0 for stocks
)

print(result["sharpe_ratio"], result["total_return_pct"])

# DataFrame-friendly
import pandas as pd
equity = pd.DataFrame(result["equity_curve"])
trades = pd.DataFrame(result["trades"])
```

### `kalman`

```python
kf = alm.kalman(prices=df["close"].tolist(), q_pos=0.001, q_vel=0.001, r=1.0)
df["kf_value"]    = kf["value"]
df["kf_velocity"] = kf["velocity"]   # >0 = uptrend, <0 = downtrend
```

### `monte_carlo`

```python
mc = alm.monte_carlo(
    pnl_pct        = result["pnl_pct"],   # from run_backtest
    capital        = 10_000.0,
    n_iter         = 5_000,
    ruin_threshold = 0.5,                 # ruin if equity drops below 50%
    seed           = 42,                  # reproducible
)

print(f"Ruin probability: {mc['ruin_probability']:.1%}")
print(f"Median final equity: {mc['final']['p50']:.0f}")

# Plot percentile bands
import matplotlib.pyplot as plt
for pct in ["p5", "p25", "p50", "p75", "p95"]:
    plt.plot(mc["curves"][pct], label=pct)
plt.legend(); plt.show()
```

### `list_strategies`

```python
names = alm.list_strategies()
# ["ma_crossover", "rsi_mean_rev", "macd_crossover", ...]
```

## Loading data from Parquet

```python
import polars as pl

df = pl.read_parquet("data/BTCUSDT/2024.parquet")
bars = {
    "t": df["t"].to_list(),
    "o": df["o"].to_list(),
    "h": df["h"].to_list(),
    "l": df["l"].to_list(),
    "c": df["c"].to_list(),
    "v": df["v"].to_list(),
}
```
