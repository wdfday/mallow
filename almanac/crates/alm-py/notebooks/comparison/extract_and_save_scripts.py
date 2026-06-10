import os
import re
import json

strategy_dir = "/Users/Giap/RustroverProjects/mallow/almanac/crates/strategy/src/named"
scratch_dir = "/Users/Giap/.gemini/antigravity/scratch"
os.makedirs(scratch_dir, exist_ok=True)

target_strategies = [
    'atr_trailing', 'bb_keltner_squeeze', 'bb_squeeze', 'chandelier_exit', 
    'elder_ray', 'heiken_ashi_breakout', 'heiken_ashi_color', 'highest_breakout', 
    'kdj', 'ma_pullback', 'mean_reversion', 'orb_breakout', 'oscillator_overlord', 
    'price_action_swing', 'volatility_squeezer', 'volatility_vanguard', 'waddah_attar'
]

extracted = {}

for root, dirs, files in os.walk(strategy_dir):
    for file in files:
        if not file.endswith(".rs"):
            continue
        path = os.path.join(root, file)
        
        # Check if the file name contains any target strategy base name
        base = os.path.basename(path).replace(".rs", "").replace("_strategy", "")
        # map special names
        if base == "atr_trailing":
            match_name = "atr_trailing"
        elif base == "sar":
            match_name = "parabolic_sar"
        else:
            match_name = base
            
        is_target = False
        target_name = ""
        for t in target_strategies:
            if t == match_name or t.replace("_", "") == match_name.replace("_", "") or match_name in t:
                is_target = True
                target_name = t
                break
        
        if not is_target:
            continue
            
        with open(path, "r", encoding="utf-8") as f:
            content = f.read()
            
        # Look for let script = r#" ... "#; or const RHAI ... = r#" ... "#;
        # We can extract anything between let script = r#" and "#;
        script_matches = re.findall(r'let\s+script\s*=\s*r#"(.*?)"#;', content, re.DOTALL)
        if not script_matches:
            # try const RHAI
            script_matches = re.findall(r'const\s+(?:RHAI[A-Z0-9_]*|SCRIPT[A-Z0-9_]*):\s*&str\s*=\s*r#"(.*?)"#;', content, re.DOTALL)
        if not script_matches:
            # try Some(r#"..."#) in fn script
            script_matches = re.findall(r'fn script\(.*?Some\(\s*r#"(.*?)"#\s*\)', content, re.DOTALL)
            
        if script_matches:
            extracted[target_name] = script_matches[0].strip()
            print(f"Extracted script for: {target_name}")

# Write to markdown
md_path = os.path.join(scratch_dir, "extracted_rhai_scripts.md")
with open(md_path, "w", encoding="utf-8") as f:
    f.write("# Extracted Rhai Scripts from Rust Source Files\n\n")
    for name, script in sorted(extracted.items()):
        f.write(f"## {name}\n")
        f.write(f"```javascript\n{script}\n```\n\n")

print(f"Saved {len(extracted)} scripts to {md_path}")
