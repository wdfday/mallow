import json
import os
from _generate_cmp import STRATEGY_IMPLS

def main():
    failed_list = [
        "dmi_adx.ipynb",
        "wolfstein.ipynb",
        "alligator.ipynb",
        "tsi.ipynb",
        "connors_rsi.ipynb",
        "cmo_zero_cross.ipynb",
        "mfi_trend.ipynb",
        "mfi_revert.ipynb"
    ]
    
    for filename in failed_list:
        if not os.path.exists(filename):
            print(f"File not found: {filename}")
            continue
            
        with open(filename, "r", encoding="utf-8") as f:
            try:
                nb = json.load(f)
            except Exception as e:
                print(f"Error loading {filename}: {e}")
                continue
                
        print(f"\n=================== ERRORS IN {filename} ===================")
        has_error = False
        for cell in nb.get("cells", []):
            if cell.get("cell_type") != "code":
                continue
            outputs = cell.get("outputs", [])
            for out in outputs:
                if out.get("output_type") == "error":
                    has_error = True
                    ename = out.get("ename", "Error")
                    evalue = out.get("evalue", "")
                    traceback = out.get("traceback", [])
                    print(f"Source code block:\n{''.join(cell.get('source', []))}\n")
                    print(f"Error: {ename} - {evalue}")
                    print("Traceback:")
                    print("\n".join(traceback[:10])) # print first 10 lines of traceback
                    print("-" * 50)
        if not has_error:
            print("No execution error cell found, maybe it wasn't run or failed silently.")

if __name__ == "__main__":
    main()
