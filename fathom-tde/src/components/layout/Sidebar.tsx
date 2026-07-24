import { useEffect, useRef, useState, type ComponentType } from 'react'
import {
  ChevronRight,
  ChevronsDownUp,
  Download,
  File,
  FileCode,
  FilePlus,
  Folder,
  FolderOpen,
  FolderPlus,
  Pencil,
  RefreshCw,
  Trash2,
} from 'lucide-react'
import { confirm } from '@tauri-apps/plugin-dialog'
import { cn } from '@/lib/utils'
import {
  createFile,
  createFolder,
  deleteEntry,
  importDataFiles,
  listDir,
  renameEntry,
  type FileEntry,
  type ProjectEntry,
} from '@/lib/projects'
import { useProjects } from '@/lib/project-context'
import { useEditor } from '@/lib/editor-context'
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuTrigger } from '@/components/ui/context-menu'

// Only text formats the Editor (Monaco + .mallow parsing) can meaningfully show — matches the
// file types a fathom project actually produces (see lib/projects.ts's PROJECT_SUBDIRS: scripts
// live in strategies/, methodology notes in notes/). Everything else (data/*.parquet, images,
// binaries) isn't something Monaco should try to render as text.
const EDITABLE_EXTENSIONS = new Set(['mallow', 'rhai', 'md', 'json', 'txt'])

function extOf(name: string): string {
  return name.split('.').pop()?.toLowerCase() ?? ''
}

function isEditable(name: string): boolean {
  const ext = extOf(name)
  return !!ext && EDITABLE_EXTENSIONS.has(ext)
}

/** True when `filePath` lives inside `dirPath`, at any depth — drives auto-expanding ancestor
 *  folders when the active editor file changes ("reveal in Explorer", VSCode's default). Checks
 *  both separators since a project folder could in principle be opened from Windows. */
function isAncestorOf(dirPath: string, filePath: string): boolean {
  const dir = dirPath.replace(/[/\\]+$/, '')
  if (!filePath.startsWith(dir)) return false
  const rest = filePath.slice(dir.length)
  return rest.length > 0 && (rest[0] === '/' || rest[0] === '\\')
}

/** `untitled.mallow` / `new-folder`, incrementing (`untitled-1.mallow`, …) until it doesn't
 *  collide with what's already in `children` — same default-name behavior VSCode's Explorer
 *  toolbar uses, so hitting Enter immediately on the placeholder never fails with a silent
 *  "already exists". Falls back to the un-incremented name when `children` isn't loaded yet
 *  (rare: creating in a folder expanded for the first time in the same click) — the create can
 *  still collide in that edge case, but it now surfaces a real, retryable inline error instead of
 *  vanishing (see CreatingState below). */
function uniqueDefaultName(children: FileEntry[] | null, base: string, ext: string): string {
  const existing = new Set((children ?? []).map((c) => c.name))
  const withExt = (n: string) => (ext ? `${n}.${ext}` : n)
  if (!existing.has(withExt(base))) return withExt(base)
  for (let i = 1; i < 1000; i++) {
    const candidate = withExt(`${base}-${i}`)
    if (!existing.has(candidate)) return candidate
  }
  return withExt(base)
}

function defaultNameFor(kind: 'file' | 'folder', children: FileEntry[] | null): string {
  return kind === 'file' ? uniqueDefaultName(children, 'untitled', 'mallow') : uniqueDefaultName(children, 'new-folder', '')
}

/** Like `useEffect`, but skips the run on mount — for signals that start at a stable initial
 *  value (collapse-all / refresh tokens) and must only fire on a real, later change, not just
 *  because a node happened to mount while holding that value. */
