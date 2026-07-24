import { useEffect, useState } from 'react'
import { Folder, FolderOpen, FolderPlus, X } from 'lucide-react'
import { useProjects } from '@/lib/project-context'
import { defaultProjectParent, pickFolder } from '@/lib/projects'
import { cn } from '@/lib/utils'

// The project switch/create dialog ("mẫu") — one modal covering both flows TitleBar's dropdown
// only gestured at: a switchable list of registered projects, and a real New Project form
// (name → creates `{location}/{name}` + scaffold, location defaulting to ~/Fathom per
// docs/VISION.md). Opened from the TitleBar ProjectSwitcher.

export function ProjectDialog({ onClose }: { onClose: () => void }) {
  const { projects, activeProject, setActive, createNamedProject, openExistingProject } = useProjects()
  const [name, setName] = useState('')
  const [parent, setParent] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    defaultProjectParent().then(setParent)
  }, [])

  async function handleCreate() {
    const trimmed = name.trim()
    if (!trimmed || !parent) return
    setCreating(true)
    setError(null)
    try {
      await createNamedProject(parent, trimmed)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setCreating(false)
    }
  }

  async function handleBrowse() {
    const picked = await pickFolder(parent)
    if (picked) setParent(picked)
  }

  async function handleOpenExisting() {
    await openExistingProject()
    onClose()
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/40" onClick={onClose} />
      <div className="relative z-10 flex w-[440px] max-h-[80vh] flex-col overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-xl">
        <div className="flex h-10 shrink-0 items-center justify-between border-b border-border px-3">
          <span className="text-sm font-semibold">Projects</span>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground">
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-3">
          {projects.length > 0 && (
            <div className="mb-3">
              <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                Switch project
              </p>
              {projects.map((p) => (
                <button
                  key={p.path}
                  onClick={() => { setActive(p.path); onClose() }}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-accent',
                    p.path === activeProject?.path && 'bg-secondary/10',
                  )}
                >
                  <Folder className={cn('h-4 w-4 shrink-0', p.path === activeProject?.path ? 'text-secondary' : 'opacity-50')} />
                  <span className="min-w-0 flex-1">
                    <span className={cn('block truncate text-[12px]', p.path === activeProject?.path && 'font-medium text-secondary')}>
                      {p.name}
                    </span>
                    <span className="block truncate text-[10px] text-muted-foreground">{p.path}</span>
                  </span>
                  <span className="shrink-0 text-[10px] text-muted-foreground">
                    {new Date(p.lastOpened).toLocaleDateString()}
                  </span>
                </button>
              ))}
            </div>
          )}

          <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            New project
          </p>
          <div className="flex flex-col gap-2 rounded-md border border-border p-2.5">
            <label className="flex flex-col gap-0.5">
              <span className="text-[10px] uppercase tracking-wider text-muted-foreground">Name</span>
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') void handleCreate() }}
                placeholder="my-alpha-research"
                className="h-8 w-full rounded-md border border-border bg-background px-2 text-[12px] outline-none placeholder:text-muted-foreground focus:border-secondary/50"
              />
            </label>
            <label className="flex flex-col gap-0.5">
              <span className="text-[10px] uppercase tracking-wider text-muted-foreground">Location</span>
              <div className="flex gap-1.5">
                <span
                  title={parent ?? undefined}
                  className="flex h-8 min-w-0 flex-1 items-center truncate rounded-md border border-border bg-muted/20 px-2 font-mono text-[11px] text-muted-foreground"
                >
                  {parent ?? '—'}
                </span>
                <button
                  onClick={() => void handleBrowse()}
                  className="h-8 shrink-0 rounded-md border border-border px-2 text-[11px] hover:border-secondary/50 hover:text-secondary"
                >
                  Browse…
                </button>
              </div>
            </label>
            {name.trim() && parent && (
              <p className="truncate font-mono text-[10px] text-muted-foreground" title={`${parent}/${name.trim()}`}>
                → {parent}/{name.trim()}
              </p>
            )}
            {error && <p className="text-[11px] text-destructive">{error}</p>}
            <button
              onClick={() => void handleCreate()}
              disabled={!name.trim() || !parent || creating}
              className="flex h-8 items-center justify-center gap-1.5 rounded-md bg-secondary/15 text-[12px] font-medium text-secondary hover:bg-secondary/25 disabled:cursor-not-allowed disabled:opacity-40"
            >
              <FolderPlus className="h-3.5 w-3.5" />
              {creating ? 'Creating…' : 'Create project'}
            </button>
          </div>

          <button
            onClick={() => void handleOpenExisting()}
            className="mt-2 flex h-8 w-full items-center justify-center gap-1.5 rounded-md border border-dashed border-border text-[12px] text-muted-foreground hover:border-secondary/50 hover:text-secondary"
          >
            <FolderOpen className="h-3.5 w-3.5" /> Open existing folder…
          </button>
        </div>
      </div>
    </div>
  )
}
