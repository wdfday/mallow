import os

def analyze():
    filepath = "comparison_results.md"
    if not os.path.exists(filepath):
        print("comparison_results.md not found")
        return
        
    with open(filepath, "r", encoding="utf-8") as f:
        content = f.read()
        
    # Split by strategy divider
    strategies = content.split("|--- |--- |--- |--- |--- |--- |--- |--- |")
    
    print(f"{'Strategy':<25} | {'alm_py':<8} | {'vbt':<8} | {'backtrader':<8} | {'Status':<15}")
    print("-" * 75)
    
    for s in strategies:
        lines = [line.strip() for line in s.strip().split("\n") if line.strip()]
        if not lines:
            continue
            
        # We need the first line of the strategy which has the strategy name and Trades row
        # Example: | **ma_crossover** | `{'fast': 20, 'slow': 50}` | Trades | 15 | 15 | 15 | **✅ MATCH** |  |
        trades_line = None
        for line in lines:
            if "Trades" in line and "|" in line:
                trades_line = line
                break
                
        if not trades_line:
            continue
            
        parts = [p.strip() for p in trades_line.split("|")]
        if len(parts) >= 8:
            strategy_name = parts[1].replace("**", "").strip()
            if not strategy_name:
                continue
            alm = parts[4].strip()
            vbt = parts[5].strip()
            bt = parts[6].strip()
            status = parts[7].strip()
            
            # Check if it is a mismatch or if the numbers are different
            # Specifically we are interested in cases where vbt == bt but alm != bt
            if alm != bt and vbt == bt:
                print(f"{strategy_name:<25} | {alm:<8} | {vbt:<8} | {bt:<8} | {status:<15}")

if __name__ == "__main__":
    analyze()
