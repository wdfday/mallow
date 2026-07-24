import { useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { listen } from '@tauri-apps/api/event'
import { AlertCircle, CheckCircle2, Loader2, Send, Wrench } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useProjects } from '@/lib/project-context'

// Self-correcting loop, run entirely in Rust (src-tauri/src/agent) — this panel is a thin
// transcript + input. Progress (tool_call/tool_result) streams in live via the `agent://step`
// Tauri event so the loop is visible ("đang lint…", "đang backtest…"), not a black box the user
// waits on. Rendered inside RightSidebar.tsx, which draws the shared title/close bar — no
// in-content header here.

export type Provider = 'claude' | 'openai' | 'gemini'
export const PROVIDERS: { value: Provider; label: string }[] = [
  { value: 'claude', label: 'Claude' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
]
export const PROVIDER_KEY = 'fathom:agentProvider'

interface AgentStepEvent {
  type: 'tool_call' | 'tool_result'
  name: string
  args?: unknown
  ok?: boolean
  summary?: string
}

interface TranscriptEntry {
  role: 'user' | 'assistant' | 'error'
  text: string
  steps?: AgentStepEvent[]
}

function ToolStepRow({ step }: { step: AgentStepEvent }) {
  if (step.type === 'tool_call') {
    return (
      <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <Wrench className="h-3 w-3 shrink-0" />
        <span className="font-mono">{step.name}</span>
      </div>
    )
  }
  return (
    <div className={cn('flex items-start gap-1.5 pl-4 text-[11px]', step.ok ? 'text-muted-foreground' : 'text-destructive')}>
      {step.ok ? <CheckCircle2 className="mt-0.5 h-3 w-3 shrink-0" /> : <AlertCircle className="mt-0.5 h-3 w-3 shrink-0" />}
      <span className="whitespace-pre-wrap break-words">{step.summary}</span>
    </div>
  )
}

function ApiKeyPrompt({ provider, onSaved }: { provider: Provider; onSaved: () => void }) {
  const [key, setKey] = useState('')
  const [saving, setSaving] = useState(false)

  async function handleSave() {
    if (!key.trim()) return
    setSaving(true)
    try {
      await invoke('agent_set_api_key', { provider, key: key.trim() })
      onSaved()
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex flex-col gap-2 p-3">
      <p className="text-[11px] text-muted-foreground">
        No API key for {PROVIDERS.find((p) => p.value === provider)?.label} yet. Keys are stored in the OS keychain and never leave this machine.
      </p>
      <input
        type="password"
        value={key}
        onChange={(e) => setKey(e.target.value)}
        placeholder="API key…"
        className="h-8 rounded-md border border-border bg-background px-2 text-[12px] outline-none focus:border-secondary/50"
      />
      <button
        onClick={handleSave}
        disabled={saving || !key.trim()}
        className="h-8 rounded-md bg-secondary/15 text-[12px] font-medium text-secondary hover:bg-secondary/25 disabled:cursor-not-allowed disabled:opacity-40"
      >
        {saving ? 'Saving…' : 'Save key'}
      </button>
    </div>
  )
}

export function AgentPanel() {
  const { activeProject } = useProjects()
  const [provider, setProvider] = useState<Provider>(
    () => (localStorage.getItem(PROVIDER_KEY) as Provider | null) ?? 'claude',
  )
  const [hasKey, setHasKey] = useState<boolean | null>(null)
  const [transcript, setTranscript] = useState<TranscriptEntry[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [liveSteps, setLiveSteps] = useState<AgentStepEvent[]>([])
  const scrollRef = useRef<HTMLDivElement>(null)
  // handleSend reads this after an `await` — a ref mirrors `liveSteps` so the steps folded into
  // the finished transcript entry aren't a stale closure snapshot from before any
  // tool_call/tool_result events arrived.
  const liveStepsRef = useRef<AgentStepEvent[]>([])

  useEffect(() => {
    localStorage.setItem(PROVIDER_KEY, provider)
    setHasKey(null)
    invoke<boolean>('agent_has_api_key', { provider }).then(setHasKey)
  }, [provider])

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight })
  }, [transcript, liveSteps])

  useEffect(() => { liveStepsRef.current = liveSteps }, [liveSteps])

  async function handleSend() {
    const message = input.trim()
    if (!message || !activeProject || sending) return
    setInput('')
    setSending(true)
    setLiveSteps([])
    setTranscript((prev) => [...prev, { role: 'user', text: message }])

    const unlisten = await listen<AgentStepEvent>('agent://step', (event) => {
      setLiveSteps((prev) => [...prev, event.payload])
    })

    try {
      const reply = await invoke<string>('agent_send', {
        provider,
        projectPath: activeProject.path,
        message,
      })
      setTranscript((prev) => [...prev, { role: 'assistant', text: reply, steps: liveStepsRef.current }])
    } catch (err) {
      const text = typeof err === 'string' ? err : err instanceof Error ? err.message : 'Agent failed with an unknown error'
      setTranscript((prev) => [...prev, { role: 'error', text, steps: liveStepsRef.current }])
    } finally {
      unlisten()
      setLiveSteps([])
      setSending(false)
    }
  }

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden bg-background">
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1.5">
        {PROVIDERS.map((p) => (
          <button
            key={p.value}
            onClick={() => setProvider(p.value)}
            className={cn(
              'rounded-md px-1.5 py-0.5 text-[10px] font-medium',
              provider === p.value ? 'bg-secondary/15 text-secondary' : 'text-muted-foreground hover:bg-muted/40',
            )}
          >
            {p.label}
          </button>
        ))}
      </div>

      {!activeProject ? (
        <div className="flex flex-1 items-center justify-center p-3 text-center text-xs text-muted-foreground">
          Mở 1 project trước khi dùng AI Agent.
        </div>
      ) : hasKey === false ? (
        <ApiKeyPrompt provider={provider} onSaved={() => setHasKey(true)} />
      ) : hasKey === null ? (
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        </div>
      ) : (
        <>
          <div ref={scrollRef} className="flex min-h-0 flex-1 flex-col gap-2.5 overflow-y-auto p-2.5">
            {transcript.length === 0 && (
              <p className="text-[11px] italic text-muted-foreground">
                Yêu cầu agent viết/sửa 1 chiến lược Rhai — nó sẽ tự lint, backtest, và sửa nếu kết quả kém.
              </p>
            )}
            {transcript.map((entry, i) => (
              <div key={i} className="flex flex-col gap-1">
                {entry.role === 'user' ? (
                  <div className="self-end rounded-md bg-secondary/15 px-2 py-1.5 text-[12px] text-foreground">
                    {entry.text}
                  </div>
                ) : (
                  <div className="flex flex-col gap-1">
                    {entry.steps && entry.steps.length > 0 && (
                      <div className="flex flex-col gap-0.5 rounded-md border border-border/60 bg-muted/20 p-1.5">
                        {entry.steps.map((s, j) => <ToolStepRow key={j} step={s} />)}
                      </div>
                    )}
                    <div className={cn(
                      'whitespace-pre-wrap rounded-md px-2 py-1.5 text-[12px]',
                      entry.role === 'error' ? 'bg-destructive/10 text-destructive' : 'bg-muted/30 text-foreground',
                    )}>
                      {entry.text}
                    </div>
                  </div>
                )}
              </div>
            ))}
            {sending && (
              <div className="flex flex-col gap-0.5 rounded-md border border-border/60 bg-muted/20 p-1.5">
                {liveSteps.length === 0 ? (
                  <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                    <Loader2 className="h-3 w-3 animate-spin" /> Thinking…
                  </div>
                ) : (
                  liveSteps.map((s, j) => <ToolStepRow key={j} step={s} />)
                )}
              </div>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1.5 border-t border-border p-2">
            <input
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void handleSend() } }}
              disabled={sending}
              placeholder="Message the agent…"
              className="h-8 flex-1 rounded-md border border-border bg-background px-2 text-[12px] outline-none focus:border-secondary/50 disabled:opacity-50"
            />
            <button
              onClick={() => void handleSend()}
              disabled={sending || !input.trim()}
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-secondary/15 text-secondary hover:bg-secondary/25 disabled:cursor-not-allowed disabled:opacity-40"
            >
              <Send className="h-3.5 w-3.5" />
            </button>
          </div>
        </>
      )}
    </div>
  )
}
