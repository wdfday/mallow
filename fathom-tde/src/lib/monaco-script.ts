import type * as Monaco from 'monaco-editor'
import { getIndicatorCatalog, type IndicatorCatalogEntry } from './almanac-wasm'

// Ported from mallow-client/lib/monaco-script.ts — Monarch tokenizer, language config,
// completion (static + WASM-backed `ind.*` catalog), hover. `alm-wasm` is now wired in (see
// almanac-wasm.ts) so the catalog-dependent branches that were previously dropped are restored.
// NOT ported: the document formatter (lower priority, still deferred).

export const SCRIPT_LANGUAGE_ID = 'script'

const MAX_SUGGESTIONS = 5
const MIN_PREFIX = 2

// Catalog loaded once from WASM (client-side, no server round-trip). Concurrent callers must
// await the SAME in-flight fetch — see mallow-client's original comment on this exact race.
let CATALOG: IndicatorCatalogEntry[] = []
let catalogPromise: Promise<void> | null = null
function ensureCatalog(): Promise<void> {
  if (!catalogPromise) {
    catalogPromise = getIndicatorCatalog()
      .then((cat) => { CATALOG = cat })
      .catch(() => { catalogPromise = null })
  }
  return catalogPromise
}

const FUNCTIONS: Array<[string, string, string]> = [
  ['cross_above', 'cross_above(a, b)', 'True the bar `a` crosses above `b`'],
  ['cross_below', 'cross_below(a, b)', 'True the bar `a` crosses below `b`'],
  ['crossed', 'crossed(a, b)', 'Cross in either direction'],
  ['rising', 'rising(a)', '`a[0] > a[1]`'],
  ['falling', 'falling(a)', '`a[0] < a[1]`'],
  ['rising_n', 'rising_n(a, n)', 'Strictly rising over the last `n` bars'],
  ['falling_n', 'falling_n(a, n)', 'Strictly falling over the last `n` bars'],
  ['above', 'above(a, b)', '`a[0] > b[0]`'],
  ['below', 'below(a, b)', '`a[0] < b[0]`'],
  ['in_range', 'in_range(v, lo, hi)', '`lo <= v <= hi`'],
  ['within', 'within(a, b, tol)', '`|a - b| <= tol` — tolerant float equality'],
  ['flag', 'flag(x)', 'Coerce a 0/1 bool-semantic field into a real bool (`x > 0.5`)'],
  ['slope', 'slope(a)', 'Average per-bar change across the buffer'],
  ['momentum', 'momentum(a, n)', '`a[0] - a[n]`'],
  ['pct_change', 'pct_change(a, n)', '`(a[0] - a[n]) / a[n]` (ROC)'],
  ['highest', 'highest(a, n)', 'Highest value in `a[0..n]`'],
  ['lowest', 'lowest(a, n)', 'Lowest value in `a[0..n]`'],
  ['avg', 'avg(a)', 'Arithmetic mean'],
  ['sum', 'sum(a)', 'Sum'],
  ['stdev', 'stdev(a, n)', 'Population standard deviation over `n` bars'],
  ['zscore', 'zscore(a, n)', '`(a[0] - mean) / stdev`'],
  ['sign', 'sign(x)', '→ -1 / 0 / 1'],
  ['abs', 'abs(x)', 'Absolute value'],
  ['sqrt', 'sqrt(x)', 'Square root'],
  ['pow', 'pow(v, e)', '`v` to the power `e`'],
  ['round', 'round(v)', 'Round to nearest integer'],
  ['floor', 'floor(v)', 'Round down'],
  ['ceil', 'ceil(v)', 'Round up'],
  ['min', 'min(a, b)', 'Minimum'],
  ['max', 'max(a, b)', 'Maximum'],
  ['clamp', 'clamp(v, lo, hi)', 'Clamp `v` to `[lo, hi]`'],
]

