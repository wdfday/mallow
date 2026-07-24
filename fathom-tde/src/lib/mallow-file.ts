// `.mallow` file format — Rhai script + metadata frontmatter. New territory: mallow-client's
// saved-strategy model (StrategySpec) is literally just `{ script: string }`, no symbol/
// timeframe/tags anywhere — this format was designed from scratch for fathom, using
// mallow-client's separate WatchEntry type (symbols[] + timeframe) as the closest reference for
// which fields make sense.
//
// Two kinds of keys live here, on purpose (see the Config tab design):
// - Chart directives: `symbol`, `timeframe` — what the strategy runs on.
// - Strategy-intrinsic run config: `size_mode`/`size_value`/`atr_multiplier`/`reverse_policy`/
//   `strength_sizing` — sizing taxonomy synced with helm's 5 SizeMode values (see
//   alm-engine's build_sizer). These version WITH the script because they're part of what the
//   strategy *is* — the same values become hand config when promoted to helm.
// Run-transient settings (date range, capital, fees) deliberately do NOT live here — they're
// per-run knobs (Config tab state), snapshotted into output/backtests JSON instead.
//
// ---
// symbol: BTCUSDT
// timeframe: H1
// size_mode: percent_equity
// size_value: 10
// ---
// ind.rsi(14)
// long_entry = cross_above(rsi, 30)

export const SIZE_MODES = ['percent_equity', 'quote_qty', 'fixed_qty', 'fixed_fractional', 'volatility'] as const
export type SizeMode = (typeof SIZE_MODES)[number]

/** UI label for what `size_value` means under each mode (see engine build_sizer fan-out). */
export const SIZE_VALUE_LABEL: Record<SizeMode, string> = {
  percent_equity: '% equity',
  quote_qty: 'USD per order',
  fixed_qty: 'Qty per order',
  fixed_fractional: 'Risk % per trade',
  volatility: 'Risk % per trade',
}

export interface MallowMetadata {
  symbol?: string
  timeframe?: string
  /** One of helm's 5 SizeMode values (engine build_sizer dispatch). */
  sizeMode?: SizeMode
  /** Meaning depends on sizeMode: percent_equity/fixed_fractional/volatility → percent (UI),
   *  quote_qty → USD, fixed_qty → quantity. Converted to the engine's fraction fields at
   *  request-build time (lib/backtest.ts). */
  sizeValue?: number
  /** ATR stop-distance multiplier — volatility mode only (engine default 2.0). */
  atrMultiplier?: number
  /** "flip" | "exit" (engine default: exit). */
  reversePolicy?: string
  /** Scale size by signal.strength — percent_equity only (engine forces it off elsewhere). */
  strengthSizing?: boolean
}

export interface MallowFile {
  metadata: MallowMetadata
  script: string
  /** Frontmatter lines we don't recognize — preserved verbatim on serialize so a rewrite
   *  (e.g. the +Symbol chip or Config tab) never destroys hand-written keys. */
  extra?: string[]
}

const FRONTMATTER_RE = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/

export function parseMallowFile(text: string): MallowFile {
  const match = text.match(FRONTMATTER_RE)
  if (!match) return { metadata: {}, script: text }

  const [, frontmatter, script] = match
  const metadata: MallowMetadata = {}
  const extra: string[] = []
  for (const line of frontmatter.split(/\r?\n/)) {
    const idx = line.indexOf(':')
    if (idx === -1) {
      if (line.trim()) extra.push(line)
      continue
    }
    const key = line.slice(0, idx).trim()
    const value = line.slice(idx + 1).trim()
    if (!value) continue
    if (key === 'symbol') metadata.symbol = value
    else if (key === 'timeframe') metadata.timeframe = value
    else if (key === 'size_mode' && (SIZE_MODES as readonly string[]).includes(value)) metadata.sizeMode = value as SizeMode
    else if (key === 'size_value' && !Number.isNaN(Number(value))) metadata.sizeValue = Number(value)
    else if (key === 'atr_multiplier' && !Number.isNaN(Number(value))) metadata.atrMultiplier = Number(value)
    else if (key === 'reverse_policy') metadata.reversePolicy = value
    else if (key === 'strength_sizing') metadata.strengthSizing = value === 'true'
    else extra.push(line)
  }
  return { metadata, script, extra: extra.length > 0 ? extra : undefined }
}

export function serializeMallowFile({ metadata, script, extra }: MallowFile): string {
  const lines: string[] = []
  if (metadata.symbol) lines.push(`symbol: ${metadata.symbol}`)
  if (metadata.timeframe) lines.push(`timeframe: ${metadata.timeframe}`)
  if (metadata.sizeMode) lines.push(`size_mode: ${metadata.sizeMode}`)
  if (metadata.sizeValue !== undefined) lines.push(`size_value: ${metadata.sizeValue}`)
  if (metadata.atrMultiplier !== undefined) lines.push(`atr_multiplier: ${metadata.atrMultiplier}`)
  if (metadata.reversePolicy) lines.push(`reverse_policy: ${metadata.reversePolicy}`)
  if (metadata.strengthSizing !== undefined) lines.push(`strength_sizing: ${metadata.strengthSizing}`)
  if (extra) lines.push(...extra)
  if (lines.length === 0) return script
  return `---\n${lines.join('\n')}\n---\n${script}`
}
