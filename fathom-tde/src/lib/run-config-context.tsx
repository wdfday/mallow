import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'
import type { MallowMetadata } from './mallow-file'

// Backtest run-config, two layers (see the Config tab / per-editor rail design):
// - GLOBAL config (this context, localStorage): run knobs (range/capital/fees) + default
//   sizing/reverse — "cái chung". Shown on the left of BottomRail, edited in the Config tab.
// - Per-file OVERRIDES: each EditorPanel's own config rail writes into that file's frontmatter
//   (sizing/reverse keys of MallowMetadata) — file wins over global at run time, see
//   lib/backtest.ts::buildEngineConfig.
//
// Provider sits in App.tsx above AppShell: BottomRail (layout chrome) and ConfigPanel (a dock
// panel) both consume it from different subtrees.

export interface GlobalRunConfig {
  from?: string
  to?: string
  initialCapital?: number
  commissionPct?: number
  slippagePct?: number
  /** Default sizing/reverse — same keys/units as the frontmatter overrides. */
  sizeMode?: MallowMetadata['sizeMode']
  sizeValue?: number
  atrMultiplier?: number
  reversePolicy?: string
  strengthSizing?: boolean
}

interface RunConfigContextType {
  globals: GlobalRunConfig
  setGlobals: (patch: Partial<GlobalRunConfig>) => void
  /** Focus a dockview panel by id (e.g. 'config', 'output') — DockArea registers the impl. */
  focusDockPanel: (id: string) => void
  registerFocusHandler: (fn: (id: string) => void) => void
}

const STORAGE_KEY = 'fathom:globalRunConfig'

function loadGlobals(): GlobalRunConfig {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}') as GlobalRunConfig
  } catch {
    return {}
  }
}

const RunConfigContext = createContext<RunConfigContextType | undefined>(undefined)

export function RunConfigProvider({ children }: { children: ReactNode }) {
  const [globals, setGlobalsState] = useState<GlobalRunConfig>(loadGlobals)
  const focusRef = useRef<((id: string) => void) | null>(null)

  const setGlobals = useCallback((patch: Partial<GlobalRunConfig>) => {
    setGlobalsState((prev) => {
      const next = { ...prev, ...patch }
      localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
      return next
    })
  }, [])

  const focusDockPanel = useCallback((id: string) => focusRef.current?.(id), [])
  const registerFocusHandler = useCallback((fn: (id: string) => void) => { focusRef.current = fn }, [])

  const value = useMemo(
    () => ({ globals, setGlobals, focusDockPanel, registerFocusHandler }),
    [globals, setGlobals, focusDockPanel, registerFocusHandler],
  )

  return <RunConfigContext.Provider value={value}>{children}</RunConfigContext.Provider>
}

export function useRunConfig() {
  const ctx = useContext(RunConfigContext)
  if (!ctx) throw new Error('useRunConfig must be used within a RunConfigProvider')
  return ctx
}
