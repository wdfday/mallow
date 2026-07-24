import type { OhlcvBar } from './almanac-wasm'

// Shared mock OHLCV generator — used by both ChartPanel.tsx (candle rendering) and backtest.ts
// (feeding ChartState for a real WASM backtest run). One source of truth so the chart and the
// backtest always run against the same bars, not two independently-randomized series.

// Deterministic-ish per-symbol seed so switching the chip in EditorPanel visibly changes what's
// on screen (different price range per symbol), not just re-rolled noise at the same scale.
export function startPriceFor(symbol?: string): number {
  if (!symbol) return 50_000
  let hash = 0
  for (const ch of symbol) hash = (hash * 31 + ch.charCodeAt(0)) >>> 0
  return 1_000 + (hash % 60_000)
}

export function generateMockOHLCV(bars = 200, startPrice = 50_000): OhlcvBar[] {
  const out: OhlcvBar[] = []
  let price = startPrice
  const nowSec = Math.floor(Date.now() / 1000)
  const stepSec = 3600 // H1
  for (let i = bars; i > 0; i--) {
    const time = nowSec - i * stepSec
    const open = price
    const drift = (Math.random() - 0.5) * (startPrice * 0.01)
    const close = Math.max(open + drift, startPrice * 0.5)
    const high = Math.max(open, close) + Math.random() * (startPrice * 0.003)
    const low = Math.min(open, close) - Math.random() * (startPrice * 0.003)
    const volume = Math.random() * 100
    out.push({ time, open, high, low, close, volume })
    price = close
  }
  return out
}
