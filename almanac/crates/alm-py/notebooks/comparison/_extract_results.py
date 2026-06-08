import json
import re
import os
from _generate_cmp import STRATEGY_IMPLS

# ── Regex patterns ────────────────────────────────────────────────────────────
_NUM = r"([e\d\.\-\+N/Anan]+)"
_OPT = r"(?:,\s*sortino=" + _NUM + r")?" + \
       r"(?:,\s*maxdd="   + _NUM + r"%)?" + \
       r"(?:,\s*winrate="  + _NUM + r"%)?" + \
       r"(?:,\s*pf="       + _NUM + r")?"

def _pat(engine):
    return re.compile(
        engine + r":\s*(\d+)\s*trades,\s*return=" + _NUM + r"%?,\s*sharpe=" + _NUM + _OPT
    )

alm_named_pat = _pat(r"alm_py\s*\(named\)")
alm_script_pat = _pat(r"alm_py\s*\(script\)")
vbt_pat  = _pat(r"vectorbt")
bt_pat   = _pat(r"backtrader")

_FIELDS = ["trades", "return", "sharpe", "sortino", "maxdd", "winrate", "pf"]
_NA     = {f: "N/A" for f in _FIELDS}
_ERR    = {f: "ERROR" for f in _FIELDS}

def _parse(m) -> dict:
    if not m:
        return dict(_NA)
    groups = m.groups()
    r = {"trades": groups[0], "return": groups[1], "sharpe": groups[2]}
    r["sortino"] = groups[3] if groups[3] else "N/A"
    r["maxdd"]   = groups[4] if groups[4] else "N/A"
    r["winrate"] = groups[5] if groups[5] else "N/A"
    r["pf"]      = groups[6] if groups[6] else "N/A"
    return r


