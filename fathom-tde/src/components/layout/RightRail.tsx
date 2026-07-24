import { Database, MessageSquare, Plug, Search, Server, Settings } from 'lucide-react'
import { cn } from '@/lib/utils'
import { usePanels, type WorkspacePanelId } from '@/lib/panel-context'

// Just an icon strip — mirrors LeftRail exactly: toggles RightSidebar.tsx's box open/closed
// (`usePanels().togglePanel`), same relationship LeftRail's Explorer button has to Sidebar. Not
// wired into Dockview at all — Agent/Search/Settings/Helm/Broker are fixed tool views, not project
// content tabs. Top group is the "business/work" panels (Agent, Helm, Broker — call out to hosted
// APIs or run the local engine); bottom group is generic utility (Search, Settings) — same split
// mallow-client's rail made between tool tabs and utility panels.

function ToolBtn({
  icon: Icon,
  title,
  active = false,
  onClick,
}: {
  icon: typeof Search
  title: string
  active?: boolean
  onClick?: () => void
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      className={cn(
        'flex h-8 w-8 items-center justify-center rounded-md transition-colors',
        active
          ? 'bg-secondary/15 text-secondary'
          : 'text-sidebar-foreground/40 hover:bg-sidebar-accent/60 hover:text-sidebar-foreground/80',
      )}
    >
      <Icon className="h-4 w-4" />
    </button>
  )
}

const TOP_ITEMS: { id: WorkspacePanelId; icon: typeof Search; title: string }[] = [
  { id: 'agent', icon: MessageSquare, title: 'AI Agent' },
  { id: 'data', icon: Database, title: 'Data' },
  { id: 'helm', icon: Server, title: 'Helm' },
  { id: 'broker', icon: Plug, title: 'Broker Connections' },
]
const BOTTOM_ITEMS: { id: WorkspacePanelId; icon: typeof Search; title: string }[] = [
  { id: 'search', icon: Search, title: 'Search' },
  { id: 'settings', icon: Settings, title: 'Settings' },
]

export function RightRailEdge() {
  const { togglePanel, activePanelId } = usePanels()

  return (
    // w-10 matches TitleBar's h-10 — rail width = title-bar height, by design. bg-sidebar (not
    // bg-card) — same token as LeftRail/TitleBar/Sidebar so the two rails read as a matched pair.
    <div className="flex h-full w-10 shrink-0 flex-col items-center bg-sidebar">
      <div className="flex min-h-0 flex-1 flex-col items-center gap-0.5 py-2">
        {TOP_ITEMS.map((item) => (
          <ToolBtn
            key={item.id}
            icon={item.icon}
            title={item.title}
            active={activePanelId === item.id}
            onClick={() => togglePanel(item.id)}
          />
        ))}
        <div className="flex-1" />
        {BOTTOM_ITEMS.map((item) => (
          <ToolBtn
            key={item.id}
            icon={item.icon}
            title={item.title}
            active={activePanelId === item.id}
            onClick={() => togglePanel(item.id)}
          />
        ))}
      </div>
    </div>
  )
}
