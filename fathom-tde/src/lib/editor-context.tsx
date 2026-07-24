import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import type { ScriptDiagnostic } from './almanac-wasm'

// Same indirection chart-context.tsx uses for openScriptChart: Sidebar (which needs to trigger
// "open this file") lives outside DockArea, but only DockArea owns the real dockview api that can
// add/focus a panel. A ref-backed handler lets Sidebar call `openFile(path)` without knowing
// dockview exists, and DockArea supplies the real implementation once mounted.

export type OpenFileHandler = (path: string, line?: number) => void

/** One open editor tab's current lint result — keyed by filePath (or a fixed sentinel for the
 *  untitled default tab, which has none) in `diagnosticsByFile` below. Aggregated across every
 *  currently-open tab, not just the active one — matches VSCode's Problems panel scope. */
export interface FileDiagnostics {
  fileName: string
  diagnostics: ScriptDiagnostic[]
}

/** Key `EditorPanel` registers its diagnostics under when it has no real file (the default
 *  untitled tab) — not a real path, so ProblemsPanel knows not to offer "jump to file" for it. */
export const UNTITLED_DIAGNOSTICS_KEY = '__untitled__'

interface EditorContextType {
  openFile: OpenFileHandler
  /** Called once by DockArea on mount to supply the real dockview-backed implementation. */
  registerOpenFileHandler: (fn: OpenFileHandler) => void
  /** Path backing the currently active/focused editor tab — null when no file-backed tab is
   *  focused (the default untitled editor, or a non-editor panel like Chart/Output/CLI). Sidebar
   *  uses this to highlight and auto-reveal the active file in the tree (VSCode's "Explorer:
   *  Auto Reveal"). Set by DockArea, which is the only thing that sees dockview's active-panel
   *  changes. */
  activeFilePath: string | null
  setActiveFilePath: (path: string | null) => void
  /** Every open editor tab's lint diagnostics, keyed by filePath (or UNTITLED_DIAGNOSTICS_KEY) —
   *  feeds ProblemsPanel. Each EditorPanel owns exactly one key and clears it on unmount (tab
   *  closed) so stale diagnostics for a file you're no longer editing don't linger. */
  diagnosticsByFile: Record<string, FileDiagnostics>
  setFileDiagnostics: (key: string, fileName: string, diagnostics: ScriptDiagnostic[]) => void
  clearFileDiagnostics: (key: string) => void
}

const EditorContext = createContext<EditorContextType | undefined>(undefined)

export function EditorProvider({ children }: { children: ReactNode }) {
  const handlerRef = useRef<OpenFileHandler | null>(null)
  const openFile: OpenFileHandler = (path, line) => handlerRef.current?.(path, line)
  const registerOpenFileHandler = (fn: OpenFileHandler) => { handlerRef.current = fn }
  const [activeFilePath, setActiveFilePath] = useState<string | null>(null)
  const [diagnosticsByFile, setDiagnosticsByFile] = useState<Record<string, FileDiagnostics>>({})

  const setFileDiagnostics = useCallback((key: string, fileName: string, diagnostics: ScriptDiagnostic[]) => {
    setDiagnosticsByFile((prev) => ({ ...prev, [key]: { fileName, diagnostics } }))
  }, [])
  const clearFileDiagnostics = useCallback((key: string) => {
    setDiagnosticsByFile((prev) => {
      if (!(key in prev)) return prev
      const next = { ...prev }
      delete next[key]
      return next
    })
  }, [])

  return (
    <EditorContext.Provider
      value={{
        openFile,
        registerOpenFileHandler,
        activeFilePath,
        setActiveFilePath,
        diagnosticsByFile,
        setFileDiagnostics,
        clearFileDiagnostics,
      }}
    >
      {children}
    </EditorContext.Provider>
  )
}

export function useEditor() {
  const ctx = useContext(EditorContext)
  if (!ctx) throw new Error('useEditor must be used within an EditorProvider')
  return ctx
}
