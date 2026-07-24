import { useEffect, useState } from 'react'
import { Loader2, Plug, Plus, Power, RefreshCw, Trash2, Zap } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/lib/auth-context'

// Broker connection management — calls helm's `/api/v1/broker-connections/*` directly (forwarded
// byte-for-byte unchanged by the gateway, unlike /helms and /hands which get a path rewrite —
// confirmed via api-gateway/internal/handler/proxy.go). Shapes verified against
// helm/internal/module/broker/{handler,dto,domain}/*.go, not guessed — in particular:
//   - credentials (api_key/api_secret/passphrase) are `json:"-"` server-side, NEVER returned by
//     any GET — there is nothing to "show" for an existing connection's key, only rotate it blind.
//   - there is NO `rebroker` action — only activate/deactivate/test/rotate-key/delete exist.
//   - required credential fields differ per provider (OKX needs `passphrase`, others don't) —
//     driven off `/providers`' `required_fields`, not hardcoded per broker.
//   - Bybit's create route is live but hidden from `/providers` server-side (commented out) — so
//     it naturally won't appear in the "add connection" picker here either, matching that intent.

interface BrokerProviderInfo {
  broker_type: string
  display_name: string
  description: string
  required_fields: string[]
  supported_features: string[]
}

type ConnStatus = 'active' | 'disconnected' | 'error' | 'pending'

interface BrokerConnection {
  id: string
  user_id: string
  broker_type: string
  broker_name: string
  status: ConnStatus
  is_paper: boolean
  notes?: string
  created_at: string
  updated_at: string
}

const STATUS_TONE: Record<ConnStatus, string> = {
  active: 'text-emerald-500 bg-emerald-500/10',
  pending: 'text-amber-500 bg-amber-500/10',
  disconnected: 'text-muted-foreground bg-muted/30',
  error: 'text-destructive bg-destructive/10',
}

const ACCOUNT_TYPES = ['spot', 'futures_usdm', 'futures_coinm', 'unified']

function StatusBadge({ status }: { status: ConnStatus }) {
  return <span className={cn('rounded px-1.5 py-0.5 text-[10px] font-medium capitalize', STATUS_TONE[status])}>{status}</span>
}

