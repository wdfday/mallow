import { useState, type ReactNode } from 'react'
import { TitleBar } from './TitleBar'
import { LeftRail } from './LeftRail'
import { Sidebar } from './Sidebar'
import { RightRailEdge } from './RightRail'
import { RightSidebar } from './RightSidebar'
import { BottomRail } from './BottomRail'

// TitleBar on top (custom, overrides the OS-drawn one — see TitleBar.tsx), then one flat row
// below it: LeftRail | Sidebar | main | RightSidebar | RightRailEdge — direct siblings at the
// same level. RightSidebar (Agent/Search/Settings/Helm) mirrors Sidebar exactly: a plain box
// toggled by its rail (see lib/panel-context.tsx), not a Dockview panel — Chart/Editor/Output/CLI
// (rendered as `children`, via DockArea) are the only things that live in the dock. Sidebar/main
// shape matches mallow-client/app/dashboard/layout.tsx (flex h-screen).
export function AppShell({ children }: { children: ReactNode }) {
  const [sidebarVisible, setSidebarVisible] = useState(true)

  return (
    // rounded-[10px]: macOS clips the whole window to its own native ~10px corner radius, but our
    // content is a plain sharp-cornered rectangle — without matching (and clipping via
    // overflow-hidden) it, TitleBar's square corner pokes past where the OS already curved the
    // window away, showing a lệch (misaligned) sliver of whatever's behind the window instead of
    // a clean curve. Hardcoded px, not a --radius theme token — this has to match the OS window
    // shape, not the app's themeable button/card radius (which some palettes set to just 2px).
    <div className="flex h-screen flex-col overflow-hidden rounded-[10px] bg-background">
      <TitleBar />
      {/* gap-2 between the bordered boxes (Sidebar / main / RightSidebar) and the rails flanking
          them — horizontal breathing room instead of every border touching flush. Only on this
          row, not on AppShell's own root: a gap between TitleBar and this row would push the
          rails down too, misaligning LeftRail's cap with the traffic lights again. */}
      <div className="flex min-h-0 flex-1 gap-2">
        <LeftRail onToggleSidebar={() => setSidebarVisible((p) => !p)} />
        {sidebarVisible && <Sidebar />}
        {/* my-2 (not h-full) — vertical breathing room from TitleBar/window-bottom for this box
            specifically, without affecting the rails, which stay flush top-to-bottom. Default
            flex stretch sizes it to the row's height minus this margin. */}
        <div className="my-2 flex min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border">
          <main className="flex-1 overflow-hidden">{children}</main>
        </div>
        <RightSidebar />
        <RightRailEdge />
      </div>
      <BottomRail />
    </div>
  )
}
