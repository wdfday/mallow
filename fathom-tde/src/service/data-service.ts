import { invoke } from '@tauri-apps/api/core'
import { generateMockOHLCV, startPriceFor } from '@/lib/mock-data'
import { getActiveProjectPath } from '@/lib/projects'
import type { OhlcvBar } from '@/lib/almanac-wasm'

// The one seam components go through for OHLCV (mirrors mallow-client's
// service/herald/data.service.ts role). Resolution ladder:
//   1. Local (Rust `load_bars`): project `data/` → mounted directories → ~/Fathom/.data lake,
//      with TF resampling when only a finer TF exists.
//   2. Hosted herald via the gateway (`POST /api/v1/data/:source/:symbol`) — when the user
//      hasn't added/imported the symbol locally yet, the chart still shows real data instead
//      of nothing; the Data panel is how they make it local (and fast/offline) later.
//   3. Mock generation — offline / not signed in / herald doesn't know the symbol either —
//      flagged via `isMock` so the UI never shows fake candles as if real.

export interface GetOhlcvOptions {
  bars?: number
}

// ── Herald fallback plumbing ────────────────────────────────────────────────
// data-service is a plain module; the authenticated fetch lives in AuthContext (Bearer +
// envelope unwrap + refresh-on-401, itself just `invoke('gateway_fetch', …)` now — Rust owns the
// actual network call). AuthProvider registers it here — same ref-registration pattern the dock
// bridges use — so this module stays hook-free.

type ApiFetch = <T>(path: string, init?: RequestInit) => Promise<T>

let heraldFetch: ApiFetch | null = null

export function registerHeraldFetch(fn: ApiFetch | null): void {
  heraldFetch = fn
}

interface HeraldBarRecord {
  t: number // Unix ms
  o: number
  h: number
  l: number
  c: number
  v: number
}

interface HeraldUnifiedDataResponse {
  source: string
  symbol: string
  tf: string
  candles?: { count: number; bars: HeraldBarRecord[]; next_before?: number | null; truncated_below?: boolean } | null
}

interface HeraldPage {
  bars: OhlcvBar[]
  /** True when herald returned fewer bars than asked for — nothing further back exists. */
  exhausted: boolean
}

/** Same source heuristic herald's own normalization uses: dash-form symbols are OKX. Exported for
 *  `use-chart-data.ts`'s WS subscribe call, which needs the same `source` this module derives
 *  internally — herald tokens (`{source}:{symbol}`) must agree everywhere or the WS subscribe key
 *  never matches the frame key the gateway actually sends back. */
export function heraldSource(symbol: string): string {
  return symbol.includes('-') ? 'okx' : 'binance'
}

async function fetchHeraldPage(
  symbol: string,
  timeframe: string | undefined,
  limit: number,
  beforeMs?: number,
): Promise<HeraldPage | null> {
  if (!heraldFetch) return null
  const source = heraldSource(symbol)
  const res = await heraldFetch<HeraldUnifiedDataResponse>(
    `/api/v1/data/${encodeURIComponent(source)}/${encodeURIComponent(symbol)}`,
    {
      method: 'POST',
      body: JSON.stringify({ tf: timeframe ?? null, candles: { limit, before: beforeMs ?? null } }),
    },
  )
  const raw = res.candles?.bars
  if (!raw || raw.length === 0) return null
  const bars = [...raw]
    .sort((a, b) => a.t - b.t)
    .map((b) => ({ time: Math.floor(b.t / 1000), open: b.o, high: b.h, low: b.l, close: b.c, volume: b.v }))
  // The very first call (`beforeMs` undefined) always stays inside herald's LIVE ledger window —
  // confirmed against source, it never falls back to the historical DuckDB/Parquet path — so
  // "fewer bars than asked for" here only means the ledger's warm-up window is short for this
  // timeframe (e.g. ~3.5 days of M1-equivalent history is very few H1/H4 bars), NOT that no
  // deeper history exists on disk. Only a call WITH an explicit `before` cursor actually consults
  // DuckDB and can report exhaustion reliably. Treating the initial load as "exhausted" here was
  // the bug: `hasMore` came back false before the user ever got to scroll, so "load back" never
  // fired a single paginated request.
  const exhausted = beforeMs !== undefined && raw.length < limit
  return { bars, exhausted }
}

