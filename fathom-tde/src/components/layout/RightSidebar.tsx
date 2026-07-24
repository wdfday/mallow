import { useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { X } from 'lucide-react'
import { usePanels, type WorkspacePanelId } from '@/lib/panel-context'
import { AgentPanel } from '@/components/panels/AgentPanel'
import { SearchPanel } from '@/components/panels/SearchPanel'
import { SettingsPanel } from '@/components/panels/SettingsPanel'
import { HelmPanel } from '@/components/panels/HelmPanel'
import { BrokerPanel } from '@/components/panels/BrokerPanel'
import { DataPanel } from '@/components/panels/DataPanel'

// The box itself — sibling of Sidebar/main/RightRailEdge in AppShell's row, same "flat sibling"
// shape Sidebar already uses. Resizable by plain mousedown/mousemove drag on its left edge, not
// react-resizable-panels (removed — this app doesn't need a whole panel-layout library for one
// draggable edge). One shared header (title + close) for whichever panel is active, since none of
// Agent/Search/Settings/Helm draw their own anymore — that was only needed while they were
// Dockview tabs (which draw their own tab chrome); as a plain box, this header is it.

const TITLES: Record<WorkspacePanelId, string> = {
  agent: 'AI Agent',
  search: 'Search',
  settings: 'Settings',
  helm: 'Helm',
  broker: 'Broker Connections',
  data: 'Data',
}

const MIN_WIDTH = 260
const MAX_WIDTH = 640
const DEFAULT_WIDTH = 340

export function RightSidebar() {
  const { activePanelId, closePanel } = usePanels()
  const [width, setWidth] = useState(DEFAULT_WIDTH)
  const draggingRef = useRef(false)

  if (!activePanelId) return null

  function handleResizeStart(e: ReactMouseEvent) {
    e.preventDefault()
    draggingRef.current = true
    const startX = e.clientX
    const startWidth = width

    function onMove(ev: MouseEvent) {
      if (!draggingRef.current) return
      // Panel is anchored to the right edge — dragging the handle left (negative dx) should grow
      // it, so the delta is startX minus the current x, not the other way round.
      const dx = startX - ev.clientX
      setWidth(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, startWidth + dx)))
    }
    function onUp() {
      draggingRef.current = false
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  return (
    <div
      className="relative my-2 flex shrink-0 overflow-hidden rounded-lg border border-border bg-background"
      style={{ width }}
    >
      <div
        onMouseDown={handleResizeStart}
        title="Drag to resize"
        className="absolute left-0 top-0 z-10 h-full w-1.5 -translate-x-1/2 cursor-col-resize hover:bg-secondary/30"
      />
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <div className="flex h-10 shrink-0 items-center justify-between border-b border-border px-3">
          <span className="text-sm font-semibold">{TITLES[activePanelId]}</span>
          <button onClick={closePanel} className="text-muted-foreground hover:text-foreground">
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-hidden">
          {activePanelId === 'agent' && <AgentPanel />}
          {activePanelId === 'search' && <SearchPanel />}
          {activePanelId === 'settings' && <SettingsPanel />}
          {activePanelId === 'helm' && <HelmPanel />}
          {activePanelId === 'broker' && <BrokerPanel />}
          {activePanelId === 'data' && <DataPanel />}
        </div>
      </div>
    </div>
  )
}
