import os
import re

strategy_dir = "/Users/Giap/RustroverProjects/mallow/almanac/crates/strategy/src/named"

strategy_scripts = {}

for root, dirs, files in os.walk(strategy_dir):
    for file in files:
        if not file.endswith(".rs"):
            continue
        path = os.path.join(root, file)
        with open(path, "r", encoding="utf-8") as f:
            content = f.read()
            
        # Check if the file implements fn script
        if "fn script" in content:
            # Find the strategy name
            name_match = re.search(r'fn name\(&self\) -> &str \{\s*"([^"]+)"', content)
            if not name_match:
                name_match = re.search(r'impl Strategy for ([a-zA-Z0-9_]+)', content)
            
            if name_match:
                strat_name = name_match.group(1)
                
                # Check for direct string return, e.g. Some(r#"..."#) or Some("...")
                direct_match = re.search(r'fn script\(&self\) -> Option<&\'static str> \{\s*Some\(\s*(r#".*?"#|".*?")\s*\)\s*\}', content, re.DOTALL)
                if not direct_match:
                     direct_match = re.search(r'fn script\(&self\) -> Option<&\s*\'static\s*str>\s*\{\s*Some\(\s*(r#".*?"#|".*?")\s*\)\s*\}', content, re.DOTALL)
                
                script_body = "Not found"
                if direct_match:
                    script_body = direct_match.group(1)
                else:
                    # Check for variable/constant return, e.g. Some(VAR)
                    some_match = re.search(r'fn script\(&self\) -> Option<&\'static str> \{\s*Some\(([^)]+)\)', content)
                    if not some_match:
                        some_match = re.search(r'fn script\(&self\) -> Option<&\s*\'static\s*str>\s*\{\s*Some\(([^)]+)\)', content)
                    
                    if some_match:
                        script_var = some_match.group(1).strip()
                        # Search for the variable definition: const VAR... = r#"..."# or "..."
                        var_pat = r'const\s+' + re.escape(script_var) + r'(?::\s*&str)?\s*=\s*(r#".*?"#|".*?")\s*;'
                        var_match = re.search(var_pat, content, re.DOTALL)
                        if var_match:
                            script_body = var_match.group(1)
                        else:
                            # Search for raw variable definition without types
                            var_pat2 = r'const\s+' + re.escape(script_var) + r'\s*=\s*(.*?);'
                            var_match2 = re.search(var_pat2, content, re.DOTALL)
                            if var_match2:
                                script_body = var_match2.group(1).strip()
                
                # Clean up formatting
                if script_body.startswith('r#"') and script_body.endswith('"#'):
                    script_body = script_body[3:-2].strip()
                elif script_body.startswith('"') and script_body.endswith('"'):
                    script_body = script_body[1:-1].strip()
                
                strategy_scripts[strat_name] = {
                    "body": script_body,
                    "file": os.path.relpath(path, strategy_dir)
                }

print(f"Total strategies implementing 'fn script': {len(strategy_scripts)}")
for name, info in sorted(strategy_scripts.items()):
    if info['body'] == "Not found":
        print(f"[!] Strategy: {name} (in {info['file']}) - Script body extraction failed.")
    else:
        print(f"Strategy: {name} (in {info['file']}) - Script found.")