const TA_FUNCTIONS: Array<[string, string[], string]> = [
  ['ema', ['period', 'value'], 'Exponential moving average, incremental (state kept per call-site key)'],
  ['smma', ['period', 'value'], 'Smoothed moving average, incremental'],
  ['sma', ['period', 'value'], 'Simple moving average, incremental'],
  ['rsum', ['period', 'value'], 'Rolling sum over the last `period` values'],
  ['stdev', ['period', 'value'], 'Rolling population standard deviation'],
  ['highest', ['period', 'value'], 'Highest value over the last `period` values'],
  ['lowest', ['period', 'value'], 'Lowest value over the last `period` values'],
  ['wma', ['period', 'value'], 'Weighted moving average, incremental'],
  ['hma', ['period', 'value'], 'Hull moving average, incremental'],
  ['decay', ['alpha', 'value'], "Exponential decay toward `value` — `alpha` is a smoothing coefficient, not a period"],
  ['vwma', ['period', 'value', 'weight'], 'Volume-weighted moving average'],
  ['reset', [], "Clear this key's accumulated state"],
]

const OUTPUT_VARS: Array<[string, string, string]> = [
  ['long', 'long = true;', 'Emit a long signal'],
  ['short', 'short = true;', 'Emit a short signal'],
  ['exit', 'exit = true;', 'Emit an exit signal'],
  ['entry', 'entry = true;', 'Legacy alias for `long`'],
  ['tp', 'tp = ', 'Take-profit price'],
  ['sl', 'sl = ', 'Stop-loss price'],
  ['strength', 'strength = ', 'Signal strength 0–1'],
  ['atr', 'atr = ', 'ATR forwarded for volatility sizing'],
  ['trail', 'trail = ', 'Trailing-stop fraction'],
  ['max_bars', 'max_bars = ', 'Time-based exit after N bars'],
  ['is_offset', 'is_offset = true;', 'tp/sl are offsets from fill price'],
  ['reason', 'reason = "', 'Signal reason tag'],
  ['trend', 'trend = "', 'Regime: trend label (regime block)'],
  ['trend_value', 'trend_value = ', 'Regime: trend raw value'],
  ['vol', 'vol = "', 'Regime: volatility label'],
  ['vol_value', 'vol_value = ', 'Regime: volatility raw value'],
]

const BAR_FIELDS = ['open', 'high', 'low', 'close', 'volume']
const KEYWORDS = ['let', 'const', 'fn', 'if', 'else', 'while', 'for', 'in', 'return', 'true', 'false']

// Map `let NAME = ind.TYPE(` declarations → indicator type, for field completion/hover.
function varTypeMap(text: string): Record<string, string> {
  const map: Record<string, string> = {}
  const re = /\blet\s+([a-zA-Z_]\w*)\s*=\s*ind\.([a-zA-Z_]\w*)\s*\(/g
  for (const m of text.matchAll(re)) map[m[1]] = m[2]
  return map
}

const SNIPPET = 4 as Monaco.languages.CompletionItemInsertTextRule // InsertAsSnippet

/** Levenshtein distance — fallback when prefix matching finds nothing/little. */
function editDistance(a: string, b: string): number {
  const m = a.length, n = b.length
  const dp: number[][] = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0))
  for (let i = 0; i <= m; i++) dp[i][0] = i
  for (let j = 0; j <= n; j++) dp[0][j] = j
  for (let i = 1; i <= m; i++) {
    for (let j = 1; j <= n; j++) {
      dp[i][j] = a[i - 1] === b[j - 1]
        ? dp[i - 1][j - 1]
        : 1 + Math.min(dp[i - 1][j - 1], dp[i - 1][j], dp[i][j - 1])
    }
  }
  return dp[m][n]
}

