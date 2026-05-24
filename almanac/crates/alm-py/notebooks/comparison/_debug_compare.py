import numpy as np
import pandas as pd
import pandas_ta as ta
import alm_py
from _shared import load_parquet, make_bars

def debug_rsi_mean_rev():
    # 1. Load data
    try:
        bars, df = load_parquet('BTCUSDT', 'H1', 'BinanceFlat', n=1000)
        print("Đã load dữ liệu Binance H1.")
    except Exception:
        bars, df = make_bars(n=1000, seed=42)
        print("Sử dụng dữ liệu synthetic.")

    # 2. Chạy alm_py
    params = {"period": 14, "oversold": 30.0, "overbought": 70.0}
    alm_res = alm_py.run_backtest(
        "BTCUSDT", "rsi_mean_rev", params, bars,
        initial_capital=10000.0, commission_pct=0.001, slippage_pct=0.0005,
        lot_size=0.0, strength_sizing=False
    )

    # 3. Tính toán bằng pandas_ta
    close = df['close']
    ta_rsi = ta.rsi(close, length=14)
    
    # 4. Lấy RSI từ alm_py
    # Chỉ báo RSI trong alm_py được lưu trong 'indicator_series'
    # Cấu trúc: {'rsi14': {'t': [...], 'v': [...]}}
    alm_ind_series = alm_res.get("indicator_series", {})
    rsi_key = [k for k in alm_ind_series.keys() if "rsi" in k.lower()]
    if not rsi_key:
        print("Không tìm thấy series RSI trong alm_py. Các series hiện có:", list(alm_ind_series.keys()))
        return
    
    rsi_key = rsi_key[0]
    alm_rsi_t = alm_ind_series[rsi_key]["t"]
    alm_rsi_v = alm_ind_series[rsi_key]["v"]
    
    # Map time sang index của DataFrame để đối chiếu chính xác
    alm_rsi_series = pd.Series(index=pd.to_datetime(alm_rsi_t, unit="ms", utc=True), data=alm_rsi_v)
    
    # Tạo DataFrame đối chiếu
    cmp_df = pd.DataFrame({
        "close": df["close"],
        "ta_rsi": ta_rsi,
        "alm_rsi": alm_rsi_series
    })
    
    # Lọc bỏ phần chưa warm-up (NaN)
    cmp_df = cmp_df.dropna(subset=["ta_rsi", "alm_rsi"])
    cmp_df["diff"] = cmp_df["ta_rsi"] - cmp_df["alm_rsi"]
    cmp_df["abs_diff"] = cmp_df["diff"].abs()

    print("\n--- SO SÁNH GIÁ TRỊ CHỈ BÁO RSI (10 dòng đầu sau warm-up) ---")
    print(cmp_df[["close", "ta_rsi", "alm_rsi", "diff"]].head(10))

    max_diff = cmp_df["abs_diff"].max()
    mean_diff = cmp_df["abs_diff"].mean()
    print(f"\nSai số lớn nhất giữa 2 bên: {max_diff:.6f}")
    print(f"Sai số trung bình: {mean_diff:.6f}")

    # 5. So sánh tín hiệu giao dịch (Signals)
    # Python signals
    ta_entries = cmp_df["ta_rsi"] < 30.0
    ta_exits = cmp_df["ta_rsi"] > 70.0

    # Lấy các lệnh thực tế từ alm_py
    alm_trades = alm_res.get("trades", [])
    print(f"\nSố lượng trade của alm_py: {len(alm_trades)}")
    
    # In một số trade đầu tiên của alm_py
    if alm_trades:
        print("3 trade đầu tiên của alm_py:")
        for t in alm_trades[:3]:
            entry_dt = pd.to_datetime(t["entry_ts"], unit="ms", utc=True)
            exit_dt = pd.to_datetime(t["exit_ts"], unit="ms", utc=True)
            print(f"  - Vào: {entry_dt} ({t['entry_price']:.2f}), Ra: {exit_dt} ({t['exit_price']:.2f}), PnL: {t['pnl']:.2f}")

    # Tạo mảng tín hiệu của alm_py từ danh sách trade
    alm_entries = pd.Series(False, index=cmp_df.index)
    for t in alm_trades:
        entry_dt = pd.to_datetime(t["entry_ts"], unit="ms", utc=True)
        if entry_dt in alm_entries.index:
            alm_entries.loc[entry_dt] = True

    # Tìm các điểm lệch tín hiệu đầu tiên
    print("\n--- PHÂN TÍCH LỆCH PHA TÍN HIỆU VÀO LỆNH (ENTRIES) ---")
    discrepancies = 0
    for idx, row in cmp_df.iterrows():
        py_sig = ta_entries.loc[idx]
        alm_sig = alm_entries.loc[idx]
        if py_sig != alm_sig:
            print(f"Thời gian: {idx.date()} {idx.time()} | Giá: {row['close']:.2f}")
            print(f"  -> pandas_ta (RSI={row['ta_rsi']:.2f}) -> Signal: {py_sig}")
            print(f"  -> alm_py    (RSI={row['alm_rsi']:.2f}) -> Signal: {alm_sig}")
            discrepancies += 1
            if discrepancies >= 5:
                print("... chỉ in ra 5 điểm lệch đầu tiên.")
                break

if __name__ == "__main__":
    debug_rsi_mean_rev()
