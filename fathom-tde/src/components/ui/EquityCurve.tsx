import { useEffect, useRef } from 'react'
import {
  createChart,
  AreaSeries,
  ColorType,
  CrosshairMode,
  type IChartApi,
  type ISeriesApi,
  type UTCTimestamp,
} from 'lightweight-charts'
import { useTheme } from '@/lib/theme-context'
import type { CurvePoint } from '@/lib/backtest'

// Port of mallow-client/components/charts/equity-chart.tsx's core (area series, sign-colored,
// mount-once/update-on-data) — adapted to fathom's theme-context and to alm-engine's CurvePoint
// {t, v} (backtest curves) instead of the helm SSE EquityPoint {ts, value}. Dropped pnlMode —
// backtest equity is always an absolute curve, colored by end-vs-start.

const THEME = {
  midnight: {
    bg: '#101820',
    text: '#94a3b8',
    grid: 'rgba(31,42,56,0.6)',
    border: '#1F2A38',
    up: '#10b981',
    down: '#ef4444',
    upFill: 'rgba(16,185,129,0.12)',
    downFill: 'rgba(239,68,68,0.12)',
  },
  foundation: {
    bg: '#ffffff',
    text: '#64748b',
    grid: 'rgba(208,208,208,0.5)',
    border: '#d0d0d0',
    up: '#16a34a',
    down: '#dc2626',
    upFill: 'rgba(22,163,74,0.10)',
    downFill: 'rgba(220,38,38,0.10)',
  },
}

/** Fills its parent — give the wrapping element a real height (it lives inside dock panels). */
export function EquityCurve({ points }: { points: CurvePoint[] }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const seriesRef = useRef<ISeriesApi<'Area'> | null>(null)
  const { themeMode } = useTheme()
  const c = THEME[themeMode === 'midnight' ? 'midnight' : 'foundation']

  useEffect(() => {
    if (!containerRef.current) return
    const chart = createChart(containerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: c.bg },
        textColor: c.text,
        fontFamily: "'Inter', sans-serif",
        fontSize: 10,
      },
      grid: { vertLines: { color: c.grid }, horzLines: { color: c.grid } },
      crosshair: { mode: CrosshairMode.Normal },
      rightPriceScale: { borderColor: c.border, scaleMargins: { top: 0.1, bottom: 0.1 } },
      timeScale: {
        borderColor: c.border,
        timeVisible: true,
        secondsVisible: false,
        fixLeftEdge: true,
        fixRightEdge: true,
      },
      width: containerRef.current.clientWidth,
      height: containerRef.current.clientHeight,
    })
    chartRef.current = chart
    seriesRef.current = chart.addSeries(AreaSeries, {
      lineWidth: 2,
      bottomColor: 'transparent',
      priceFormat: { type: 'price', precision: 2, minMove: 0.01 },
      priceLineVisible: false,
    })

    const ro = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (entry && chartRef.current) {
        const { width, height } = entry.contentRect
        if (width > 0 && height > 0) chartRef.current.resize(width, height)
      }
    })
    ro.observe(containerRef.current)

    return () => {
      ro.disconnect()
      chart.remove()
      chartRef.current = null
      seriesRef.current = null
    }
  }, [themeMode, c])

  useEffect(() => {
    const series = seriesRef.current
    const chart = chartRef.current
    if (!series || !chart) return
    // CurvePoint.t is Unix ms (Bar timestamps); lightweight-charts wants seconds.
    const data = points.map((p) => ({
      time: (p.t > 100_000_000_000 ? Math.floor(p.t / 1000) : p.t) as UTCTimestamp,
      value: p.v,
    }))
    const positive = data.length > 1 ? data[data.length - 1].value >= data[0].value : true
    series.applyOptions({
      lineColor: positive ? c.up : c.down,
      topColor: positive ? c.upFill : c.downFill,
      bottomColor: 'transparent',
    })
    series.setData(data)
    if (data.length > 0) chart.timeScale().fitContent()
  }, [points, c])

  if (points.length === 0) return null
  return <div ref={containerRef} className="h-full w-full overflow-hidden" />
}