def parse_notebook_results():
    results = {}

    for name, impl in STRATEGY_IMPLS.items():
        nb_file = f"cmp_{name}.ipynb"
        params_str = str(impl.get("params", {}))

        results[name] = {
            "params": params_str,
            "alm_named": dict(_NA),
            "alm_script": dict(_NA),
            "vbt": dict(_NA),
            "bt":  dict(_NA),
            "error": None,
        }

        if not os.path.exists(nb_file):
            continue

        try:
            with open(nb_file, "r", encoding="utf-8") as f:
                nb = json.load(f)

            for cell in nb.get("cells", []):
                if cell.get("cell_type") != "code":
                    continue

                for out in cell.get("outputs", []):
                    if out.get("output_type") == "stream" and "text" in out:
                        text_lines = out["text"]
                        if isinstance(text_lines, str):
                            text_lines = [text_lines]
                        for line in text_lines:
                            m = alm_named_pat.search(line)
                            if m:
                                results[name]["alm_named"] = _parse(m)
                            m = alm_script_pat.search(line)
                            if m:
                                results[name]["alm_script"] = _parse(m)
                            m = vbt_pat.search(line)
                            if m:
                                results[name]["vbt"] = _parse(m)
                            m = bt_pat.search(line)
                            if m:
                                results[name]["bt"] = _parse(m)

                    elif out.get("output_type") == "error":
                        ename  = out.get("ename", "Error")
                        evalue = out.get("evalue", "")
                        src    = "".join(cell.get("source", []))
                        key = ("vbt" if "vbt_run"          in src else
                               "bt"  if "BtStrat"          in src else
                               "alm_named" if "alm_py.run_backtest" in src else
                               "alm_script" if "run_script_backtest" in src else None)
                        if key:
                            results[name][key] = dict(_ERR)
                            results[name]["error"] = f"{ename}: {evalue}"

        except Exception as e:
            results[name]["error"] = f"Failed to parse notebook: {e}"

    # ── Markdown output ───────────────────────────────────────────────────────
    md = []
    md.append("# Bảng so sánh chiến lược — alm_py (named) · alm_py (script) · vectorbt · backtrader")
    md.append("\nDữ liệu: BTCUSDT M1, 20 000 bars. Commission=0, Slippage=0.")
    md.append("Annualization: alm ~365, bt factor=365, vbt freq=1min (≡ sqrt(525960)).\n")

    # Header
    md.append("| Strategy | Params | Metric | alm_py (named) | alm_py (script) | vectorbt | backtrader | Match (alm vs bt/vbt) | Note |")
    md.append("| :--- | :--- | :--- | :---: | :---: | :---: | :---: | :---: | :--- |")

    for name, data in results.items():
        alm_named = data["alm_named"]
        alm_script = data["alm_script"]
        vbt = data["vbt"]
        bt  = data["bt"]
        params = data["params"]
        err    = data["error"] or ""

        # Match logic: prefer bt; if bt=N/A/0 fall back to vbt
        match_status = "⚠️ N/A"
        if alm_named["trades"] == "ERROR" or alm_script["trades"] == "ERROR" or bt["trades"] == "ERROR":
            match_status = "❌ ERROR"
        elif alm_named["trades"] != "N/A":
            try:
                a_named = int(alm_named["trades"])
                a_script = int(alm_script["trades"]) if alm_script["trades"] != "N/A" else a_named
                
                parity_ok = (a_named == a_script)
                
                b_str = bt["trades"]
                ref = "bt"
                if b_str in ("N/A", "ERROR", "0") and vbt["trades"] not in ("N/A", "ERROR"):
                    b_str = vbt["trades"]
                    ref = "vbt"
                
                if b_str in ("N/A", "ERROR"):
                    if parity_ok:
                        match_status = "✅ PARITY"
                    else:
                        match_status = f"❌ PARITY FAIL ({a_named} vs {a_script})"
                else:
                    b = int(b_str)
                    diff_named = abs(a_named - b)
                    suffix = " vs vbt" if ref == "vbt" else ""
                    if parity_ok:
                        if diff_named == 0:
                            match_status = f"✅ MATCH{suffix}"
                        elif diff_named <= 2:
                            match_status = f"✅ MATCH (diff={a_named - b:+d}){suffix}"
                        else:
                            match_status = f"❌ MISMATCH ({a_named} vs {b}){suffix}"
                    else:
                        match_status = f"❌ PARITY FAIL ({a_named} vs {a_script}){suffix}"
            except ValueError:
                pass

        def _v(d, key, suffix=""):
            v = d.get(key, "N/A")
            return f"{v}{suffix}" if v not in ("N/A", "ERROR", "nan") else v

        md.append(f"| **{name}** | `{params}` | Trades    | {alm_named['trades']} | {alm_script['trades']} | {vbt['trades']} | {bt['trades']} | **{match_status}** | {err} |")
        md.append(f"| | | Return %  | {_v(alm_named,'return','%')} | {_v(alm_script,'return','%')} | {_v(vbt,'return','%')} | {_v(bt,'return','%')} | | |")
        md.append(f"| | | Sharpe    | {_v(alm_named,'sharpe')} | {_v(alm_script,'sharpe')} | {_v(vbt,'sharpe')} | {_v(bt,'sharpe')} | | |")
        md.append(f"| | | Sortino   | {_v(alm_named,'sortino')} | {_v(alm_script,'sortino')} | {_v(vbt,'sortino')} | {_v(bt,'sortino')} | | |")
        md.append(f"| | | Max DD %  | {_v(alm_named,'maxdd','%')} | {_v(alm_script,'maxdd','%')} | {_v(vbt,'maxdd','%')} | {_v(bt,'maxdd','%')} | | |")
        md.append(f"| | | Win Rate  | {_v(alm_named,'winrate','%')} | {_v(alm_script,'winrate','%')} | {_v(vbt,'winrate','%')} | {_v(bt,'winrate','%')} | | |")
        md.append(f"| | | Prof. Fac | {_v(alm_named,'pf')} | {_v(alm_script,'pf')} | {_v(vbt,'pf')} | {_v(bt,'pf')} | | |")
        md.append("|---|---|---|---|---|---|---|---|---|")

    output_path = "comparison_results.md"
    with open(output_path, "w", encoding="utf-8") as f:
        f.write("\n".join(md))

    print(f"Đã xuất kết quả ra: {output_path}")

    # ── Summary stats ─────────────────────────────────────────────────────────
    matched = mismatched = na_count = 0
    for data in results.values():
        alm_named = data["alm_named"]
        bt  = data["bt"]
        vbt = data["vbt"]
        if alm_named["trades"] in ("N/A", "ERROR") or (bt["trades"] in ("N/A", "ERROR") and vbt["trades"] in ("N/A", "ERROR")):
            na_count += 1
            continue
        try:
            a = int(alm_named["trades"])
            b_str = bt["trades"]
            if b_str in ("N/A", "ERROR", "0") and vbt["trades"] not in ("N/A", "ERROR"):
                b_str = vbt["trades"]
            
            if b_str in ("N/A", "ERROR"):
                na_count += 1
            elif abs(a - int(b_str)) <= 2:
                matched += 1
            else:
                mismatched += 1
        except ValueError:
            na_count += 1

    total = matched + mismatched + na_count
    print(f"\nSummary: {matched}/{total} MATCH, {mismatched} MISMATCH, {na_count} N/A")


if __name__ == "__main__":
    parse_notebook_results()