export interface OhlcvResult {
  bars: OhlcvBar[]
  isMock: boolean
  /** Origin label from resolution, e.g. `"lake:binanceflat"` or `"project:BTCUSDT.csv"`. Also
   *  the tier `loadMoreBars` re-derives which backend to page against — never re-detected from
   *  scratch, so a chart session can't accidentally splice local history onto herald's live tail
   *  or vice versa. */
  source: string | null
  /** True when the bars were aggregated up from a finer stored TF. */
  resampled: boolean
  /** False once there's nothing further back to page for (see `loadMoreBars`). Always true for
   *  mock (nothing real to page into). */
  hasMore: boolean
}

interface LoadBarsResult {
  bars: OhlcvBar[]
  source: string
  fileTimeframe: string
  resampled: boolean
  exhausted: boolean
}

export async function getOhlcv(
  symbol: string | undefined,
  timeframe: string | undefined,
  options: GetOhlcvOptions = {},
): Promise<OhlcvResult> {
  if (symbol) {
    try {
      const res = await invoke<LoadBarsResult | null>('load_bars', {
        projectPath: getActiveProjectPath(),
        symbol,
        timeframe: timeframe ?? null,
        limit: options.bars ?? 5000,
        beforeMs: null,
      })
      if (res && res.bars.length > 0) {
        return { bars: res.bars, isMock: false, source: res.source, resampled: res.resampled, hasMore: !res.exhausted }
      }
    } catch (err) {
      // Falls through to herald/mock below — a malformed local file shouldn't leave the chart
      // blank, just visibly non-local so the user knows to fix or re-import it.
      console.error(`[data-service] load_bars failed for ${symbol}, trying herald:`, err)
    }
    try {
      const page = await fetchHeraldPage(symbol, timeframe, options.bars ?? 5000)
      if (page) {
        return {
          bars: page.bars,
          isMock: false,
          source: `herald:${heraldSource(symbol)}`,
          resampled: false,
          hasMore: !page.exhausted,
        }
      }
    } catch (err) {
      // Offline, not signed in, or herald doesn't track the symbol — mock is the last resort.
      console.error(`[data-service] herald fallback failed for ${symbol}, falling back to mock:`, err)
    }
  }
  return {
    bars: generateMockOHLCV(options.bars ?? 200, startPriceFor(symbol)),
    isMock: true,
    source: null,
    resampled: false,
    hasMore: false,
  }
}

/**
 * "Load back" pagination — fetches older bars strictly before `oldestLoadedTime` (Unix seconds,
 * the current earliest bar on the chart) from WHICHEVER tier originally resolved this chart's
 * data (`source`, from `getOhlcv`'s result) — never re-runs the local→herald ladder, since mixing
 * a local historical file with herald's live-ledger history for the same chart session would
 * splice two unrelated datasets together. `null` source (mock) never has anything to page into.
 */
export async function loadMoreBars(
  symbol: string,
  timeframe: string | undefined,
  source: string | null,
  oldestLoadedTime: number,
  limit: number,
): Promise<{ bars: OhlcvBar[]; hasMore: boolean } | null> {
  const beforeMs = oldestLoadedTime * 1000 - 1
  if (source?.startsWith('herald:')) {
    const page = await fetchHeraldPage(symbol, timeframe, limit, beforeMs)
    return page ? { bars: page.bars, hasMore: !page.exhausted } : { bars: [], hasMore: false }
  }
  if (source) {
    const res = await invoke<LoadBarsResult | null>('load_bars', {
      projectPath: getActiveProjectPath(),
      symbol,
      timeframe: timeframe ?? null,
      limit,
      beforeMs,
    })
    return res ? { bars: res.bars, hasMore: !res.exhausted } : { bars: [], hasMore: false }
  }
  return null
}
