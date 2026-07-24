// Ported from mallow-client/lib/almanac-wasm.ts — lazy client for the `alm-wasm` module.
// Confirmed via source (almanac/crates/alm-wasm/src/lib.rs, chart_state.rs): every function
// here is pure Rust compute over passed-in arrays/strings, zero network calls — no herald
// fallback needed (unlike mallow-client's editor, which tries WASM first and falls back to
// herald HTTP only as a resilience measure; fathom has no backend to fall back to, and doesn't
// need one).
//
// Build: cd almanac/crates/alm-wasm && wasm-pack build --target bundler, then copy pkg/ into
// fathom-tde/vendor/alm-wasm/ (package.json depends on it via "link:./vendor/alm-wasm").

export interface OhlcvBar {
  time: number // Unix seconds
  open: number
  high: number
  low: number
  close: number
  volume: number
}

interface OhlcvArrays {
  t: number[]
  o: number[]
  h: number[]
  l: number[]
  c: number[]
  v: number[]
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type WasmModule = any

let _wasm: WasmModule | null = null
let _initPromise: Promise<WasmModule> | null = null

export async function loadWasm(): Promise<WasmModule> {
  if (typeof window === 'undefined') throw new Error('alm-wasm is browser-only')
  if (_wasm) return _wasm
  if (_initPromise) return _initPromise
  _initPromise = (async () => {
    const mod = await import('alm-wasm')
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    if (typeof (mod as any).default === 'function') await (mod as any).default()
    _wasm = mod
    return mod
  })()
  return _initPromise
}

export function barsToArrays(bars: OhlcvBar[]) {
  const n = bars.length
  const t = new Float64Array(n), o = new Float64Array(n), h = new Float64Array(n)
  const l = new Float64Array(n), c = new Float64Array(n), v = new Float64Array(n)
  for (let i = 0; i < n; i++) {
    t[i] = bars[i].time * 1000; o[i] = bars[i].open; h[i] = bars[i].high
    l[i] = bars[i].low; c[i] = bars[i].close; v[i] = bars[i].volume
  }
  return { t, o, h, l, c, v }
}

export function arraysToBars(arrays: OhlcvArrays): OhlcvBar[] {
  return arrays.t.map((ts, i) => ({
    time: Math.floor(ts / 1000), open: arrays.o[i], high: arrays.h[i],
    low: arrays.l[i], close: arrays.c[i], volume: arrays.v[i],
  }))
}

// ── Indicator catalog ────────────────────────────────────────────────────────

export interface IndicatorCatalogEntry {
  name: string
  label: string
  category: string
  description: string
  overlay: boolean
  params: { name: string; type: string; default: unknown; description?: string }[]
  outputs: { name: string; type: string }[]
  mainpane: string[]
  subpanes: string[][]
}

export async function getIndicatorCatalog(): Promise<IndicatorCatalogEntry[]> {
  const wasm = await loadWasm()
  return wasm.indicator_catalog() as IndicatorCatalogEntry[]
}

// ── Script validation ─────────────────────────────────────────────────────────

export interface ScriptDiagnostic {
  severity: 'error' | 'warning' | 'info'
  message: string
  line?: number
  col?: number
}

export interface ScriptValidation {
  errors: ScriptDiagnostic[]
}

export async function validateScript(script: string, baseTf: string): Promise<ScriptValidation> {
  const wasm = await loadWasm()
  const result = wasm.validate_script(script, baseTf)
  if (result?.error) throw new Error(result.error)
  return (result ?? { errors: [] }) as ScriptValidation
}

// ── HTF probe ──────────────────────────────────────────────────────────────────

/** Higher timeframes an `ind.TYPE(period, "TF")` line in `script` declares, e.g. ["H1","H4"]. */
export async function probeScriptHtfs(script: string): Promise<string[]> {
  const wasm = await loadWasm()
  return (wasm.probe_script_htfs(script) ?? []) as string[]
}
