import { createContext, useContext, useRef, useState, type ReactNode } from 'react'
import type { BacktestResponse } from './backtest'

// Cross-panel state for Dockview's chart/editor/output panels. Verified via research: DockviewReact's
// panel content renders via ReactDOM.createPortal from inside DockArea.tsx's own render tree —
// React Context propagates through portals normally, so wrapping <DockviewReact> in this
// provider works with no dockview-specific API needed (simpler than the "native" alternative,
// containerApi.getPanel('chart').api.updateParameters(...)).

export interface BacktestState {
  status: 'idle' | 'running' | 'done' | 'error'
  response: BacktestResponse | null
  error: string | null
}

/** What the DEFAULT "chart" panel shows — the one hookless tab in the layout. Nothing writes to
 *  this anymore (see `OpenScriptChartParams`/`OpenHandParams` below for the hooked tabs script
 *  and hand clicks actually target) — it's reserved for a future manual symbol picker on the
 *  default chart itself. Kept as real state (not deleted) so that feature is cheap to add later;
 *  today it just renders whatever mock placeholder `ChartPanel` shows with no symbol picked. */
export interface ChartSelection {
  symbol?: string
  timeframe?: string
  script?: string
}

/** EditorPanel's "Chart View" button (and its Symbol/Timeframe chips, for a single-TF version of
 *  the same action) open one dedicated chart tab per timeframe, each HOOKED to this specific
 *  script — re-triggering (re-click, or the same script's chip after an edit) refreshes the
 *  existing tab's symbol/timeframe/script instead of spawning a duplicate. `sourceKey` is the
 *  editor tab's own dockview panel id (stable per open tab, distinct even for two untitled files)
 *  — NOT the file path, which two different open tabs could momentarily share or lack entirely.
 *  `scripts` is keyed by timeframe (its keys ARE the set of tabs to open) — a per-TF
 *  declarations-only mini-script from `script-indicators.ts`'s `buildPerTfScripts`, NOT the raw
 *  full script, since a single shared multi-TF script would trip every tab's ChartState
 *  HTF-not-supported guard identically (see that module's doc comment for why). */
export interface OpenScriptChartParams {
  sourceKey: string
  symbol: string
  scripts: Record<string, string>
}
export type OpenScriptChartHandler = (params: OpenScriptChartParams) => void

interface ChartSelectionContextType {
  selection: ChartSelection
  setSelection: (next: ChartSelection) => void
  backtest: BacktestState
  setBacktest: (next: BacktestState) => void
  /** Called by EditorPanel. No-op until DockArea registers the real dockview-backed handler. */
  openScriptChart: OpenScriptChartHandler
  /** Called once by DockArea on mount to supply the real implementation. */
  registerOpenScriptChartHandler: (fn: OpenScriptChartHandler) => void
}

const ChartSelectionContext = createContext<ChartSelectionContextType | undefined>(undefined)

export function ChartSelectionProvider({ children }: { children: ReactNode }) {
  const [selection, setSelection] = useState<ChartSelection>({})
  const [backtest, setBacktest] = useState<BacktestState>({ status: 'idle', response: null, error: null })
  // Ref, not state: DockArea registers this post-mount and it never needs to trigger a re-render
  // of consumers — only openScriptChart's *call* matters, not the handler identity.
  const handlerRef = useRef<OpenScriptChartHandler | null>(null)
  const openScriptChart: OpenScriptChartHandler = (params) => handlerRef.current?.(params)
  const registerOpenScriptChartHandler = (fn: OpenScriptChartHandler) => { handlerRef.current = fn }

  return (
    <ChartSelectionContext.Provider
      value={{ selection, setSelection, backtest, setBacktest, openScriptChart, registerOpenScriptChartHandler }}
    >
      {children}
    </ChartSelectionContext.Provider>
  )
}

export function useChartSelection() {
  const ctx = useContext(ChartSelectionContext)
  if (!ctx) throw new Error('useChartSelection must be used within a ChartSelectionProvider')
  return ctx
}
