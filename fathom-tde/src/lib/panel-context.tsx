import { createContext, useContext, useState, type ReactNode } from 'react'

// Right-side "business" panels (Agent/Search/Settings/Helm) work exactly like LeftRail →
// Sidebar: a plain toggleable box owned by AppShell, not a Dockview panel — Chart/Editor/Output/
// CLI are the dock's job (project content, arbitrarily many tabs/groups); Agent/Search/Settings/
// Helm are fixed single-instance tool views, same category as the file-tree Sidebar already is.
// State lives here (not local to AppShell) only so RightRailEdge (icon strip) and RightSidebar
// (the box itself) — two separate components rendered as flat siblings in AppShell — can share
// "which one is open" without a prop-drilling detour through AppShell.

export type WorkspacePanelId = 'agent' | 'search' | 'settings' | 'helm' | 'broker' | 'data'

interface PanelContextType {
  activePanelId: WorkspacePanelId | null
  togglePanel: (id: WorkspacePanelId) => void
  closePanel: () => void
}

const PanelContext = createContext<PanelContextType | undefined>(undefined)

export function PanelProvider({ children }: { children: ReactNode }) {
  const [activePanelId, setActivePanelId] = useState<WorkspacePanelId | null>(null)
  const togglePanel = (id: WorkspacePanelId) => setActivePanelId((prev) => (prev === id ? null : id))
  const closePanel = () => setActivePanelId(null)

  return (
    <PanelContext.Provider value={{ activePanelId, togglePanel, closePanel }}>
      {children}
    </PanelContext.Provider>
  )
}

export function usePanels() {
  const ctx = useContext(PanelContext)
  if (!ctx) throw new Error('usePanels must be used within a PanelProvider')
  return ctx
}
