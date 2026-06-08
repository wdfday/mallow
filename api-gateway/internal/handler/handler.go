package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
)

// Handler holds shared dependencies for all route handlers.
type Handler struct {
	NC        *nats.Conn
	HeraldURL string
}

func (h *Handler) SwaggerIndex(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Mallow API Docs</title>
  <style>
    :root {
      --bg: #f4f0e8;
      --card: #fffdf8;
      --ink: #1d1a16;
      --muted: #6c6258;
      --line: #d8cfc3;
      --accent: #1b6b73;
      --accent-2: #c96d42;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "IBM Plex Sans", "Segoe UI", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(circle at top left, rgba(201,109,66,.18), transparent 24rem),
        radial-gradient(circle at bottom right, rgba(27,107,115,.18), transparent 28rem),
        var(--bg);
      min-height: 100vh;
    }
    main {
      max-width: 920px;
      margin: 0 auto;
      padding: 48px 20px 64px;
    }
    h1 {
      margin: 0 0 12px;
      font-size: clamp(2.4rem, 6vw, 4.8rem);
      line-height: .94;
      letter-spacing: -.05em;
    }
    p.lead {
      max-width: 48rem;
      margin: 0 0 32px;
      color: var(--muted);
      font-size: 1.05rem;
      line-height: 1.6;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
      gap: 18px;
    }
    .card {
      background: var(--card);
      border: 1px solid var(--line);
      border-radius: 20px;
      padding: 22px;
      box-shadow: 0 10px 30px rgba(29,26,22,.06);
    }
    .eyebrow {
      margin: 0 0 10px;
      font-size: .82rem;
      letter-spacing: .08em;
      text-transform: uppercase;
      color: var(--accent);
      font-weight: 700;
    }
    h2 {
      margin: 0 0 10px;
      font-size: 1.35rem;
    }
    .card p {
      margin: 0 0 18px;
      color: var(--muted);
      line-height: 1.55;
    }
    .actions {
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
    }
    a.button {
      display: inline-block;
      text-decoration: none;
      color: white;
      background: var(--accent);
      padding: 10px 14px;
      border-radius: 999px;
      font-weight: 600;
    }
    a.link {
      display: inline-block;
      text-decoration: none;
      color: var(--accent-2);
      font-weight: 600;
      padding: 10px 0;
    }
    code {
      font-family: "IBM Plex Mono", monospace;
      font-size: .92em;
      background: rgba(27,107,115,.08);
      padding: .12rem .35rem;
      border-radius: .4rem;
    }
  </style>
</head>
<body>
  <main>
    <p class="eyebrow">Mallow Gateway</p>
    <h1>API docs through gateway</h1>
    <p class="lead">
      Open each service Swagger UI from <code>:8080</code>. The docs themselves are proxied by the gateway,
      so once you authorize with a JWT, requests go through the gateway path and preserve the real header-injection flow.
    </p>
    <section class="grid">
      <article class="card">
        <p class="eyebrow">Identity</p>
        <h2>Auth, user, profile</h2>
        <p>Public auth routes and protected identity routes exposed under <code>/api/v1</code>.</p>
        <div class="actions">
          <a class="button" href="/api/v1/swagger/index.html">Open Swagger UI</a>
          <a class="link" href="/api/v1/swagger/doc.json">Open spec JSON</a>
        </div>
      </article>

      <article class="card">
        <p class="eyebrow">Helm</p>
        <h2>Helms and hands APIs</h2>
        <p>Trade execution service. Helms at <code>/api/v1/helms</code>, autonomous bots (hands) at <code>/api/v1/hands</code>.</p>
        <div class="actions">
          <a class="button" href="/swagger/orchestrator/index.html">Open Swagger UI</a>
          <a class="link" href="/swagger/orchestrator/doc.json">Open spec JSON</a>
        </div>
      </article>
      <article class="card">
        <p class="eyebrow">Herald (Rust Signal Engine)</p>
        <h2>Backtesting, signals &amp; data</h2>
        <p>80+ strategies, 66 indicators. <code>POST /api/backtest</code> — run backtest. <code>GET /api/symbols</code> — available data. <code>GET /api/data/{symbol}</code> — OHLCV bars. <code>GET /api/stream/{symbol}</code> — SSE bar stream.</p>
        <div class="actions">
          <a class="button" href="/swagger/herald">Open Swagger UI</a>
          <a class="link" href="/api/strategies">List strategies</a>
          <a class="link" href="/api/symbols">List symbols</a>
        </div>
      </article>
      <article class="card">
        <p class="eyebrow">Gateway</p>
        <h2>This gateway</h2>
        <p>JWT auth, rate limiting, reverse proxy to all upstream services.</p>
        <div class="actions">
          <a class="button" href="/swagger/gateway/index.html">Open Swagger UI</a>
        </div>
      </article>
    </section>
  </main>
</body>
</html>`))
}

// ── Health ──────────────────────────────────────────────────────────

// Health godoc
//
// @Summary      Health check
// @Description  Returns gateway status and NATS connectivity.
// @Tags         System
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Router       /health [get]
func (h *Handler) Health(c *gin.Context) {
	natsOK := h.NC != nil && h.NC.IsConnected()

	heraldOK := false
	{
		// Use a short-timeout client — /health is probed frequently by load
		// balancers. http.DefaultClient has no timeout; a hung herald would
		// block every health probe goroutine indefinitely.
		hc := &http.Client{Timeout: 3 * time.Second}
		if resp, err := hc.Get(h.HeraldURL + "/health"); err == nil {
			heraldOK = resp.StatusCode == http.StatusOK
			resp.Body.Close()
		}
	}

	status := "ok"
	if !natsOK || !heraldOK {
		status = "degraded"
	}
	c.JSON(http.StatusOK, gin.H{
		"status": status,
		"nats":   natsOK,
		"herald": heraldOK,
	})
}
