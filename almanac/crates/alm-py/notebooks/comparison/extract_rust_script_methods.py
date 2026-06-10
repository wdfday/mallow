import os
import re

strategy_dir = "/Users/Giap/RustroverProjects/mallow/almanac/crates/strategy/src/named"

target_files = [
    "volatility/volatility_squeezer.rs",
    "volatility/volatility_vanguard.rs",
    "volatility/bb_squeeze.rs",
    "volatility/chandelier_exit.rs",
    "volatility/donchian_breakout.rs",
    "pattern/elder_ray_strategy.rs",
    "volatility/highest_breakout.rs",
    "momentum/kdj_strategy.rs",
    "trend/ma_pullback.rs",
    "composite/mean_reversion.rs",
    "composite/oscillator_overlord.rs",
    "volume/waddah_attar.rs",
    "composite/bb_keltner_squeeze.rs", # might be composite or volatility, let's see
]

# Just walk and find files matching the name
for root, dirs, files in os.walk(strategy_dir):
    for file in files:
        if not file.endswith(".rs"):
            continue
        path = os.path.join(root, file)
        
        # Check if this file is one of our targets (match by basename)
        base = os.path.basename(path)
        is_target = any(t.endswith(base) for t in target_files)
        if not is_target:
            continue
            
        with open(path, "r", encoding="utf-8") as f:
            content = f.read()
            
        if "fn script" in content:
            print(f"\n==================================================")
            print(f"File: {os.path.relpath(path, strategy_dir)}")
            
            # Find the fn script implementation and print around it
            # e.g., fn script(&self) -> ... { ... }
            # Let's search for fn script and extract about 20 lines after it
            idx = content.find("fn script")
            end_idx = content.find("}", idx)
            # Find matching brace
            brace_count = 0
            started = False
            brace_idx = idx
            for i in range(idx, len(content)):
                if content[i] == "{":
                    brace_count += 1
                    started = True
                elif content[i] == "}":
                    brace_count -= 1
                if started and brace_count == 0:
                    brace_idx = i
                    break
            
            fn_body = content[idx:brace_idx+1]
            print(fn_body)
            
            # Also find any const RHAI or similar variables near the top
            rhai_consts = re.findall(r'(const\s+RHAI[A-Z0-9_]*.*?;)', content, re.DOTALL)
            if rhai_consts:
                print("--- Constants found ---")
                for c in rhai_consts:
                    print(c.strip())
