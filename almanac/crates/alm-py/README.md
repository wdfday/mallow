# alm-py

PyO3 Python bindings for the almanac backtesting engine.

## Build & install

Dự án hỗ trợ các lệnh just để tự động hóa toàn bộ quá trình thiết lập môi trường ảo và biên dịch:

### 1. Khởi tạo môi trường ảo (venv)
Khởi tạo môi trường ảo .venv tại thư mục crates/alm-py (lệnh này sẽ tự động phát hiện và ưu tiên sử dụng Python 3.13 hoặc 3.12 nếu có sẵn trên máy):
```bash
just init
```

### 2. Cài đặt các thư viện phụ thuộc
Cài đặt nâng cấp pip, công cụ maturin và tất cả các thư viện Python liên quan (numpy, pandas, vectorbt, pytest, jupyter...):
```bash
just install-deps
```

### 3. Biên dịch thư viện bindings (alm_py)
Biên dịch mã nguồn Rust và cài đặt trực tiếp vào môi trường ảo .venv:
- Đối với chế độ Release (khuyên dùng để chạy đối soát tối ưu hiệu năng):
  ```bash
  just build-release
  ```
- Đối với chế độ Debug (biên dịch nhanh hơn):
  ```bash
  just build
  ```

## Testing & Pipeline

Dự án tích hợp bộ công cụ kiểm thử tự động và pipeline đối soát 82 chiến lược giao dịch:

### 1. Chạy unit tests cho mã nguồn Python
Chạy toàn bộ các ca kiểm thử trong thư mục tests:
```bash
just test
```

Chạy một file kiểm thử cụ thể:
```bash
just test-file tests/test_backtest.py
```

### 2. Chạy toàn bộ pipeline đối soát (Jupyter Notebooks)
Tự động build release thư viện, khởi chạy 82 notebooks song song để so sánh hiệu năng giữa Rust engine, Vectorbt và Backtrader, sau đó ghi nhận kết quả ra tệp comparison_results.md:
```bash
just compare
```

### 3. Dọn dẹp các tệp tin rác và cache
Xoá bỏ các file notebooks tự động sinh ra (cmp_*.ipynb) và các thư mục cache của Python để làm sạch thư mục:
```bash
just clear
```

## Dòng lệnh thuần (Không dùng just)

Nếu hệ thống của anh/chị không cài đặt công cụ just, anh/chị có thể thực hiện tuần tự các dòng lệnh tương đương sau:

### 1. Khởi tạo môi trường ảo (venv)
```bash
# Sử dụng Python 3.13 hoặc 3.12 có sẵn trên máy
python3 -m venv .venv
```

### 2. Cài đặt các thư viện phụ thuộc và Maturin
```bash
.venv/bin/pip install --upgrade pip
.venv/bin/pip install maturin
.venv/bin/pip install -e .[dev]
```

### 3. Biên dịch thư viện bindings (alm_py)
```bash
# Build Release (Khuyên dùng)
.venv/bin/maturin develop --release

# Build Debug
.venv/bin/maturin develop
```

### 4. Chạy unit tests cho mã nguồn Python
```bash
.venv/bin/pytest tests/
# Chạy một file kiểm thử cụ thể
.venv/bin/pytest tests/test_backtest.py -v
```

### 5. Chạy pipeline đối soát và sinh kết quả
```bash
# Build release trước để tối ưu hóa thời gian chạy
.venv/bin/maturin develop --release
# Vào thư mục chứa pipeline đối soát để thực thi
cd notebooks/comparison
../../.venv/bin/python run_pipeline.py
```

### 6. Dọn dẹp tệp tin rác và cache
```bash
rm -f notebooks/comparison/cmp_*.ipynb
rm -rf .pytest_cache
find . -type d -name "__pycache__" -exec rm -rf {} +
```

### Lưu ý đối với hệ điều hành Windows
Nếu sử dụng Windows, anh/chị cần lưu ý một số điểm khác biệt sau về cú pháp dòng lệnh:
1. Thay thế tất cả các đường dẫn `.venv/bin/...` trong các lệnh trên thành `.venv\Scripts\...` (sử dụng dấu gạch chéo ngược và thư mục Scripts thay vì bin). Ví dụ: `.venv\Scripts\pip` hoặc `.venv\Scripts\maturin`.
2. Đối với các lệnh dọn dẹp hệ thống ở bước 6:
   - Nếu sử dụng Git Bash: Giữ nguyên cú pháp Unix.
   - Nếu sử dụng PowerShell, chạy các lệnh sau:
     ```powershell
     Remove-Item -Path notebooks\comparison\cmp_*.ipynb -Force -ErrorAction SilentlyContinue
     Remove-Item -Path .pytest_cache -Recurse -Force -ErrorAction SilentlyContinue
     Get-ChildItem -Recurse -Directory -Filter "__pycache__" | Remove-Item -Recurse -Force
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
