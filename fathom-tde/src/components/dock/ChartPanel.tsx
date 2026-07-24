import { useEffect, useRef, useState } from 'react'
import {
  createChart,
  createSeriesMarkers,
  CandlestickSeries,
  HistogramSeries,
  LineSeries,
  ColorType,
  CrosshairMode,
  type IChartApi,
  type ISeriesApi,
  type LogicalRange,
  type SeriesMarker,
  type Time,
} from 'lightweight-charts'
import type { IDockviewPanelProps } from 'dockview-react'
import { useTheme } from '@/lib/theme-context'
import { useChartSelection } from '@/lib/chart-context'
import { useChartData } from '@/hooks/use-chart-data'
import { loadWasm, type OhlcvBar } from '@/lib/almanac-wasm'
import { INDICATOR_COLORS } from '@/lib/indicator-colors'
import { Spinner } from '@/components/ui/spinner'

interface ChartPanelParams {
  symbol?: string
  timeframe?: string
  /** Present on script- and hand-hooked tabs (see chart-context.tsx's `OpenScriptChartParams` and
   *  DockArea's HandViewBridge) — absent on the one hookless default "chart" panel. */
  script?: string
}

// Simplified port of mallow-client/components/charts/lightweight-chart-container.tsx's core
// candle+volume rendering (chart init, ResizeObserver, data sync) — NOT the full component.
// Dropped: toolbar, drawing tools, crosshair legend. Sub-pane indicators (RSI/MACD-style
// oscillators) DO render now — a single shared pane 1 below the candles (see the indicator-series
// effect below) — this is a v1: one pane for every oscillator, not one pane per indicator type
// with its own independent price scale, which would be the fuller version of this.
//
// Live vs research mode: no explicit toggle — mirrors the one already-established rule that
// decides whether trade markers show (see `isResearch` below). Research (a completed backtest
// result is showing for this symbol) freezes the chart on that exact snapshot; otherwise the
// chart subscribes to the gateway's real-time WS tick stream (useChartData's `live` option — see
// hooks/use-chart-data.ts and src-tauri/src/stream/mod.rs for the actual connection, owned by Rust).
//
// Script indicator overlay: `selection.script` (whether from the one hookless default panel or a
// script-hooked tab's `params.script` — see chart-context.tsx's OpenScriptChartParams and
// DockArea's ScriptChartBridge) drives this. When present, this component feeds bars through
// alm-wasm's `ChartState` (the same engine the backtest button uses) purely for indicator-series
// extraction (`set_script` + `snapshot().indicators`, never `.backtest()`) and draws each
// MAIN-PANE indicator (`snapshot().overlay[label] === true`) as an extra line series.

const THEME = {
  midnight: {
    background: '#101820',
    text: '#94a3b8',
    grid: 'rgba(255, 255, 255, 0.05)',
    border: '#1F2A38',
    upColor: '#26a69a',
    downColor: '#ef5350',
    volumeUp: 'rgba(38, 166, 154, 0.3)',
    volumeDown: 'rgba(239, 83, 80, 0.3)',
  },
  foundation: {
    background: '#ffffff',
    text: '#64748b',
    grid: 'rgba(208, 208, 208, 0.3)',
    border: '#d0d0d0',
    upColor: '#16a34a',
    downColor: '#dc2626',
    volumeUp: 'rgba(22, 163, 74, 0.2)',
    volumeDown: 'rgba(220, 38, 38, 0.2)',
  },
}

interface IndicatorSnapshot {
  indicators: Record<string, Record<string, unknown[]>>
  overlay: Record<string, boolean>
  barTimes: number[] // unix seconds, parallel to each indicator field array
}

/** True when `next` is exactly `prev` plus the last bar replaced (forming-candle tick) or
 *  appended (a candle just closed) — i.e. safe to apply incrementally instead of a full
 *  teardown/recreate or full WASM replay. False for anything else (symbol/timeframe swap, a
 *  fresh historical load, pagination, …), which callers handle via their normal full path. */
function isTailUpdate(prev: OhlcvBar[], next: OhlcvBar[]): boolean {
  if (prev.length === 0 || next.length === 0) return false
  if (prev[0].time !== next[0].time) return false
  if (next.length !== prev.length && next.length !== prev.length + 1) return false
  for (let i = 0; i < prev.length - 1; i++) {
    if (prev[i].time !== next[i].time) return false
  }
  return true
}