function CreateForm({ providers, onCreated, onCancel }: {
  providers: BrokerProviderInfo[]
  onCreated: () => void
  onCancel: () => void
}) {
  const { apiFetch } = useAuth()
  const [providerType, setProviderType] = useState(providers[0]?.broker_type ?? '')
  const [brokerName, setBrokerName] = useState('')
  const [accountType, setAccountType] = useState('')
  const [isPaper, setIsPaper] = useState(true)
  const [notes, setNotes] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const provider = providers.find((p) => p.broker_type === providerType)

  async function handleSubmit() {
    if (!provider || !brokerName.trim()) return
    if (provider.required_fields.some((f) => !fields[f]?.trim())) {
      setError('Fill in all required credential fields')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await apiFetch(`/api/v1/broker-connections/${provider.broker_type}`, {
        method: 'POST',
        body: JSON.stringify({
          broker_name: brokerName.trim(),
          account_type: accountType || undefined,
          notes: notes.trim() || undefined,
          is_paper: isPaper,
          ...fields,
        }),
      })
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="flex flex-col gap-2 border-b border-border p-2.5">
      <select
        value={providerType}
        onChange={(e) => { setProviderType(e.target.value); setFields({}) }}
        className="h-7 rounded-md border border-border bg-background px-1.5 text-[11px]"
      >
        {providers.map((p) => <option key={p.broker_type} value={p.broker_type}>{p.display_name}</option>)}
      </select>
      <input
        value={brokerName}
        onChange={(e) => setBrokerName(e.target.value)}
        placeholder="Connection name (e.g. Demo Binance spot)"
        className="h-7 rounded-md border border-border bg-background px-1.5 text-[11px] outline-none focus:border-secondary/50"
      />
      <select
        value={accountType}
        onChange={(e) => setAccountType(e.target.value)}
        className="h-7 rounded-md border border-border bg-background px-1.5 text-[11px]"
      >
        <option value="">Account type (default)</option>
        {ACCOUNT_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
      </select>
      {provider?.required_fields.map((f) => (
        <input
          key={f}
          type="password"
          value={fields[f] ?? ''}
          onChange={(e) => setFields((prev) => ({ ...prev, [f]: e.target.value }))}
          placeholder={f}
          className="h-7 rounded-md border border-border bg-background px-1.5 text-[11px] outline-none focus:border-secondary/50"
        />
      ))}
      <label className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <input type="checkbox" checked={isPaper} onChange={(e) => setIsPaper(e.target.checked)} />
        Paper trading
      </label>
      <input
        value={notes}
        onChange={(e) => setNotes(e.target.value)}
        placeholder="Notes (optional)"
        className="h-7 rounded-md border border-border bg-background px-1.5 text-[11px] outline-none focus:border-secondary/50"
      />
      {error && <p className="text-[10px] text-destructive">{error}</p>}
      <div className="flex gap-1.5">
        <button
          onClick={() => void handleSubmit()}
          disabled={submitting}
          className="h-7 flex-1 rounded-md bg-secondary/15 text-[11px] font-medium text-secondary hover:bg-secondary/25 disabled:opacity-40"
        >
          {submitting ? 'Creating…' : 'Create connection'}
        </button>
        <button onClick={onCancel} className="h-7 rounded-md border border-border px-2 text-[11px] text-muted-foreground">
          Cancel
        </button>
      </div>
    </div>
  )
}

function ConnectionRow({ conn, onAction, busyAction }: {
  conn: BrokerConnection
  onAction: (id: string, action: 'activate' | 'deactivate' | 'test' | 'delete') => void
  busyAction: string | null
}) {
  return (
    <div className="border-b border-border/60 px-2.5 py-2">
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-[12px] font-medium">{conn.broker_name}</span>
        <StatusBadge status={conn.status} />
      </div>
      <p className="mt-0.5 text-[10px] text-muted-foreground">
        {conn.broker_type}{conn.is_paper ? ' · paper' : ''}
      </p>
      {conn.notes && <p className="mt-0.5 truncate text-[10px] text-muted-foreground" title={conn.notes}>{conn.notes}</p>}
      <div className="mt-1.5 flex flex-wrap gap-1">
        {conn.status !== 'active' && (
          <button
            onClick={() => onAction(conn.id, 'activate')}
            disabled={busyAction === `${conn.id}:activate`}
            className="flex items-center gap-1 rounded-md border border-border px-1.5 py-1 text-[10px] text-foreground/70 hover:border-secondary/50 hover:text-secondary disabled:opacity-40"
          >
            {busyAction === `${conn.id}:activate` ? <Loader2 className="h-3 w-3 animate-spin" /> : <Power className="h-3 w-3" />} Activate
          </button>
        )}
        {conn.status === 'active' && (
          <button
            onClick={() => onAction(conn.id, 'deactivate')}
            disabled={busyAction === `${conn.id}:deactivate`}
            className="flex items-center gap-1 rounded-md border border-border px-1.5 py-1 text-[10px] text-foreground/70 hover:border-secondary/50 hover:text-secondary disabled:opacity-40"
          >
            {busyAction === `${conn.id}:deactivate` ? <Loader2 className="h-3 w-3 animate-spin" /> : <Power className="h-3 w-3" />} Deactivate
          </button>
        )}
        <button
          onClick={() => onAction(conn.id, 'test')}
          disabled={busyAction === `${conn.id}:test`}
          className="flex items-center gap-1 rounded-md border border-border px-1.5 py-1 text-[10px] text-foreground/70 hover:border-secondary/50 hover:text-secondary disabled:opacity-40"
        >
          {busyAction === `${conn.id}:test` ? <Loader2 className="h-3 w-3 animate-spin" /> : <Zap className="h-3 w-3" />} Test
        </button>
        <button
          onClick={() => onAction(conn.id, 'delete')}
          disabled={busyAction === `${conn.id}:delete`}
          className="flex items-center gap-1 rounded-md border border-destructive/30 px-1.5 py-1 text-[10px] text-destructive hover:bg-destructive/10 disabled:opacity-40"
        >
          {busyAction === `${conn.id}:delete` ? <Loader2 className="h-3 w-3 animate-spin" /> : <Trash2 className="h-3 w-3" />} Delete
        </button>
      </div>
    </div>
  )
}

export function BrokerPanel() {
  const { apiFetch } = useAuth()
  const [providers, setProviders] = useState<BrokerProviderInfo[] | null>(null)
  const [connections, setConnections] = useState<BrokerConnection[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [busyAction, setBusyAction] = useState<string | null>(null)

  function refresh() {
    apiFetch<{ connections: BrokerConnection[]; total: number }>('/api/v1/broker-connections')
      .then((r) => setConnections(r.connections))
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }

  useEffect(() => {
    apiFetch<{ providers: BrokerProviderInfo[] }>('/api/v1/broker-connections/providers')
      .then((r) => setProviders(r.providers))
      .catch(() => setProviders([]))
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- fetch once on mount
  }, [])

  async function runAction(id: string, action: 'activate' | 'deactivate' | 'test' | 'delete') {
    setBusyAction(`${id}:${action}`)
    try {
      if (action === 'delete') {
        await apiFetch(`/api/v1/broker-connections/${id}`, { method: 'DELETE' })
      } else {
        await apiFetch(`/api/v1/broker-connections/${id}/${action}`, { method: 'POST' })
      }
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusyAction(null)
    }
  }

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden bg-background">
      <div className="flex shrink-0 items-center justify-between border-b border-border px-2.5 py-1.5">
        <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
          Connections {connections ? `(${connections.length})` : ''}
        </span>
        <div className="flex gap-1">
          <button onClick={refresh} title="Refresh" className="text-muted-foreground hover:text-foreground">
            <RefreshCw className="h-3.5 w-3.5" />
          </button>
          <button
            onClick={() => setShowCreate((p) => !p)}
            title="Add connection"
            className={cn('text-muted-foreground hover:text-foreground', showCreate && 'text-secondary')}
          >
            <Plus className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      {error && <p className="border-b border-border px-2.5 py-1.5 text-[10px] text-destructive">{error}</p>}
      {showCreate && providers && (
        providers.length === 0 ? (
          <p className="border-b border-border p-2.5 text-[11px] italic text-muted-foreground">No providers available</p>
        ) : (
          <CreateForm providers={providers} onCreated={() => { setShowCreate(false); refresh() }} onCancel={() => setShowCreate(false)} />
        )
      )}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {connections === null && !error && (
          <div className="flex flex-1 items-center justify-center p-4"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /></div>
        )}
        {connections?.length === 0 && (
          <div className="flex flex-col items-center gap-1.5 p-4 text-center text-[11px] italic text-muted-foreground">
            <Plug className="h-5 w-5 opacity-30" />
            No broker connections yet
          </div>
        )}
        {connections?.map((c) => (
          <ConnectionRow key={c.id} conn={c} onAction={runAction} busyAction={busyAction} />
        ))}
      </div>
    </div>
  )
}
