// Lightweight, bar-independent parse of `let NAME = ind.TYPE(args)` declarations — same regex
// convention monaco-script.ts's `varTypeMap` already trusts for field-completion/hover, extended
// to also capture the raw args string for display. Deliberately NOT the WASM ChartState route
// (that needs live bar data to produce a snapshot) — this only needs the script TEXT, so
// EditorPanel's IndicatorsRail can show "what's declared" independent of whether the chart has
// any data loaded at all.

export interface DeclaredIndicator {
  varName: string
  type: string
  /** Raw text between the parens, e.g. `14` or `50, "H1"` — not parsed further. */
  args: string
}

const DECL_RE = /\blet\s+([a-zA-Z_]\w*)\s*=\s*ind\.([a-zA-Z_]\w*)\s*\(([^)]*)\)/g

export function parseDeclaredIndicators(script: string): DeclaredIndicator[] {
  const out: DeclaredIndicator[] = []
  for (const m of script.matchAll(DECL_RE)) {
    out.push({ varName: m[1], type: m[2], args: m[3].trim() })
  }
  return out
}

/** Splits off a trailing quoted TF argument (`50, "H1"` → base args `50`, tf `H1`) by
 *  comma-splitting rather than a single fixed-shape regex, so multi-arg indicators
 *  (`ind.macd(12, 26, 9, "H1")`) split correctly too — only the LAST arg is ever a TF string in
 *  this DSL. `null` tf means the declaration is base-TF (no override). */
function splitTfArg(args: string): { baseArgs: string; tf: string | null } {
  const parts = args.split(',').map((s) => s.trim())
  const last = parts[parts.length - 1]
  const m = last?.match(/^"([A-Za-z0-9]+)"$/)
  if (m && parts.length > 1) {
    return { baseArgs: parts.slice(0, -1).join(', '), tf: m[1].toUpperCase() }
  }
  return { baseArgs: args, tf: null }
}

/**
 * Buckets a script's indicator declarations by the timeframe they actually run on — a base-TF
 * declaration (no explicit TF arg) goes under `baseTf`; an HTF one (`ind.ema(50, "H1")`) goes
 * under `"H1"`, REWRITTEN with the TF arg stripped (`ind.ema(50)`) since on that TF's own chart
 * tab, H1 effectively becomes the base timeframe.
 *
 * Why this exists: alm-wasm's `ChartState` (the on-chart preview engine) is single-TF only and
 * hard-rejects `set_script()` for ANY script containing an HTF reference at all — so feeding the
 * same full multi-TF script into every "Chart View" tab (one per TF) would make every single tab
 * fail identically, showing zero indicators anywhere. Each tab must instead get a
 * declarations-only mini-script containing ONLY ITS OWN TF's indicators — no `long`/`exit`/other
 * logic lines either (a v1 script never requires them; unset output vars just default to `false`,
 * see `script/v1/strategy.rs`), since those lines could reference an indicator that belongs to a
 * different TF's bucket and wouldn't compile standalone.
 */
export function groupIndicatorsByTimeframe(script: string, baseTf: string): Map<string, string[]> {
  const groups = new Map<string, string[]>()
  const base = baseTf.toUpperCase()
  for (const decl of parseDeclaredIndicators(script)) {
    const { baseArgs, tf } = splitTfArg(decl.args)
    const target = tf ?? base
    const line = `let ${decl.varName} = ind.${decl.type}(${baseArgs});`
    const bucket = groups.get(target)
    if (bucket) bucket.push(line)
    else groups.set(target, [line])
  }
  return groups
}

/** One declarations-only mini-script per requested timeframe (empty string if that TF has no
 *  indicators of its own — a valid, harmless no-op script). */
export function buildPerTfScripts(script: string, baseTf: string, timeframes: string[]): Record<string, string> {
  const groups = groupIndicatorsByTimeframe(script, baseTf)
  const out: Record<string, string> = {}
  for (const tf of timeframes) {
    out[tf] = (groups.get(tf.toUpperCase()) ?? []).join('\n')
  }
  return out
}