/** Non-zero (the count prepended) when `next` is exactly `prev` with N older bars spliced onto
 *  the front — the shape `loadMore` produces. Checked at both ends of the overlap plus the
 *  midpoint rather than every bar (this only needs to distinguish "a load-back page" from an
 *  unrelated reload, not guarantee byte-exact equality). */
function prependedCount(prev: OhlcvBar[], next: OhlcvBar[]): number {
  if (prev.length === 0 || next.length <= prev.length) return 0
  const n = next.length - prev.length
  if (next[n].time !== prev[0].time) return 0
  if (next[next.length - 1].time !== prev[prev.length - 1].time) return 0
  const mid = Math.floor(prev.length / 2)
  if (next[n + mid].time !== prev[mid].time) return 0
  return n
}

// `params` is set for multichart tabs (one fixed symbol/timeframe per tab, from EditorPanel's
// "Chart View" button via DockArea's ScriptChartBridge) — falls back to the shared chart-context
// selection (click-a-metadata-chip wiring) when absent, i.e. the single default "Chart" panel.
export function ChartPanel({ params }: Partial<IDockviewPanelProps<ChartPanelParams>>) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candleSeriesRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const volumeSeriesRef = useRef<ISeriesApi<'Histogram'> | null>(null)
  const indicatorSeriesRef = useRef<Map<string, ISeriesApi<'Line'>>>(new Map())
  const prevBarsRef = useRef<OhlcvBar[]>([])
  // eslint-disable-next-line @typescript-eslint/no-explicit-any -- alm-wasm has no TS types
  const chartStateRef = useRef<any>(null)
  const chartStateSymbolRef = useRef<string | undefined>(undefined)
  const prevIndicatorScriptRef = useRef<string | undefined>(undefined)
  const prevIndicatorBarsRef = useRef<OhlcvBar[]>([])
  const [indicatorSnapshot, setIndicatorSnapshot] = useState<IndicatorSnapshot | null>(null)

  const { themeMode } = useTheme()
  const colors = THEME[themeMode === 'midnight' ? 'midnight' : 'foundation']
  const { selection: globalSelection, backtest } = useChartSelection()
  const selection = params?.symbol || params?.timeframe
    ? { symbol: params?.symbol, timeframe: params?.timeframe, script: params?.script }
    : globalSelection

  // Research mode = a completed backtest result is showing for THIS symbol (same condition that
  // already gated trade markers below) — freeze on that snapshot instead of polling live.
  const isResearch = backtest.status === 'done' && backtest.response !== null && backtest.response.symbol === selection.symbol

  const { bars, isLoading, isMock, source, hasMore, loadingMore, loadMore } = useChartData(
    selection.symbol,
    selection.timeframe,
    { live: !isResearch },
  )

  // Scroll-to-edge → load more history. Subscribed once per chart instance (see the
  // creation effect below), so the handler reads through refs rather than closing over
  // state that would go stale the moment the chart isn't recreated.
  const loadMoreRef = useRef(loadMore)
  const hasMoreRef = useRef(hasMore)
  const loadingMoreRef = useRef(loadingMore)
  useEffect(() => {
    loadMoreRef.current = loadMore
    hasMoreRef.current = hasMore
    loadingMoreRef.current = loadingMore
  }, [loadMore, hasMore, loadingMore])
  // Set right before a `loadMore()` prepend resolves, so the data-application effect below can
  // shift the visible logical range instead of running its normal fitContent()/reset path.
  const pendingPrependRangeRef = useRef<LogicalRange | null>(null)

  // Latest completed run's trades, only when it belongs to the symbol on display.
  const backtestTrades = isResearch && backtest.response ? backtest.response.trades : null

  // ── WASM ChartState: script → indicator series, decoupled from the chart-rendering effect
  // below so a still-loading WASM module or an in-flight snapshot never blocks candle rendering.
  useEffect(() => {
    let cancelled = false
    const symbol = selection.symbol
    const script = selection.script

    async function run() {
      if (!symbol || !script || bars.length === 0) {
        chartStateRef.current?.free?.()
        chartStateRef.current = null
        chartStateSymbolRef.current = undefined
        prevIndicatorScriptRef.current = undefined
        prevIndicatorBarsRef.current = []
        if (!cancelled) setIndicatorSnapshot(null)
        return
      }

      try {
        const wasm = await loadWasm()
        if (cancelled) return

        // A tail-only bar update (new/forming candle) can reuse the existing ChartState — but
        // ONLY if the script itself hasn't changed since it was set. Without this check, re-
        // clicking "Chart View"/a chip for the SAME symbol+timeframe (bars unchanged, so this
        // effect only re-ran because `script` did) would hit `isTailUpdate`'s "identical arrays
        // count as a tail" case and skip `set_script` entirely — silently keeping the OLD
        // script's indicators on screen (or none, the first time) instead of the edited one.
        const scriptChanged = prevIndicatorScriptRef.current !== script
        const tail = !scriptChanged && chartStateSymbolRef.current === symbol && isTailUpdate(prevIndicatorBarsRef.current, bars)
        if (!tail) {
          chartStateRef.current?.free?.()
          chartStateRef.current = new wasm.ChartState(symbol)
          chartStateSymbolRef.current = symbol
          chartStateRef.current.set_script(script)
          prevIndicatorScriptRef.current = script
          for (const b of bars) chartStateRef.current.add_tail(b.time, b.open, b.high, b.low, b.close, b.volume)
        } else {
          const last = bars[bars.length - 1]
          chartStateRef.current.add_tail(last.time, last.open, last.high, last.low, last.close, last.volume)
        }
        prevIndicatorBarsRef.current = bars

        const snap = chartStateRef.current.snapshot() as {
          indicators: Record<string, Record<string, unknown[]>>
          overlay: Record<string, boolean>
        }
        if (!cancelled) {
          setIndicatorSnapshot({ indicators: snap.indicators, overlay: snap.overlay, barTimes: bars.map((b) => b.time) })
        }
      } catch (err) {
        // `set_script` throws (as a rejected promise here, since we're inside an async fn) for an
        // invalid script — most commonly ChartState's HTF-not-supported guard if a script text
        // still contains a cross-TF reference. Surfacing this was the actual bug that made
        // "no indicators show up anywhere" so hard to diagnose: `void run()` below previously let
        // this become a silent unhandled promise rejection instead of a visible console error.
        console.error(`[chart-indicators] set_script failed for ${symbol}:`, err)
        chartStateRef.current?.free?.()
        chartStateRef.current = null
        chartStateSymbolRef.current = undefined
        prevIndicatorScriptRef.current = undefined
        prevIndicatorBarsRef.current = []
        if (!cancelled) setIndicatorSnapshot(null)
      }
    }

    void run()
    return () => { cancelled = true }
  }, [bars, selection.symbol, selection.script])

  // Free the WASM instance on unmount only (not on every effect re-run above, which reuses it).
  useEffect(() => () => chartStateRef.current?.free?.(), [])

  // ── Chart lifecycle: create once per (theme, symbol) identity — a genuine rebuild boundary —
  // then apply data incrementally in the effect below so a live-poll tick never resets
  // zoom/pan/crosshair the way a full recreate would.
  useEffect(() => {
    if (!containerRef.current) return

    const chart = createChart(containerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: colors.background },
        textColor: colors.text,
        fontFamily: "'Inter', sans-serif",
        fontSize: 11,
      },
      grid: {
        vertLines: { color: colors.grid },
        horzLines: { color: colors.grid },
      },
      crosshair: { mode: CrosshairMode.Normal },
      rightPriceScale: { borderColor: colors.border },
      timeScale: { borderColor: colors.border, timeVisible: true, secondsVisible: false },
      width: containerRef.current.clientWidth,
      height: containerRef.current.clientHeight,
    })
    chartRef.current = chart

    const candleSeries = chart.addSeries(CandlestickSeries, {
      upColor: colors.upColor,
      downColor: colors.downColor,
      borderUpColor: colors.upColor,
      borderDownColor: colors.downColor,
      wickUpColor: colors.upColor,
      wickDownColor: colors.downColor,
      priceFormat: {
        type: 'price',
        precision: selection.symbol?.includes('USDT') || selection.symbol?.includes('-') ? 2 : 4,
        minMove: selection.symbol?.includes('USDT') || selection.symbol?.includes('-') ? 0.01 : 0.0001,
      },
    })
    candleSeriesRef.current = candleSeries

    const volumeSeries = chart.addSeries(HistogramSeries, {
      priceFormat: { type: 'volume' },
      priceScaleId: 'volume',
    })
    chart.priceScale('volume').applyOptions({ scaleMargins: { top: 0.8, bottom: 0 } })
    volumeSeriesRef.current = volumeSeries

    const ro = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (entry && chartRef.current) {
        const { width, height } = entry.contentRect
        if (width > 0 && height > 0) chartRef.current.resize(width, height)
      }
    })
    ro.observe(containerRef.current)

    // Near the left edge of what's loaded → fetch older bars. `range.from` is a logical index
    // (can go negative past the first bar as the user keeps panning) — a small/negative threshold
    // means "close enough to the start to want more," not "exactly at bar 0."
    const handleVisibleRangeChange = (range: LogicalRange | null) => {
      if (!range || range.from > 10) return
      if (!hasMoreRef.current || loadingMoreRef.current) return
      pendingPrependRangeRef.current = range
      void loadMoreRef.current()
    }
    chart.timeScale().subscribeVisibleLogicalRangeChange(handleVisibleRangeChange)

    // Fresh chart → nothing rendered on it yet; the data-application effect below only draws a
    // full setData() when it sees a bars array it doesn't recognize as a tail update, and an
    // empty prevBarsRef guarantees exactly that on the very first run after this reset.
    prevBarsRef.current = []

    return () => {
      chart.timeScale().unsubscribeVisibleLogicalRangeChange(handleVisibleRangeChange)
      ro.disconnect()
      chart.remove()
      chartRef.current = null
      candleSeriesRef.current = null
      volumeSeriesRef.current = null
      indicatorSeriesRef.current.clear()
      prevBarsRef.current = []
    }
  }, [colors, selection.symbol])

  // ── Data application: incremental `.update()` for a pure tail change, full `.setData()`
  // otherwise (initial load, timeframe swap under the same symbol, pagination, …).
  useEffect(() => {
    const candleSeries = candleSeriesRef.current
    const volumeSeries = volumeSeriesRef.current
    const chart = chartRef.current
    if (!candleSeries || !volumeSeries || !chart || bars.length === 0) return

    const tail = isTailUpdate(prevBarsRef.current, bars)
    const prependN = tail ? 0 : prependedCount(prevBarsRef.current, bars)
    const barToCandle = (b: OhlcvBar) => ({ time: b.time as Time, open: b.open, high: b.high, low: b.low, close: b.close })
    const barToVolume = (b: OhlcvBar) => ({ time: b.time as Time, value: b.volume, color: b.close >= b.open ? colors.volumeUp : colors.volumeDown })

    if (tail) {
      const last = bars[bars.length - 1]
      candleSeries.update(barToCandle(last))
      volumeSeries.update(barToVolume(last))
    } else {
      candleSeries.setData(bars.map(barToCandle))
      volumeSeries.setData(bars.map(barToVolume))
      const pendingRange = prependN > 0 ? pendingPrependRangeRef.current : null
      if (pendingRange) {
        // Every existing bar's logical index shifted by +prependN once the older page was
        // spliced onto the front — shift the saved pre-fetch range the same amount instead of
        // fitContent()'s reset, so the user's scroll position holds steady across the load.
        chart.timeScale().setVisibleLogicalRange({ from: pendingRange.from + prependN, to: pendingRange.to + prependN })
      } else {
        chart.timeScale().fitContent()
      }
    }
    pendingPrependRangeRef.current = null
    prevBarsRef.current = bars

    // Trade markers from the latest backtest run — entry arrow per side, exit circle.
    // v5 API: createSeriesMarkers(series, markers), not series.setMarkers (same as
    // mallow-client/components/charts/lightweight-chart.tsx).
    if (backtestTrades && backtestTrades.length > 0) {
      const markers: SeriesMarker<Time>[] = backtestTrades
        .flatMap((t) => {
          const isLong = String(t.side).toLowerCase().includes('long') || String(t.side).toLowerCase() === 'buy'
          return [
            {
              time: Math.floor(t.entry_ts / 1000) as Time,
              position: isLong ? ('belowBar' as const) : ('aboveBar' as const),
              color: isLong ? colors.upColor : colors.downColor,
              shape: isLong ? ('arrowUp' as const) : ('arrowDown' as const),
              text: isLong ? 'L' : 'S',
              size: 1,
            },
            {
              time: Math.floor(t.exit_ts / 1000) as Time,
              position: 'aboveBar' as const,
              color: colors.text,
              shape: 'circle' as const,
              text: 'x',
              size: 0.7,
            },
          ]
        })
        .sort((a, b) => (a.time as number) - (b.time as number))
      createSeriesMarkers(candleSeries, markers)
    } else {
      createSeriesMarkers(candleSeries, [])
    }
  }, [bars, colors, backtestTrades])

  // ── Indicator line series: create/update/remove to match the latest snapshot. Main-pane
  // indicators (overlay === true, e.g. EMA/Bollinger) draw on pane 0 with the candles; sub-pane
  // ones (overlay === false, e.g. RSI/MACD) get their own pane 1 below — a single shared
  // oscillator pane for now (not one pane per indicator type), created on first use via
  // `addSeries(..., 1)` and torn down via `removePane(1)` once nothing needs it anymore, so a
  // script with no oscillators never leaves an empty pane wasting vertical space.
  useEffect(() => {
    const chart = chartRef.current
    if (!chart || !indicatorSnapshot) return

    const wanted = new Set<string>()
    let colorIdx = 0
    let hasSubPane = false
    for (const [label, fields] of Object.entries(indicatorSnapshot.indicators)) {
      const isOverlay = indicatorSnapshot.overlay[label]
      if (!isOverlay) hasSubPane = true
      for (const [field, values] of Object.entries(fields)) {
        const key = `${label}.${field}`
        wanted.add(key)
        const data = indicatorSnapshot.barTimes
          .map((t, i) => ({ time: t as Time, value: values[i] }))
          .filter((p): p is { time: Time; value: number } => typeof p.value === 'number')

        let series = indicatorSeriesRef.current.get(key)
        if (!series) {
          series = chart.addSeries(
            LineSeries,
            {
              color: INDICATOR_COLORS[colorIdx % INDICATOR_COLORS.length],
              lineWidth: 1,
              priceLineVisible: false,
              lastValueVisible: false,
              title: key,
            },
            isOverlay ? 0 : 1,
          )
          indicatorSeriesRef.current.set(key, series)
        }
        colorIdx++
        series.setData(data)
      }
    }
    for (const [key, series] of indicatorSeriesRef.current) {
      if (!wanted.has(key)) {
        chart.removeSeries(series)
        indicatorSeriesRef.current.delete(key)
      }
    }
    if (!hasSubPane && chart.panes().length > 1) {
      chart.removePane(1)
    }
  }, [indicatorSnapshot])

  return (
    <div className="relative h-full w-full">
      {(selection.symbol || selection.timeframe) && (
        <div className="pointer-events-none absolute left-2 top-2 z-10 flex items-center gap-1.5 rounded-md bg-card/80 px-2 py-1 text-[11px] font-medium text-foreground backdrop-blur">
          {selection.symbol ?? '—'} · {selection.timeframe ?? '—'}
          {!isResearch && !isMock && (
            <span title="Live — subscribed to the gateway's real-time tick stream" className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
          )}
          {isResearch && (
            <span className="rounded-sm bg-secondary/15 px-1 py-0.5 text-[9px] font-medium text-secondary">research</span>
          )}
          {isMock && (
            <span
              title="No data resolved for this symbol (project data/ → mounts → ~/Fathom/.data) — showing simulated candles. Add the symbol in the Data panel to download it."
              className="rounded-sm bg-amber-500/20 px-1 py-0.5 text-[9px] font-semibold uppercase tracking-wide text-amber-600 dark:text-amber-400"
            >
              mock
            </span>
          )}
          {!isMock && source && (
            <span
              title="Data source in use"
              className="rounded-sm bg-secondary/15 px-1 py-0.5 text-[9px] font-medium text-secondary"
            >
              {source}
            </span>
          )}
        </div>
      )}
      {isLoading && (
        <div className="absolute inset-0 z-10 flex items-center justify-center bg-background/60">
          <Spinner className="size-6 text-muted-foreground" />
        </div>
      )}
      <div ref={containerRef} className="h-full w-full" />
    </div>
  )
}
