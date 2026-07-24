import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { invoke } from '@tauri-apps/api/core'
import { listen, type UnlistenFn } from '@tauri-apps/api/event'
import { useProjects } from '@/lib/project-context'
import { useTheme } from '@/lib/theme-context'

// Real PTY (src-tauri/src/terminal — portable-pty, not a log viewer): one shell session per
// active project, rooted at its directory. Spawned when this panel mounts (or the active project
// changes), killed on unmount/project-switch — not a background process fathom keeps running
// after the tab closes. v1 is a single terminal, no split panes/multi-tab (out of scope, see the
// plan).

const THEME = {
  midnight: { background: '#101820', foreground: '#e2e8f0', cursor: '#e2e8f0' },
  foundation: { background: '#ffffff', foreground: '#1e293b', cursor: '#1e293b' },
}

export function CliPanel() {
  const { activeProject } = useProjects()
  const { themeMode } = useTheme()
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)

  useEffect(() => {
    if (!activeProject || !containerRef.current) return
    const container = containerRef.current

    const term = new Terminal({
      fontSize: 12,
      fontFamily: "'JetBrains Mono', monospace",
      theme: THEME[themeMode === 'midnight' ? 'midnight' : 'foundation'],
      cursorBlink: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(container)
    fit.fit()
    termRef.current = term

    let unlistenOutput: UnlistenFn | null = null
    let unlistenExit: UnlistenFn | null = null
    let sessionId: number | null = null
    let disposed = false

    invoke<number>('terminal_spawn', { cwd: activeProject.path, rows: term.rows, cols: term.cols })
      .then(async (id) => {
        if (disposed) {
          void invoke('terminal_kill', { id })
          return
        }
        sessionId = id
        unlistenOutput = await listen<string>(`terminal://output/${id}`, (e) => term.write(e.payload))
        unlistenExit = await listen(`terminal://exit/${id}`, () => term.write('\r\n[process exited]\r\n'))
      })
      .catch((err) => {
        term.write(`\r\nFailed to start terminal: ${err instanceof Error ? err.message : String(err)}\r\n`)
      })

    const dataDisposable = term.onData((data) => {
      if (sessionId !== null) void invoke('terminal_write', { id: sessionId, data })
    })

    const ro = new ResizeObserver(() => {
      fit.fit()
      if (sessionId !== null) void invoke('terminal_resize', { id: sessionId, rows: term.rows, cols: term.cols })
    })
    ro.observe(container)

    return () => {
      disposed = true
      ro.disconnect()
      dataDisposable.dispose()
      unlistenOutput?.()
      unlistenExit?.()
      if (sessionId !== null) void invoke('terminal_kill', { id: sessionId })
      term.dispose()
      termRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- themeMode intentionally excluded:
    // handled by the separate effect below so toggling theme doesn't kill/respawn the shell.
  }, [activeProject?.path])

  useEffect(() => {
    if (termRef.current) {
      termRef.current.options.theme = THEME[themeMode === 'midnight' ? 'midnight' : 'foundation']
    }
  }, [themeMode])

  if (!activeProject) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        Open a project to use the terminal.
      </div>
    )
  }

  return <div ref={containerRef} className="h-full w-full overflow-hidden bg-background p-1" />
}