function useDidUpdate(effect: () => void, deps: unknown[]) {
  const mounted = useRef(false)
  useEffect(() => {
    if (!mounted.current) { mounted.current = true; return }
    effect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
}

// Measurements/interaction pattern ported from mallow-client/components/layout/sidebar.tsx —
// 232px/56px widths, 200ms width transition, 40px rows, tree-indent guide lines. The "Projects"
// section replaces mallow-client's backend-driven nav-items/strategy-tree with a local,
// filesystem-based project list (see src/lib/projects.ts).
//
// IDE-completeness pass (this file): active-file highlight + auto-reveal (mirrors editor-context's
// `activeFilePath`, set by DockArea from dockview's active-panel changes), an Explorer toolbar
// (New File/Folder/Refresh/Collapse All — VSCode's panel header, not just right-click), and
// inline validation on create/rename (a failed attempt now stays open with a retryable error
// instead of silently vanishing).
//
// New/rename/delete known limitation (accepted, not guarded): renaming or deleting a file that's
// simultaneously open in an Editor tab races against that tab's own autosave/unmount-flush (see
// EditorPanel.tsx) — the tab doesn't know its underlying path just moved. Rare in practice (would
// need renaming via Sidebar while mid-edit on the exact same file); not worth the extra
// open-file-tracking plumbing a real guard would need for v1.

// ─── Inline name input — shared by "new file/folder" and "rename" ──────────────

function InlineNameInput({
  initialValue,
  selectExtent = 'all',
  error,
  onCommit,
  onCancel,
}: {
  initialValue: string
  /** 'stem' selects the name without its extension (VSCode's rename behavior); 'all' selects
   *  everything (new-file default, so typing immediately replaces the placeholder). */
  selectExtent?: 'all' | 'stem'
  /** Validation error from the previous attempt (name collision, etc.) — shown below the input.
   *  Callers remount this component (bump a `key`) whenever the error changes, since a single
   *  instance's internal "finished" guard would otherwise ignore a second Enter/blur. */
  error?: string
  onCommit: (name: string) => void
  onCancel: () => void
}) {
  const ref = useRef<HTMLInputElement>(null)
  const doneRef = useRef(false)

  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.focus()
    if (selectExtent === 'stem') {
      const dot = initialValue.lastIndexOf('.')
      el.setSelectionRange(0, dot > 0 ? dot : initialValue.length)
    } else {
      el.select()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- run once on mount only
  }, [])

  function finish(value: string) {
    if (doneRef.current) return
    doneRef.current = true
    const v = value.trim()
    if (v && v !== initialValue) onCommit(v)
    else if (v === initialValue) onCommit(v) // unchanged default (e.g. left "untitled.mallow" as-is) still creates/keeps it
    else onCancel()
  }

  return (
    <div>
      <input
        ref={ref}
        defaultValue={initialValue}
        onBlur={(e) => finish(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') e.currentTarget.blur()
          if (e.key === 'Escape') { doneRef.current = true; onCancel(); e.currentTarget.blur() }
        }}
        className={cn(
          'h-6 w-full rounded border bg-background px-1 text-[12px] outline-none',
          error ? 'border-destructive/60' : 'border-secondary/50',
        )}
      />
      {error && (
        <p className="mt-0.5 truncate text-[10px] text-destructive" title={error}>
          {error}
        </p>
      )}
    </div>
  )
}

interface CreatingState {
  kind: 'file' | 'folder'
  value: string
  error?: string
  /** Bumped on every failed attempt so InlineNameInput remounts (fresh focus/selection + its
   *  once-only commit guard reset) instead of silently ignoring the retry. */
  attempt: number
}

interface RenamingState {
  value: string
  error?: string
  attempt: number
}

// ─── Explorer toolbar (VSCode-style panel header: New File/Folder, Refresh, Collapse All) ─────

function ToolbarIconButton({
  icon: Icon,
  title,
  onClick,
}: {
  icon: ComponentType<{ className?: string }>
  title: string
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      className="flex h-6 w-6 shrink-0 items-center justify-center rounded text-sidebar-foreground/40 transition-colors hover:bg-sidebar-accent/60 hover:text-sidebar-foreground/80"
    >
      <Icon className="h-3.5 w-3.5" />
    </button>
  )
}

function ExplorerToolbar({
  onNewFile,
  onNewFolder,
  onRefresh,
  onCollapseAll,
}: {
  onNewFile: () => void
  onNewFolder: () => void
  onRefresh: () => void
  onCollapseAll: () => void
}) {
  return (
    <div className="flex h-8 shrink-0 items-center justify-between border-b border-sidebar-border/40 px-2">
      <span className="text-[10px] font-semibold uppercase tracking-wider text-sidebar-foreground/35">Explorer</span>
      <div className="flex items-center gap-0.5">
        <ToolbarIconButton icon={FilePlus} title="New File" onClick={onNewFile} />
        <ToolbarIconButton icon={FolderPlus} title="New Folder" onClick={onNewFolder} />
        <ToolbarIconButton icon={RefreshCw} title="Refresh Explorer" onClick={onRefresh} />
        <ToolbarIconButton icon={ChevronsDownUp} title="Collapse All" onClick={onCollapseAll} />
      </div>
    </div>
  )
}

// ─── File tree node (recursive — real directory contents, any depth) ──────────

// Bug fix (kept from earlier pass): previously this only ever looked for 4 hardcoded subdirs
// (data/output/strategies/notes) — correct for a project `createProject` scaffolded, but empty for
// any real folder opened via "Open project" (e.g. an existing repo with its own structure). This
// is a genuine recursive tree (like VSCode's Explorer) — expanding any directory lists its actual
// contents via lib/projects.ts's listDir.
function FileTreeNode({
  entry,
  onParentRefresh,
  revealPath,
  refreshToken,
  collapseSignal,
}: {
  entry: FileEntry
  onParentRefresh: () => void
  /** Path of the currently active editor file — auto-expands this node if it's an ancestor, and
   *  highlights it if this node IS that file. */
  revealPath: string | null
  /** Bumped by the Explorer toolbar's Refresh — re-lists this node's children if it's open,
   *  without collapsing it (unlike a full remount). */
  refreshToken: number
  /** Bumped by the Explorer toolbar's Collapse All — closes this node if it's a directory. */
  collapseSignal: number
}) {
  const [open, setOpen] = useState(false)
  const [children, setChildren] = useState<FileEntry[] | null>(null)
  const [creating, setCreating] = useState<CreatingState | null>(null)
  const [renaming, setRenaming] = useState<RenamingState | null>(null)
  const { openFile } = useEditor()
  const fileButtonRef = useRef<HTMLButtonElement>(null)
  const isActiveFile = !entry.isDirectory && entry.path === revealPath

  function refresh() {
    listDir(entry.path).then(setChildren).catch(() => setChildren([]))
  }

  useEffect(() => {
    if (open && entry.isDirectory && !children) refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, entry.isDirectory, entry.path, children])

  // Reveal in Explorer: auto-expand when the active editor file lives inside this directory.
  // Runs on mount too (not gated) — when a parent just opened because IT was on the path, this
  // child mounts fresh and must continue the chain immediately, not wait for a later "change".
  useEffect(() => {
    if (entry.isDirectory && revealPath && entry.path !== revealPath && isAncestorOf(entry.path, revealPath)) {
      setOpen(true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealPath, entry.isDirectory, entry.path])

  // Scroll the active file into view once its ancestors have opened above.
  useEffect(() => {
    if (isActiveFile) fileButtonRef.current?.scrollIntoView({ block: 'nearest' })
  }, [isActiveFile])

  useDidUpdate(() => {
    if (entry.isDirectory) setOpen(false)
  }, [collapseSignal])

  useDidUpdate(() => {
    if (entry.isDirectory && open) refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshToken])

  async function handleCreate(kind: 'file' | 'folder', name: string) {
    try {
      const path = kind === 'file' ? await createFile(entry.path, name) : await createFolder(entry.path, name)
      setCreating(null)
      setOpen(true)
      refresh()
      if (kind === 'file') openFile(path)
    } catch (err) {
      setCreating((prev) => ({
        kind,
        value: name,
        error: err instanceof Error ? err.message : String(err),
        attempt: (prev?.attempt ?? 0) + 1,
      }))
    }
  }

  async function handleRename(newName: string) {
    try {
      await renameEntry(entry.path, newName)
      setRenaming(null)
      onParentRefresh()
    } catch (err) {
      setRenaming((prev) => ({
        value: newName,
        error: err instanceof Error ? err.message : String(err),
        attempt: (prev?.attempt ?? 0) + 1,
      }))
    }
  }

  async function handleDelete() {
    const ok = await confirm(
      `Delete ${entry.isDirectory ? 'folder' : 'file'} "${entry.name}"? This cannot be undone.`,
      { title: 'Confirm delete', kind: 'warning' },
    )
    if (!ok) return
    await deleteEntry(entry.path, entry.isDirectory)
    onParentRefresh()
  }

  if (!entry.isDirectory) {
    const editable = isEditable(entry.name)
    const isMallow = extOf(entry.name) === 'mallow'
    if (renaming) {
      return (
        <div className="px-2 py-0.5">
          <InlineNameInput
            key={renaming.attempt}
            initialValue={renaming.value}
            selectExtent="stem"
            error={renaming.error}
            onCommit={handleRename}
            onCancel={() => setRenaming(null)}
          />
        </div>
      )
    }
    return (
      <ContextMenu>
        <ContextMenuTrigger asChild>
          <button
            ref={fileButtonRef}
            onClick={() => editable && openFile(entry.path)}
            disabled={!editable}
            title={editable ? entry.name : `${entry.name} (cannot be opened in the Editor)`}
            className={cn(
              'flex w-full items-center gap-1.5 truncate rounded-md px-2 py-1 text-left text-[12px] transition-colors',
              isActiveFile ? 'bg-secondary/10 text-secondary' : 'text-sidebar-foreground/45',
              editable && !isActiveFile && 'hover:bg-sidebar-accent/60 hover:text-sidebar-foreground/80',
              !editable && 'cursor-default opacity-50',
            )}
          >
            {isMallow ? (
              <FileCode className={cn('h-3 w-3 shrink-0', isActiveFile ? 'text-secondary' : 'text-secondary/70')} />
            ) : (
              <File className="h-3 w-3 shrink-0 opacity-50" />
            )}
            <span className="min-w-0 flex-1 truncate">{entry.name}</span>
          </button>
        </ContextMenuTrigger>
        <ContextMenuContent className="w-40">
          <ContextMenuItem onClick={() => setRenaming({ value: entry.name, attempt: 0 })}>
            <Pencil className="h-3.5 w-3.5" /> Rename
          </ContextMenuItem>
          <ContextMenuItem variant="destructive" onClick={() => void handleDelete()}>
            <Trash2 className="h-3.5 w-3.5" /> Delete
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>
    )
  }

  return (
    <div>
      {renaming ? (
        <div className="px-2 py-0.5">
          <InlineNameInput
            key={renaming.attempt}
            initialValue={renaming.value}
            selectExtent="stem"
            error={renaming.error}
            onCommit={handleRename}
            onCancel={() => setRenaming(null)}
          />
        </div>
      ) : (
        <ContextMenu>
          <ContextMenuTrigger asChild>
            <button
              onClick={() => setOpen((p) => !p)}
              className="flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-[12px] text-sidebar-foreground/55 transition-colors hover:bg-sidebar-accent/60"
            >
              <ChevronRight className={cn('h-3 w-3 shrink-0 transition-transform', open && 'rotate-90')} />
              {open ? <FolderOpen className="h-3 w-3 shrink-0 text-secondary/70" /> : <Folder className="h-3 w-3 shrink-0 opacity-60" />}
              <span className="min-w-0 flex-1 truncate text-left">{entry.name}</span>
            </button>
          </ContextMenuTrigger>
          <ContextMenuContent className="w-40">
            <ContextMenuItem
              onClick={() => { setOpen(true); setCreating({ kind: 'file', value: defaultNameFor('file', children), attempt: 0 }) }}
            >
              <FilePlus className="h-3.5 w-3.5" /> New File
            </ContextMenuItem>
            <ContextMenuItem
              onClick={() => { setOpen(true); setCreating({ kind: 'folder', value: defaultNameFor('folder', children), attempt: 0 }) }}
            >
              <FolderPlus className="h-3.5 w-3.5" /> New Folder
            </ContextMenuItem>
            <ContextMenuItem onClick={() => setRenaming({ value: entry.name, attempt: 0 })}>
              <Pencil className="h-3.5 w-3.5" /> Rename
            </ContextMenuItem>
            <ContextMenuItem variant="destructive" onClick={() => void handleDelete()}>
              <Trash2 className="h-3.5 w-3.5" /> Delete
            </ContextMenuItem>
          </ContextMenuContent>
        </ContextMenu>
      )}
      {open && (
        <div className="ml-3 border-l border-sidebar-border/40 pl-2">
          {creating && (
            <div className="px-2 py-0.5">
              <InlineNameInput
                key={creating.attempt}
                initialValue={creating.value}
                error={creating.error}
                onCommit={(name) => void handleCreate(creating.kind, name)}
                onCancel={() => setCreating(null)}
              />
            </div>
          )}
          {children === null ? (
            <p className="px-2 py-1 text-[11px] italic text-sidebar-foreground/30">Loading…</p>
          ) : children.length === 0 && !creating ? (
            <p className="px-2 py-1 text-[11px] italic text-sidebar-foreground/30">Empty</p>
          ) : (
            children.map((c) => (
              <FileTreeNode
                key={c.path}
                entry={c}
                onParentRefresh={refresh}
                revealPath={revealPath}
                refreshToken={refreshToken}
                collapseSignal={collapseSignal}
              />
            ))
          )}
        </div>
      )}
    </div>
  )
}

// ─── Mounted project (the ONE active project's file tree — not a multi-project list) ──────────

// "Mount" like a filesystem mount point: exactly one project attached here at a time, not a list
// of every registered project sitting expanded/collapsed side by side. Switching projects (via
// TitleBar's ProjectSwitcher) replaces what's mounted here entirely — it doesn't add a second row.
// `key={project.path}` on the call site remounts this fresh (fresh expanded/children state) on
// every switch, instead of a manual reset effect.
function MountedProject({
  project,
  rootAction,
  refreshToken,
  collapseSignal,
  revealPath,
}: {
  project: ProjectEntry
  /** New object each time the Explorer toolbar's New File/Folder is clicked (root-level target) —
   *  a fresh reference every click (even for the same kind twice in a row) is what makes the
   *  effect below fire on every click, not just the first. */
  rootAction: { kind: 'file' | 'folder' } | null
  refreshToken: number
  collapseSignal: number
  revealPath: string | null
}) {
  const { removeProject } = useProjects()
  const { openFile } = useEditor()
  const [expanded, setExpanded] = useState(true)
  const [children, setChildren] = useState<FileEntry[] | null>(null)
  const [creating, setCreating] = useState<CreatingState | null>(null)

  function refresh() {
    listDir(project.path).then(setChildren).catch(() => setChildren([]))
  }

  useEffect(() => {
    if (expanded && !children) refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expanded, children, project.path])

  useEffect(() => {
    if (revealPath && isAncestorOf(project.path, revealPath)) setExpanded(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealPath, project.path])

  useDidUpdate(() => { setExpanded(false) }, [collapseSignal])
  useDidUpdate(() => { refresh() }, [refreshToken])

  // Explorer toolbar's New File/Folder target the project root.
  useEffect(() => {
    if (!rootAction) return
    setExpanded(true)
    setCreating({ kind: rootAction.kind, value: defaultNameFor(rootAction.kind, children), attempt: 0 })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rootAction])

  async function handleCreate(kind: 'file' | 'folder', name: string) {
    try {
      const path = kind === 'file' ? await createFile(project.path, name) : await createFolder(project.path, name)
      setCreating(null)
      refresh()
      if (kind === 'file') openFile(path)
    } catch (err) {
      setCreating((prev) => ({
        kind,
        value: name,
        error: err instanceof Error ? err.message : String(err),
        attempt: (prev?.attempt ?? 0) + 1,
      }))
    }
  }

  return (
    <div>
      <ContextMenu>
        <ContextMenuTrigger asChild>
          <div className="group flex items-center gap-1 rounded-md pr-1 transition-colors hover:bg-sidebar-accent/60">
            <button
              onClick={() => setExpanded((p) => !p)}
              className="flex h-6 w-5 shrink-0 items-center justify-center text-sidebar-foreground/30 hover:text-sidebar-foreground/70"
            >
              <ChevronRight className={cn('h-3 w-3 transition-transform', expanded && 'rotate-90')} />
            </button>
            <button
              onClick={() => setExpanded((p) => !p)}
              className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
            >
              {expanded ? (
                <FolderOpen className="h-3.5 w-3.5 shrink-0 text-secondary/70" />
              ) : (
                <Folder className="h-3.5 w-3.5 shrink-0 text-sidebar-foreground/40" />
              )}
              <span className="min-w-0 flex-1 truncate text-[12px] font-medium text-sidebar-foreground">
                {project.name}
              </span>
            </button>
          </div>
        </ContextMenuTrigger>
        <ContextMenuContent className="w-44">
          <ContextMenuItem onClick={() => { setExpanded(true); setCreating({ kind: 'file', value: defaultNameFor('file', children), attempt: 0 }) }}>
            <FilePlus className="h-3.5 w-3.5" /> New File
          </ContextMenuItem>
          <ContextMenuItem onClick={() => { setExpanded(true); setCreating({ kind: 'folder', value: defaultNameFor('folder', children), attempt: 0 }) }}>
            <FolderPlus className="h-3.5 w-3.5" /> New Folder
          </ContextMenuItem>
          <ContextMenuItem
            onClick={async () => {
              // Copies picked file(s) into this project's data/ — refetching by clearing
              // `children` re-triggers MountedProject's list effect, same cache-invalidation
              // trick used everywhere else in this file (see FileTreeNode's `open` toggle).
              const imported = await importDataFiles(project.path)
              if (imported && imported.length > 0) setChildren(null)
            }}
          >
            <Download className="h-3.5 w-3.5" /> Import data…
          </ContextMenuItem>
          <ContextMenuItem variant="destructive" onClick={() => removeProject(project.path)}>
            <Trash2 className="h-3.5 w-3.5" /> Remove from list
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>

      {expanded && (
        <div className="mb-1 mt-0.5 ml-3 border-l border-sidebar-border/40 pl-2">
          {creating && (
            <div className="px-2 py-0.5">
              <InlineNameInput
                key={creating.attempt}
                initialValue={creating.value}
                error={creating.error}
                onCommit={(name) => void handleCreate(creating.kind, name)}
                onCancel={() => setCreating(null)}
              />
            </div>
          )}
          {children === null ? (
            <p className="px-2 py-1 text-[11px] italic text-sidebar-foreground/30">Loading…</p>
          ) : children.length === 0 && !creating ? (
            <p className="px-2 py-1 text-[11px] italic text-sidebar-foreground/30">Empty</p>
          ) : (
            children.map((c) => (
              <FileTreeNode
                key={c.path}
                entry={c}
                onParentRefresh={refresh}
                revealPath={revealPath}
                refreshToken={refreshToken}
                collapseSignal={collapseSignal}
              />
            ))
          )}
        </div>
      )}
    </div>
  )
}

// ─── Projects section ───────────────────────────────────────────────────────────

// Only ever renders the mounted (active) project — the registry's full project list lives in
// TitleBar's ProjectSwitcher dropdown, not duplicated here as a second, independently-expandable
// list. Owns the Explorer toolbar's signals (root-level create target, refresh token, collapse
// signal) since those need to reach into MountedProject/FileTreeNode from outside the tree.
function ProjectsSection() {
  const { activeProject } = useProjects()
  const { activeFilePath } = useEditor()
  const [rootAction, setRootAction] = useState<{ kind: 'file' | 'folder' } | null>(null)
  const [refreshToken, setRefreshToken] = useState(0)
  const [collapseSignal, setCollapseSignal] = useState(0)

  if (!activeProject) {
    return <p className="px-3 py-3 text-[11px] italic text-sidebar-foreground/30">No project mounted — pick one from the title bar</p>
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ExplorerToolbar
        onNewFile={() => setRootAction({ kind: 'file' })}
        onNewFolder={() => setRootAction({ kind: 'folder' })}
        onRefresh={() => setRefreshToken((n) => n + 1)}
        onCollapseAll={() => setCollapseSignal((n) => n + 1)}
      />
      <div className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-2 py-2">
        <MountedProject
          key={activeProject.path}
          project={activeProject}
          rootAction={rootAction}
          refreshToken={refreshToken}
          collapseSignal={collapseSignal}
          revealPath={activeFilePath}
        />
      </div>
    </div>
  )
}

// ─── Sidebar ──────────────────────────────────────────────────────────────────

// No collapsed/narrow-icon mode anymore — that was a pointless halfway state (56px, still
// mounted, still taking space) once LeftRail's Explorer button can just mount/unmount this
// component entirely (see LeftRail.tsx). One show/hide mechanism, not two.
export function Sidebar() {
  return (
    <aside
      // Full border (not just border-r) — same treatment as the dock-content wrapper in
      // AppShell.tsx: both are "child" columns in the same row, so both get their own complete
      // frame instead of just one side. my-2 (not h-full) — vertical breathing room from
      // TitleBar/window-bottom, same reasoning as AppShell.tsx's main-wrapper div: default flex
      // stretch sizes it to the row's height minus the margin, without touching LeftRail's own
      // (unmargined, flush) height.
      className="my-2 flex w-[232px] shrink-0 flex-col overflow-hidden rounded-lg border border-sidebar-border bg-sidebar"
    >
      {/* No brand header here anymore — TitleBar.tsx already shows "Fathom" at the very top of
          the window; having it again here was a duplicate label right below it. */}
      <ProjectsSection />
    </aside>
  )
}
