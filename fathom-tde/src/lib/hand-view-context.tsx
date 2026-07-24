import { createContext, useContext, useRef, type ReactNode } from 'react'

// Ref-registration bridge, same shape as editor-context's openFile / chart-context's
// openScriptChart: HelmPanel (RightSidebar, a sibling of DockArea in AppShell) needs to open a
// dockview panel, but only DockArea owns the real dockview api. Lives at the App.tsx level (like
// run-config-context), NOT inside DockArea's own <ChartSelectionProvider> — HelmPanel is outside
// that subtree entirely, so a Provider scoped to DockArea wouldn't reach it.

export interface OpenHandParams {
  helmId: string
  handId: string
  handName: string
  helmName: string
  /** When present, also syncs the Chart panel to this hand's market (chart-context's
   *  setSelection) — clicking a hand shows its price action, not just its stats. */
  symbol?: string
  timeframe?: string
}

export type OpenHandHandler = (params: OpenHandParams) => void

interface HandViewContextType {
  openHand: OpenHandHandler
  /** Called once by DockArea's HandViewBridge on mount to supply the real implementation
   *  (opens/focuses a dockview 'hand' panel + syncs chart-context's selection). */
  registerOpenHandHandler: (fn: OpenHandHandler) => void
}

const HandViewContext = createContext<HandViewContextType | undefined>(undefined)

export function HandViewProvider({ children }: { children: ReactNode }) {
  const handlerRef = useRef<OpenHandHandler | null>(null)
  const openHand: OpenHandHandler = (params) => handlerRef.current?.(params)
  const registerOpenHandHandler = (fn: OpenHandHandler) => { handlerRef.current = fn }

  return (
    <HandViewContext.Provider value={{ openHand, registerOpenHandHandler }}>
      {children}
    </HandViewContext.Provider>
  )
}

export function useHandView() {
  const ctx = useContext(HandViewContext)
  if (!ctx) throw new Error('useHandView must be used within a HandViewProvider')
  return ctx
}
