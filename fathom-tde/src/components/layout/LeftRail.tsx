import { useState } from 'react'
import { Blocks, FolderTree, GitBranch, Search } from 'lucide-react'
import { cn } from '@/lib/utils'

// IDE/Cursor-style activity bar — narrow icon-strip (56px) at the far-left edge. Just the icon
// strip itself: Sidebar is a sibling AppShell renders directly (same level as main/RightRail's
// panel, not nested inside this component) — clicking Explorer calls `onToggleSidebar`, which
// AppShell uses to mount/unmount Sidebar entirely (no halfway "collapsed" width, it's just gone
// from the DOM when hidden). Static otherwise: switching views (Search/Source Control/Extensions)
// isn't wired to anything yet, only Explorer has real content to show/hide.

type LeftTab = 'explorer' | 'search' | 'git' | 'extensions'

function RailBtn({
  icon: Icon,
  title,
  active,
  onClick,
}: {
  icon: typeof FolderTree
  title: string
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      className={cn(
        // Matches RightRail's ToolBtn exactly (h-8 w-8, rounded-md, h-4 icon) — both rails are
        // the same 56px width and should read as a mirrored pair, not two different sizings.
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

export function LeftRail({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const [tab, setTab] = useState<LeftTab>('explorer')

  // w-10 matches TitleBar's h-10 — rail width = title-bar height, by design. No border-r: it
  // shares bg-sidebar with TitleBar/Sidebar, and a border here just poked a meaningless straight
  // line perpendicular into TitleBar's row — the bg-color match alone is the separation.
  return (
    <div className="flex h-full w-10 shrink-0 flex-col items-center gap-0.5 bg-sidebar py-2">
      <RailBtn
        icon={FolderTree}
        title="Explorer"
        active={tab === 'explorer'}
        onClick={() => { setTab('explorer'); onToggleSidebar() }}
      />
      <RailBtn icon={Search} title="Search" active={tab === 'search'} onClick={() => setTab('search')} />
      <RailBtn icon={GitBranch} title="Source Control" active={tab === 'git'} onClick={() => setTab('git')} />
      <RailBtn icon={Blocks} title="Extensions" active={tab === 'extensions'} onClick={() => setTab('extensions')} />
    </div>
  )
}
