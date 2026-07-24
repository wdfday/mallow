import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { Moon, Sun } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useTheme, THEME_PALETTES, CORNER_STYLES, type ThemePalette, type CornerStyle } from '@/lib/theme-context'
import { useAuth } from '@/lib/auth-context'
import { PROVIDERS, PROVIDER_KEY, type Provider } from './AgentPanel'

// Account (session list/revoke, sign out), AI Agent (provider/key — same agent_set_api_key
// command AgentPanel's inline prompt uses, not a separate mechanism to reconcile), and theme.
// Rendered inside RightSidebar.tsx, which draws the shared title/close bar — no in-content
// header here.

interface SessionInfo {
  sid: string
  ip: string
  user_agent: string
  created_at: string
  expires_at: string
  is_current: boolean
}

function AccountSection() {
  const { user, logout } = useAuth()
  const [sessions, setSessions] = useState<SessionInfo[] | null>(null)
  const [busy, setBusy] = useState(false)

  function refresh() {
    invoke<SessionInfo[]>('auth_list_sessions').then(setSessions).catch(() => setSessions([]))
  }

  useEffect(refresh, [])

  async function revoke(sid: string) {
    setBusy(true)
    try {
      await invoke('auth_revoke_session', { sid })
      refresh()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mb-4">
      <p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Account</p>
      <p className="mb-2 truncate text-[12px] font-medium">{user?.full_name || user?.email}</p>

      <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        Sessions {sessions ? `(${sessions.length})` : ''}
      </p>
      <div className="mb-2 flex max-h-32 flex-col gap-1 overflow-y-auto">
        {sessions === null ? (
          <span className="text-[11px] text-muted-foreground">Loading…</span>
        ) : (
          sessions.map((s) => (
            <div key={s.sid} className="flex items-center gap-1.5 rounded-md border border-border/60 px-1.5 py-1 text-[11px]">
              <span className="min-w-0 flex-1 truncate text-muted-foreground" title={s.user_agent}>
                {s.ip} {s.is_current && <span className="text-secondary">(current)</span>}
              </span>
              {!s.is_current && (
                <button
                  onClick={() => void revoke(s.sid)}
                  disabled={busy}
                  className="shrink-0 text-destructive hover:underline disabled:opacity-40"
                >
                  Revoke
                </button>
              )}
            </div>
          ))
        )}
      </div>

      <div className="flex gap-1.5">
        <button
          onClick={() => void logout()}
          className="h-7 flex-1 rounded-md border border-border text-[11px] font-medium hover:border-destructive/50 hover:text-destructive"
        >
          Sign out
        </button>
        <button
          onClick={async () => { setBusy(true); try { await invoke('auth_revoke_all_sessions'); await logout() } finally { setBusy(false) } }}
          disabled={busy}
          className="h-7 flex-1 rounded-md border border-border text-[11px] font-medium text-destructive hover:border-destructive/50 disabled:opacity-40"
        >
          Sign out all devices
        </button>
      </div>
    </div>
  )
}

function AgentSettingsSection() {
  const [provider, setProvider] = useState<Provider>(
    () => (localStorage.getItem(PROVIDER_KEY) as Provider | null) ?? 'claude',
  )
  const [hasKey, setHasKey] = useState<boolean | null>(null)
  const [key, setKey] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    localStorage.setItem(PROVIDER_KEY, provider)
    setHasKey(null)
    invoke<boolean>('agent_has_api_key', { provider }).then(setHasKey)
  }, [provider])

  async function handleSave() {
    if (!key.trim()) return
    setSaving(true)
    try {
      await invoke('agent_set_api_key', { provider, key: key.trim() })
      setHasKey(true)
      setKey('')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mb-4">
      <p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">AI Agent</p>
      <div className="mb-2 flex gap-1.5">
        {PROVIDERS.map((p) => (
          <button
            key={p.value}
            onClick={() => setProvider(p.value)}
            className={cn(
              'rounded-md border px-2 py-1 text-[11px] transition-colors',
              provider === p.value
                ? 'border-secondary bg-secondary/10 text-secondary'
                : 'border-border text-muted-foreground hover:border-secondary/40',
            )}
          >
            {p.label}
          </button>
        ))}
      </div>
      <div className="flex gap-1.5">
        <input
          type="password"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder={hasKey ? 'Key saved — enter a new one to replace' : 'API key…'}
          className="h-7 flex-1 rounded-md border border-border bg-background px-2 text-[11px] outline-none focus:border-secondary/50"
        />
        <button
          onClick={() => void handleSave()}
          disabled={saving || !key.trim()}
          className="h-7 shrink-0 rounded-md bg-secondary/15 px-2 text-[11px] font-medium text-secondary hover:bg-secondary/25 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {saving ? '…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

const CORNER_LABELS: Record<CornerStyle, string> = {
  auto: 'palette default',
  rounded: 'rounded',
  square: 'square',
}

export function SettingsPanel() {
  const { themeMode, setThemeMode, themePalette, setThemePalette, cornerStyle, setCornerStyle } = useTheme()
  return (
    <div className="h-full min-w-0 overflow-y-auto bg-background p-3">
      <AccountSection />
      <AgentSettingsSection />
      <div className="mb-4">
        <p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Appearance</p>
        <div className="flex gap-1.5">
          <button
            onClick={() => setThemeMode('foundation')}
            className={cn(
              'flex flex-1 items-center justify-center gap-1.5 rounded-md border px-2 py-1.5 text-[11px] font-medium transition-colors',
              themeMode === 'foundation'
                ? 'border-secondary bg-secondary/10 text-secondary'
                : 'border-border text-muted-foreground hover:border-secondary/40',
            )}
          >
            <Sun className="h-3.5 w-3.5" /> Light
          </button>
          <button
            onClick={() => setThemeMode('midnight')}
            className={cn(
              'flex flex-1 items-center justify-center gap-1.5 rounded-md border px-2 py-1.5 text-[11px] font-medium transition-colors',
              themeMode === 'midnight'
                ? 'border-secondary bg-secondary/10 text-secondary'
                : 'border-border text-muted-foreground hover:border-secondary/40',
            )}
          >
            <Moon className="h-3.5 w-3.5" /> Dark
          </button>
        </div>
      </div>
      <div className="mb-4">
        <p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Theme palette</p>
        <div className="flex flex-wrap gap-1.5">
          {THEME_PALETTES.map((p: ThemePalette) => (
            <button
              key={p}
              onClick={() => setThemePalette(p)}
              className={cn(
                'rounded-md border px-2 py-1 text-[11px] capitalize transition-colors',
                p === themePalette
                  ? 'border-secondary bg-secondary/10 text-secondary'
                  : 'border-border text-muted-foreground hover:border-secondary/40',
              )}
            >
              {p}
            </button>
          ))}
        </div>
      </div>
      <div>
        <p className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Corners</p>
        <div className="flex flex-wrap gap-1.5">
          {CORNER_STYLES.map((c) => (
            <button
              key={c}
              onClick={() => setCornerStyle(c)}
              className={cn(
                'rounded-md border px-2 py-1 text-[11px] transition-colors',
                c === cornerStyle
                  ? 'border-secondary bg-secondary/10 text-secondary'
                  : 'border-border text-muted-foreground hover:border-secondary/40',
              )}
            >
              {CORNER_LABELS[c]}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
