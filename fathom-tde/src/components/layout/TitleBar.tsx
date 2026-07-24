import { useState } from 'react'
import { ChevronDown, FlaskConical, Folder, FolderOpen, LogOut, Plus, User } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useProjects } from '@/lib/project-context'
import { useAuth } from '@/lib/auth-context'
import { ProjectDialog } from '@/components/ui/ProjectDialog'

// Overrides the OS-drawn title bar (tauri.conf.json: titleBarStyle "overlay" + hiddenTitle) with
// our own — same approach VSCode/Cursor use on macOS: native traffic lights stay (expected,
// well-integrated window chrome — not something worth faking), but the title-text strip they used
// to sit in is now real HTML, ours to draw. `trafficLightPosition` in tauri.conf.json is pinned to
// {x:12, y:14} to match this bar's h-10 (40px) height — keep them in sync if this height changes.
//
// No border-b here on purpose — this bar and LeftRail directly below it share the same bg-sidebar
// color, so they read as one continuous panel (like a real app's title bar + toolbar) instead of
// two boxes with a seam where their borders cross. LeftRail/Sidebar/RightRail still draw their own
// vertical borders below; this bar just doesn't add a redundant horizontal one on top of them.
//
// `data-tauri-drag-region` makes the bar (and the span within it) drag the window — required since
// overlay mode gives us no native drag region of its own (see TitleBarStyle::Overlay's doc caveat).
// Dragging itself needs `core:window:allow-start-dragging` in capabilities/default.json — that
// permission isn't in core:window's default set, so it has to be listed explicitly or the drag
// silently no-ops.
function ProjectSwitcher() {
  const [open, setOpen] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)
  const { projects, activeProject, setActive, openExistingProject } = useProjects()

  return (
    <div className="relative flex items-center">
      <button
        onClick={() => setOpen((p) => !p)}
        className="flex items-center gap-1 rounded-md px-1.5 py-1 text-[12px] text-sidebar-foreground/60 transition-colors hover:bg-sidebar-accent/60"
      >
        <span className="max-w-40 truncate leading-none">{activeProject?.name ?? 'No project'}</span>
        <ChevronDown className="h-3 w-3 shrink-0 opacity-60" />
      </button>
      {open && (
        <>
          {/* Click-outside catcher — same simple pattern as Profile's popover below. */}
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute left-0 top-9 z-50 w-56 rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-md">
            {projects.length === 0 && (
              <p className="px-2 py-1.5 text-[11px] italic text-muted-foreground">No projects yet</p>
            )}
            {projects.map((p) => (
              <button
                key={p.path}
                onClick={() => { setActive(p.path); setOpen(false) }}
                className={cn(
                  'flex w-full items-center gap-1.5 truncate rounded-md px-2 py-1.5 text-left text-[12px] hover:bg-accent',
                  p.path === activeProject?.path && 'font-medium text-secondary',
                )}
              >
                <Folder className="h-3.5 w-3.5 shrink-0 opacity-60" />
                <span className="min-w-0 flex-1 truncate">{p.name}</span>
              </button>
            ))}
            <div className="my-1 border-t border-border" />
            <button
              onClick={() => { openExistingProject(); setOpen(false) }}
              className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[12px] hover:bg-accent"
            >
              <FolderOpen className="h-3.5 w-3.5 shrink-0 opacity-60" /> Open project…
            </button>
            <button
              onClick={() => { setDialogOpen(true); setOpen(false) }}
              className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[12px] hover:bg-accent"
            >
              <Plus className="h-3.5 w-3.5 shrink-0 opacity-60" /> New project…
            </button>
          </div>
        </>
      )}
      {dialogOpen && <ProjectDialog onClose={() => setDialogOpen(false)} />}
    </div>
  )
}

function ProfilePopover({ onClose }: { onClose: () => void }) {
  const { user, logout } = useAuth()

  return (
    <>
      <div className="fixed inset-0 z-40" onClick={onClose} />
      <div className="absolute right-0 top-11 z-50 w-52 rounded-md border border-border bg-popover p-3 text-popover-foreground shadow-md">
        <p className="truncate text-[12px] font-medium">{user?.full_name || user?.email}</p>
        <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{user?.email}</p>
        <div className="my-2 border-t border-border" />
        <button
          onClick={() => { onClose(); logout() }}
          className="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1.5 text-left text-[12px] text-destructive hover:bg-destructive/10"
        >
          <LogOut className="h-3.5 w-3.5" /> Sign out
        </button>
      </div>
    </>
  )
}

export function TitleBar() {
  const [open, setOpen] = useState(false)

  return (
    <div
      data-tauri-drag-region
      className="relative flex h-10 shrink-0 select-none items-center gap-2 bg-sidebar pl-[78px]"
    >
      <FlaskConical data-tauri-drag-region className="h-4 w-4 shrink-0 text-secondary" />
      <span data-tauri-drag-region className="leading-none text-[12px] font-medium text-sidebar-foreground/50">
        Fathom
      </span>
      <span data-tauri-drag-region className="leading-none text-sidebar-foreground/25">/</span>
      <ProjectSwitcher />

      <div className="flex-1" data-tauri-drag-region />

      {/* Profile — sits at the title/right-rail junction: a w-10 cap in TitleBar's own top-right
          corner, directly above RightRail's w-10 column, so it reads as that rail's top button
          rather than an unrelated title-bar widget. Real session now (Phase 1 — identity hosted
          auth via src-tauri/src/auth): App.tsx's Gate only mounts this component at all once
          `useAuth()` reports authenticated, so `user` is never null here in practice. Fuller
          account management (session list/revoke) lives in Settings, not this popover — this is
          just "who am I, sign out", the quick-glance version. */}
      <div className="relative flex h-10 w-10 shrink-0 items-center justify-center">
        <button
          onClick={() => setOpen((p) => !p)}
          title="Profile"
          className={cn(
            'flex h-8 w-8 items-center justify-center rounded-md transition-colors',
            open
              ? 'bg-secondary/15 text-secondary'
              : 'text-sidebar-foreground/40 hover:bg-sidebar-accent/60 hover:text-sidebar-foreground/80',
          )}
        >
          <User className="h-4 w-4" />
        </button>
        {open && <ProfilePopover onClose={() => setOpen(false)} />}
      </div>
    </div>
  )
}
