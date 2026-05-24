import os

def main():
    filepath = "comparison_results.md"
    if not os.path.exists(filepath):
        print(f"{filepath} not found")
        return
        
    print("=== STRATEGIES THAT DO NOT MATCH EXACTLY OR HAVE ERRORS ===")
    with open(filepath, "r", encoding="utf-8") as f:
        lines = f.readlines()
        
    for line in lines:
        if "|" in line and ("MISMATCH" in line or "ERROR" in line or "N/A" in line):
            # Check if it is a strategy row
            parts = [p.strip() for p in line.split("|")]
            if len(parts) >= 8 and parts[1]: # has strategy name
                strategy_name = parts[1].replace("**", "")
                params = parts[2]
                match_status = parts[7]
                note = parts[8] if len(parts) > 8 else ""
                print(f"Strategy: {strategy_name:<25} | Match: {match_status:<25} | Params: {params} | Note: {note}")

if __name__ == "__main__":
    main()
