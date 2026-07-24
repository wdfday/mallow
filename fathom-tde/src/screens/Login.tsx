import { useState, type FormEvent } from 'react'
import { AlertCircle, FlaskConical } from 'lucide-react'
import { useAuth } from '@/lib/auth-context'
import { Spinner } from '@/components/ui/spinner'
import { cn } from '@/lib/utils'

// Layout ported from mallow-client/app/auth/login/page.tsx (centered card, email/password +
// Google button) — the navigation/redirect mechanics underneath are NOT ported, since they don't
// apply to a desktop app: Google here goes through auth-context's `loginWithGoogle()`, which is a
// single Tauri command (`auth_google_start`) that owns the whole system-browser + loopback-PKCE
// dance in Rust, not a client-side redirect/callback page pair.

export function Login() {
  const { login, loginWithGoogle } = useAuth()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [googleSubmitting, setGoogleSubmitting] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setFormError(null)
    setSubmitting(true)
    try {
      await login(email, password)
    } catch (err) {
      setFormError(typeof err === 'string' ? err : 'Sign-in failed')
    } finally {
      setSubmitting(false)
    }
  }

  async function handleGoogle() {
    setFormError(null)
    setGoogleSubmitting(true)
    try {
      await loginWithGoogle()
    } catch (err) {
      setFormError(typeof err === 'string' ? err : 'Google sign-in failed')
    } finally {
      setGoogleSubmitting(false)
    }
  }

  const busy = submitting || googleSubmitting

  return (
    <div data-tauri-drag-region className="flex h-screen w-screen items-center justify-center bg-background">
      <div className="w-full max-w-[340px] rounded-lg border border-border bg-card p-6">
        <div className="mb-6 flex flex-col items-center gap-2">
          <FlaskConical className="h-7 w-7 text-secondary" />
          <h1 className="text-sm font-semibold text-foreground">Sign in to fathom</h1>
          <p className="text-center text-[11px] text-muted-foreground">
            Same account as mallow — signs in through the hosted identity service.
          </p>
        </div>

        {formError && (
          <div className="mb-4 flex items-start gap-1.5 rounded-md border border-destructive/30 bg-destructive/10 px-2.5 py-2 text-[11px] text-destructive">
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>{formError}</span>
          </div>
        )}

        <form onSubmit={handleSubmit} className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <label className="text-[11px] font-medium text-muted-foreground" htmlFor="email">Email</label>
            <input
              id="email"
              type="email"
              required
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              disabled={busy}
              className="h-8 rounded-md border border-border bg-background px-2.5 text-[12px] outline-none focus:border-secondary/50 disabled:opacity-50"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-[11px] font-medium text-muted-foreground" htmlFor="password">Password</label>
            <input
              id="password"
              type="password"
              required
              minLength={8}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={busy}
              className="h-8 rounded-md border border-border bg-background px-2.5 text-[12px] outline-none focus:border-secondary/50 disabled:opacity-50"
            />
          </div>
          <button
            type="submit"
            disabled={busy}
            className={cn(
              'mt-1 flex h-8 items-center justify-center gap-1.5 rounded-md bg-secondary/15 text-[12px] font-medium text-secondary transition-colors hover:bg-secondary/25 disabled:cursor-not-allowed disabled:opacity-50',
            )}
          >
            {submitting && <Spinner className="h-3.5 w-3.5" />}
            Sign in
          </button>
        </form>

        <div className="my-4 flex items-center gap-2">
          <div className="h-px flex-1 bg-border" />
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground">or</span>
          <div className="h-px flex-1 bg-border" />
        </div>

        <button
          onClick={handleGoogle}
          disabled={busy}
          className="flex h-8 w-full items-center justify-center gap-1.5 rounded-md border border-border text-[12px] font-medium text-foreground/80 transition-colors hover:border-secondary/50 hover:text-secondary disabled:cursor-not-allowed disabled:opacity-50"
        >
          {googleSubmitting ? <Spinner className="h-3.5 w-3.5" /> : null}
          {googleSubmitting ? 'Opening browser…' : 'Sign in with Google'}
        </button>
      </div>
    </div>
  )
}