export function registerScriptLanguage(monaco: typeof Monaco) {
  if (monaco.languages.getLanguages().some((l) => l.id === SCRIPT_LANGUAGE_ID)) return
  monaco.languages.register({ id: SCRIPT_LANGUAGE_ID, extensions: ['.mallow'] })
  void ensureCatalog()

  monaco.languages.setMonarchTokensProvider(SCRIPT_LANGUAGE_ID, {
    keywords: [
      'let', 'const', 'if', 'else', 'while', 'loop', 'for', 'in',
      'return', 'break', 'continue', 'fn', 'true', 'false',
      'import', 'export', 'as', 'private', 'throw', 'try', 'catch',
      'switch', 'do', 'until', 'type',
    ],
    typeKeywords: ['int', 'float', 'bool', 'char', 'str', 'string', 'Array', 'Map'],
    builtins: [
      'ind', 'flag', 'cross_above', 'cross_below', 'crossed', 'rising', 'falling',
      'above', 'below', 'in_range', 'within', 'slope', 'momentum', 'pct_change',
      'highest', 'lowest', 'avg', 'sum', 'stdev', 'zscore', 'sign',
      'abs', 'min', 'max', 'floor', 'ceil', 'round', 'sqrt', 'pow', 'clamp', 'len', 'print',
    ],
    operators: [
      '=', '>', '<', '!', '~', '?', ':',
      '==', '<=', '>=', '!=', '&&', '||', '++', '--',
      '+', '-', '*', '/', '&', '|', '^', '%', '<<', '>>',
      '+=', '-=', '*=', '/=', '&=', '|=', '^=',
    ],
    symbols: /[=><!~?:&|+\-*/^%]+/,
    tokenizer: {
      root: [
        [/[a-zA-Z_]\w*/, { cases: { '@keywords': 'keyword', '@typeKeywords': 'type', '@builtins': 'predefined', '@default': 'identifier' } }],
        { include: '@whitespace' },
        [/\d*\.\d+([eE][-+]?\d+)?/, 'number.float'],
        [/0x[0-9a-fA-F]+/, 'number.hex'],
        [/\d+(_\d+)*/, 'number'],
        [/"([^"\\]|\\.)*$/, 'string.invalid'],
        [/"/, 'string', '@string_double'],
        [/'[^\\']'/, 'string'],
        [/@symbols/, { cases: { '@operators': 'operator', '@default': '' } }],
        [/[{}()[\]]/, '@brackets'],
        [/[,;]/, 'delimiter'],
        [/`/, 'string', '@template'],
      ],
      whitespace: [[/\s+/, 'white'], [/\/\/.*$/, 'comment'], [/\/\*/, 'comment', '@comment']],
      comment: [[/[^/*]+/, 'comment'], [/\*\//, 'comment', '@pop'], [/[/*]/, 'comment']],
      string_double: [[/[^\\"]+/, 'string'], [/\\./, 'string.escape'], [/"/, 'string', '@pop']],
      template: [[/[^`$\\]+/, 'string'], [/\$\{/, { token: 'string', next: '@template_expr' }], [/\\./, 'string.escape'], [/`/, 'string', '@pop']],
      template_expr: [[/\}/, { token: 'string', next: '@pop' }], { include: 'root' }],
    },
  })

  monaco.languages.setLanguageConfiguration(SCRIPT_LANGUAGE_ID, {
    comments: { lineComment: '//', blockComment: ['/*', '*/'] },
    brackets: [['{', '}'], ['[', ']'], ['(', ')']],
    autoClosingPairs: [
      { open: '{', close: '}' },
      { open: '[', close: ']' },
      { open: '(', close: ')' },
      { open: '"', close: '"', notIn: ['string'] },
    ],
    surroundingPairs: [
      { open: '{', close: '}' },
      { open: '[', close: ']' },
      { open: '(', close: ')' },
      { open: '"', close: '"' },
    ],
    wordPattern: /[A-Za-z_]\w*/,
    indentationRules: {
      increaseIndentPattern: /\{\s*(\/\/.*)?$/,
      decreaseIndentPattern: /^\s*\}/,
    },
  })

  monaco.languages.registerCompletionItemProvider(SCRIPT_LANGUAGE_ID, {
    triggerCharacters: ['.'],
    provideCompletionItems: async (model, position) => {
      const word = model.getWordUntilPosition(position)
      const range = { startLineNumber: position.lineNumber, endLineNumber: position.lineNumber, startColumn: word.startColumn, endColumn: word.endColumn }
      const lineText = model.getValueInRange({ startLineNumber: position.lineNumber, startColumn: 1, endLineNumber: position.lineNumber, endColumn: position.column })

      // ta.* — small static list, zero dependency on the WASM catalog.
      if (/\bta\.\s*\w*$/.test(lineText)) {
        const prefix = word.word.toLowerCase()
        const items = TA_FUNCTIONS
          .filter(([name]) => name.startsWith(prefix))
          .slice(0, MAX_SUGGESTIONS)
          .map(([name, params, doc]) => {
            const args = params.map((p, i) => `\${${i + 1}:${p}}`).join(', ')
            return {
              label: name,
              kind: monaco.languages.CompletionItemKind.Function,
              insertText: `${name}(${args})`,
              insertTextRules: SNIPPET,
              detail: `ta.${name}(${params.join(', ')})`,
              documentation: { value: doc },
              range,
            }
          })
        return { suggestions: items, incomplete: true }
      }

      // Field completion (`st.` / `macd.`) and `ind.` indicator-type completion both read
      // CATALOG — gate the WASM fetch behind an await only for these two contexts.
      const fieldCtx = lineText.match(/([a-zA-Z_]\w*)\s*(?:\[[^\]]*\])?\.\s*(\w*)$/)
      const isFieldCtx = !!fieldCtx && fieldCtx[1] !== 'ind' && fieldCtx[1] !== 'ta'
      const isIndCtx = /\bind\.\s*\w*$/.test(lineText)

      if (isFieldCtx || isIndCtx) {
        if (CATALOG.length === 0) await ensureCatalog()

        if (isFieldCtx) {
          const type = varTypeMap(model.getValue())[fieldCtx![1]]
          const entry = CATALOG.find((c) => c.name === type)
          if (entry) {
            const suggestions = entry.outputs.map((o) => ({
              label: o.type === 'bool' ? `${o.name} (bool)` : o.name,
              kind: monaco.languages.CompletionItemKind.Field,
              insertText: o.name,
              documentation: o.type === 'bool' ? 'Bool-semantic field (0/1) — compare `> 0.5` or wrap in `flag(...)`' : 'Scalar (f64) output field',
              range,
            }))
            return { suggestions }
          }
          return { suggestions: [] }
        }

        const prefix = word.word.toLowerCase()
        const prefixMatches = CATALOG.filter((c) => c.name.startsWith(prefix))
        let candidates = prefixMatches
        if (candidates.length < MAX_SUGGESTIONS && prefix.length >= 2) {
          const already = new Set(prefixMatches.map((c) => c.name))
          const fuzzy = CATALOG
            .filter((c) => !already.has(c.name))
            .map((c) => ({ c, dist: editDistance(prefix, c.name) }))
            .filter(({ dist }) => dist <= 2)
            .sort((a, b) => a.dist - b.dist)
            .map(({ c }) => c)
          candidates = [...prefixMatches, ...fuzzy]
        }
        const items = candidates
          .slice(0, MAX_SUGGESTIONS)
          .map((c) => {
            const args = c.params.map((p, i) => `\${${i + 1}:${p.default}}`).join(', ')
            return {
              label: c.name,
              kind: monaco.languages.CompletionItemKind.Function,
              insertText: `${c.name}(${args})`,
              insertTextRules: SNIPPET,
              detail: c.label,
              documentation: { value: `**${c.label}** — ${c.description}\n\nOutputs: ${c.outputs.map((o) => `\`${o.name}\``).join(', ')}` },
              range,
            }
          })
        return { suggestions: items, incomplete: true }
      }

      // General: keywords/builtins/output-vars/bar-fields. Never touches CATALOG.
      const prefix = word.word.toLowerCase()
      if (prefix.length < MIN_PREFIX) return { suggestions: [] }

      type C = Monaco.languages.CompletionItem
      const pool: C[] = []
      for (const [name, sig, doc] of FUNCTIONS) {
        pool.push({ label: name, kind: monaco.languages.CompletionItemKind.Function, insertText: `${name}($0)`, insertTextRules: SNIPPET, detail: sig, documentation: { value: doc }, range })
      }
      pool.push({ label: 'ind', kind: monaco.languages.CompletionItemKind.Module, insertText: 'ind.', detail: 'Indicator namespace', range })
      pool.push({ label: 'ta', kind: monaco.languages.CompletionItemKind.Module, insertText: 'ta.', detail: 'ta.* incremental-state namespace', range })
      for (const [name, snippet, doc] of OUTPUT_VARS) {
        pool.push({ label: name, kind: monaco.languages.CompletionItemKind.Variable, insertText: snippet, detail: 'output', documentation: { value: doc }, range })
      }
      for (const f of BAR_FIELDS) pool.push({ label: f, kind: monaco.languages.CompletionItemKind.Variable, insertText: f, detail: 'bar series', range })
      for (const k of KEYWORDS) pool.push({ label: k, kind: monaco.languages.CompletionItemKind.Keyword, insertText: k, range })

      const filtered = pool
        .filter((s) => (s.label as string).toLowerCase().startsWith(prefix))
        .slice(0, MAX_SUGGESTIONS)
      return { suggestions: filtered, incomplete: true }
    },
  })

  monaco.languages.registerHoverProvider(SCRIPT_LANGUAGE_ID, {
    provideHover: async (model, position) => {
      const w = model.getWordAtPosition(position)
      if (!w) return null
      const name = w.word
      const range = new monaco.Range(position.lineNumber, w.startColumn, position.lineNumber, w.endColumn)

      const catalogLookup = () => {
        const ind = CATALOG.find((c) => c.name === name)
        if (ind) {
          const params = ind.params.map((p) => `${p.name}=${p.default}`).join(', ')
          const outs = ind.outputs.map((o) => `\`${o.name}\`: ${o.type}`).join(', ')
          const rhaiTy = ind.outputs.length > 1 ? 'Array<Map>' : 'Array<f64>'
          return { range, contents: [
            { value: `**ind.${ind.name}(${params})** — ${ind.label}` },
            { value: ind.description },
            { value: `Type: \`${rhaiTy}\` — Outputs: ${outs}` },
          ] }
        }
        const type = varTypeMap(model.getValue())[name]
        const bound = CATALOG.find((c) => c.name === type)
        if (bound) {
          const rhaiTy = bound.outputs.length > 1 ? 'Array<Map>' : 'Array<f64>'
          return { range, contents: [
            { value: `**${name}** = ind.${bound.name}` },
            { value: `Type: \`${rhaiTy}\` — indexed as \`${name}[0]\`, \`${name}[1]\`, ... (0 = current bar)` },
            { value: `Fields: ${bound.outputs.map((o) => `\`${o.name}\``).join(', ')}` },
          ] }
        }
        return null
      }

      if (CATALOG.length > 0) {
        const hit = catalogLookup()
        if (hit) return hit
      }

      const fn = FUNCTIONS.find((f) => f[0] === name)
      if (fn) return { range, contents: [{ value: `**${fn[1]}**` }, { value: fn[2] }] }
      const ta = TA_FUNCTIONS.find((f) => f[0] === name)
      if (ta) return { range, contents: [{ value: `**ta.${ta[0]}(${ta[1].join(', ')})**` }, { value: ta[2] }] }
      const ov = OUTPUT_VARS.find((v) => v[0] === name)
      if (ov) return { range, contents: [{ value: `**${ov[0]}** (output)` }, { value: ov[2] }] }
      if (BAR_FIELDS.includes(name)) {
        return { range, contents: [
          { value: `**${name}**` },
          { value: `Type: \`Array<f64>\` — raw bar series, indexed as \`${name}[0]\`, \`${name}[1]\`, ... (0 = current bar)` },
        ] }
      }
      if (name === 'state') {
        return { range, contents: [
          { value: '**state**' },
          { value: 'Type: `Map` — persistent per-hand storage, carried between bars (e.g. `state["in_position"] = true;`). Keys are dynamic — not statically checked.' },
        ] }
      }
      if (name === 'ta') {
        return { range, contents: [
          { value: '**ta**' },
          { value: 'Type: `Map` — internal state for `ta.*` incremental functions (`ta.ema(...)`, `ta.decay(...)`, ...). Not read/written directly.' },
        ] }
      }

      if (CATALOG.length === 0) {
        await ensureCatalog()
        const hit = catalogLookup()
        if (hit) return hit
      }
      return null
    },
  })
}
