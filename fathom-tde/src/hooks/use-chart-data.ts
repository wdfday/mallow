import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { listen } from '@tauri-apps/api/event'
import { getOhlcv, heraldSource, loadMoreBars, type GetOhlcvOptions } from '@/service/data-service'
import type { OhlcvBar } from '@/lib/almanac-wasm'

// Scoped-down mirror of mallow-client's hooks/use-chart-state.ts loading contract: fetch → track
// isLoading/error → expose refetch, keyed by (symbol, timeframe) same as useChartState's effect
// deps.
//
// Live tail (`live: true`, ChartPanel.tsx): a genuine WS tick subscription now, not a poll — the
// gateway's `/api/v1/stream` connection is owned entirely by the Rust side (src-tauri/src/stream/
// mod.rs: connect/reconnect loop, binary protobuf BarMsg frame decode, backoff table). This hook
// just does `stream_connect` (idempotent — a no-op if the loop is already running) + `
// stream_subscribe_bars`, then listens for `stream://bar` events and merges them into `bars` by
// timestamp (same-timestamp → tail replace, i.e. the forming candle ticking; newer → append, i.e.
// a candle just closed) — same merge logic the old REST poll used, just fed by real ticks instead
// of a 10s sample. Only runs once the initial load resolved to something real (`!isMock`) —
// subscribing for a symbol that came back mock would just interleave synthetic and real prices.

interface StreamBarEvent {
  key: string
  tf: string
  source: string
  symbol: string
  forming: boolean
  time: number
  open: number
  high: number
  low: number
  close: number
  volume: number
}

export interface UseChartDataResult {
  bars: OhlcvBar[]
  isLoading: boolean
  isMock: boolean
  /** Where the bars came from (e.g. "lake:binanceflat"), null when mock. */
  source: string | null
  error: string | null
  refetch: () => void
  /** False once a `loadMoreBars` call came back exhausted (or there's nothing to page — mock,
   *  not loaded yet). ChartPanel's scroll-to-edge handler stops calling `loadMore` once this
   *  flips, instead of hammering the same "no more data" request on every scroll tick. */
  hasMore: boolean
  loadingMore: boolean
  /** Fetches older bars before the currently-loaded earliest bar and prepends them. No-op (and a
   *  fast return) while a previous call is still in flight, or once `hasMore` is false. */
  loadMore: () => Promise<void>
}

/** Bars fetched per "load back" page — independent of the initial load size (`options.bars`,
 *  default 5000 in data-service.ts's `getOhlcv`), since scrolling further into history should
 *  feel incremental/light, not repeat the initial bulk load's size every time. */
const LOAD_MORE_PAGE_SIZE = 500

export interface UseChartDataOptions extends GetOhlcvOptions {
  /** Subscribe to the gateway's live WS tick stream for this symbol (see module doc). Pass
   *  `false` (the default) while reviewing a backtest/research snapshot — ChartPanel derives this
   *  from whether a completed backtest result is showing for the current symbol. */
  live?: boolean
}

export function useChartData(
  symbol: string | undefined,
  timeframe: string | undefined,
  options: UseChartDataOptions = {},
): UseChartDataResult {
  const [bars, setBars] = useState<OhlcvBar[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [isMock, setIsMock] = useState(false)
  const [source, setSource] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const barsCount = options.bars

  // `loadMore` needs the latest bars/source without re-subscribing every render (it's called from
  // a lightweight-charts event handler, not React state) — a ref mirror kept in sync alongside
  // the corresponding state setters, same trick as ChartPanel's own refs for chart/series instances.
  const barsRef = useRef<OhlcvBar[]>([])
  const sourceRef = useRef<string | null>(null)
  const loadingMoreRef = useRef(false)
  const hasMoreRef = useRef(false)

  useEffect(() => {
    if (!symbol) {
      setBars([])
      setIsLoading(false)
      setError(null)
      setHasMore(false)
      hasMoreRef.current = false
      return
    }

    let cancelled = false
    setIsLoading(true)
    setError(null)

    getOhlcv(symbol, timeframe, { bars: barsCount })
      .then((result) => {
        if (cancelled) return
        setBars(result.bars)
        barsRef.current = result.bars
        setIsMock(result.isMock)
        setSource(result.source)
        sourceRef.current = result.source
        setHasMore(result.hasMore)
        hasMoreRef.current = result.hasMore
      })
      .catch((err) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false)
      })

    return () => { cancelled = true }
  }, [symbol, timeframe, barsCount, reloadToken])

  const loadMore = useCallback(async () => {
    if (loadingMoreRef.current || !hasMoreRef.current || !symbol) return
    const oldest = barsRef.current[0]
    if (!oldest) return
    loadingMoreRef.current = true
    setLoadingMore(true)
    try {
      const page = await loadMoreBars(symbol, timeframe, sourceRef.current, oldest.time, LOAD_MORE_PAGE_SIZE)
      if (!page || page.bars.length === 0) {
        hasMoreRef.current = false
        setHasMore(false)
        return
      }
      const next = [...page.bars, ...barsRef.current]
      barsRef.current = next
      setBars(next)
      hasMoreRef.current = page.hasMore
      setHasMore(page.hasMore)
    } catch (err) {
      console.error(`[use-chart-data] loadMore failed for ${symbol}:`, err)
    } finally {
      loadingMoreRef.current = false
      setLoadingMore(false)
    }
  }, [symbol, timeframe, barsCount])

  const live = options.live ?? false
  useEffect(() => {
    if (!live || !symbol || isMock) return
    const source = heraldSource(symbol)
    const tf = (timeframe ?? 'M1').toUpperCase()
    const expectedKey = `${tf}:${source}:${symbol}`
    let cancelled = false
    let unlisten: (() => void) | undefined

    async function setup() {
      try {
        await invoke('stream_connect')
        await invoke('stream_subscribe_bars', { source, symbol, timeframe: tf })
      } catch (err) {
        console.error(`[use-chart-data] stream subscribe failed for ${symbol}:`, err)
        return
      }
      if (cancelled) return
      unlisten = await listen<StreamBarEvent>('stream://bar', (event) => {
        const b = event.payload
        if (b.key !== expectedKey) return // a frame for a different subscription sharing the socket
        const bar: OhlcvBar = { time: b.time, open: b.open, high: b.high, low: b.low, close: b.close, volume: b.volume }
        setBars((prev) => {
          const last = prev[prev.length - 1]
          let next: OhlcvBar[]
          if (!last || bar.time > last.time) next = [...prev, bar] // new candle closed
          else if (bar.time === last.time) next = [...prev.slice(0, -1), bar] // forming candle tick
          else return prev // stale/out-of-order relative to what's already shown — ignore
          barsRef.current = next
          return next
        })
      })
    }
    void setup()

    return () => {
      cancelled = true
      unlisten?.()
      invoke('stream_unsubscribe_bars', { source, symbol, timeframe: tf }).catch(() => {})
    }
  }, [live, symbol, timeframe, isMock])

  const refetch = useCallback(() => setReloadToken((n) => n + 1), [])

  return { bars, isLoading, isMock, source, error, refetch, hasMore, loadingMore, loadMore }
}
