package handler

// This file contains swagger-only stub handlers for all reverse-proxied routes.
// None of these functions are registered in the router — actual routing uses
// anonymous proxy closures in app/router.go. The stubs exist solely so that
// swag init can generate accurate OpenAPI documentation.

import "github.com/gin-gonic/gin"

// ── Request / response types ──────────────────────────────────────────────────

// BacktestRequest is the body for POST /api/v1/backtest and /backtest/script.
type BacktestRequest struct {
	Symbol         string                 `json:"symbol" example:"BTCUSDT"`
	Strategy       string                 `json:"strategy,omitempty" example:"RSIStrategy"`
	Script         string                 `json:"script,omitempty" example:"// strategy script"`
	Params         map[string]interface{} `json:"params,omitempty"`
	FromTime       string                 `json:"from_time" example:"2024-01-01T00:00:00Z"`
	ToTime         string                 `json:"to_time" example:"2024-12-31T23:59:59Z"`
	Timeframe      string                 `json:"timeframe,omitempty" example:"M1"`
	InitialCapital float64                `json:"initial_capital" example:"10000"`
	CommissionPct  float64                `json:"commission_pct" example:"0.001"`
	SlippagePct    float64                `json:"slippage_pct,omitempty" example:"0"`
	LotSize        float64                `json:"lot_size,omitempty" example:"0"`
}

// BacktestEstimateRequest is the body for POST /api/v1/backtest/estimate.
type BacktestEstimateRequest struct {
	Symbol    string `json:"symbol" example:"BTCUSDT"`
	Strategy  string `json:"strategy" example:"RSIStrategy"`
	FromTime  string `json:"from_time" example:"2024-01-01T00:00:00Z"`
	ToTime    string `json:"to_time" example:"2024-12-31T23:59:59Z"`
	Timeframe string `json:"timeframe,omitempty" example:"M1"`
}

// DataQueryRequest is the body for POST /api/v1/data/:symbol (OHLCV + indicators).
type DataQueryRequest struct {
	Indicators []map[string]interface{} `json:"indicators,omitempty"`
	From       string                   `json:"from,omitempty" example:"2024-01-01T00:00:00Z"`
	To         string                   `json:"to,omitempty" example:"2024-12-31T23:59:59Z"`
	Limit      int                      `json:"limit,omitempty" example:"500"`
}

// StreamRequest is the body for POST /api/v1/stream/:symbol.
type StreamRequest struct {
	Indicators []map[string]interface{} `json:"indicators,omitempty"`
	Script     string                   `json:"script,omitempty"`
	Timeframe  string                   `json:"timeframe,omitempty" example:"M1"`
}

// ── Herald — data ─────────────────────────────────────────────────────────────

// GetSymbols godoc
//
// @Summary      List live symbols
// @Description  Returns all symbols currently ingested by herald.
// @Tags         Herald
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}   string
// @Router       /api/v1/symbols [get]
func (h *Handler) GetSymbols(c *gin.Context) {}

// GetIndicators godoc
//
// @Summary      List available indicators
// @Description  Returns all ~66 indicator names that herald supports.
// @Tags         Herald
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}   string
// @Router       /api/v1/indicators [get]
func (h *Handler) GetIndicators(c *gin.Context) {}

// GetLatestBar godoc
//
// @Summary      Latest OHLCV bar
// @Description  Returns the most recent bar and live indicator snapshot for a symbol.
// @Tags         Herald
// @Security     BearerAuth
// @Produce      json
// @Param        symbol  path      string  true  "Symbol, e.g. BTCUSDT"
// @Success      200     {object}  map[string]interface{}
// @Router       /api/v1/data/{symbol} [get]
func (h *Handler) GetLatestBar(c *gin.Context) {}

// GetLatestBarAlias godoc
//
// @Summary      Latest OHLCV bar (alias)
// @Description  Alias for GET /api/v1/data/:symbol.
// @Tags         Herald
// @Security     BearerAuth
// @Produce      json
// @Param        symbol  path      string  true  "Symbol"
// @Success      200     {object}  map[string]interface{}
// @Router       /api/v1/data/{symbol}/latest [get]
func (h *Handler) GetLatestBarAlias(c *gin.Context) {}

// QueryBars godoc
//
// @Summary      Query OHLCV bars + indicators
// @Description  Returns historical bars with optional indicator overlay. Falls back to DuckDB Parquet when the requested range predates the live ledger window.
// @Tags         Herald
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        symbol  path      string            true  "Symbol, e.g. BTCUSDT"
// @Param        body    body      DataQueryRequest  false "Query parameters"
// @Success      200     {object}  map[string]interface{}
// @Router       /api/v1/data/{symbol} [post]
func (h *Handler) QueryBars(c *gin.Context) {}

// QueryDuckDB godoc
//
// @Summary      DuckDB Parquet query
// @Description  Run a raw Parquet query via DuckDB on historical data.
// @Tags         Herald
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "DuckDB query payload"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/data/duckdb [post]
func (h *Handler) QueryDuckDB(c *gin.Context) {}

// ── Herald — backtest ────────────────────────────────────────────────────────

// GetStrategies godoc
//
// @Summary      List available strategies
// @Description  Returns all ~80 named strategy IDs that herald supports.
// @Tags         Herald
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}  string
// @Router       /api/v1/strategies [get]
func (h *Handler) GetStrategies(c *gin.Context) {}

// RunBacktest godoc
//
// @Summary      Run a backtest
// @Description  Executes a named strategy backtest over a historical date range. Returns a BacktestReport with Sharpe, Sortino, Calmar, max drawdown, win rate, profit factor, and trade list.
// @Tags         Herald
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      BacktestRequest        true  "Backtest parameters"
// @Success      200   {object}  map[string]interface{}
// @Failure      400   {object}  map[string]interface{}
// @Router       /api/v1/backtest [post]
func (h *Handler) RunBacktest(c *gin.Context) {}

// EstimateBacktest godoc
//
// @Summary      Estimate backtest cost
// @Description  Returns estimated bar count and time without running the full engine.
// @Tags         Herald
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      BacktestEstimateRequest  true  "Estimate parameters"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/backtest/estimate [post]
func (h *Handler) EstimateBacktest(c *gin.Context) {}

// RunScriptBacktest godoc
//
// @Summary      Run a script backtest
// @Description  Executes a script strategy. Always saves the script version and result to the store. Pass strategy_id to link to an existing version chain.
// @Tags         Herald
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      BacktestRequest        true  "Script backtest parameters (use 'script' field)"
// @Success      200   {object}  map[string]interface{}
// @Failure      400   {object}  map[string]interface{}
// @Router       /api/v1/backtest/script [post]
func (h *Handler) RunScriptBacktest(c *gin.Context) {}

// ValidateScript godoc
//
// @Summary      Validate a script
// @Description  Lints a strategy script without running a backtest. Used by the Monaco editor.
// @Tags         Herald
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "{ \"script\": \"...\" }"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/script/validate [post]
func (h *Handler) ValidateScript(c *gin.Context) {}

// ── Herald — store ────────────────────────────────────────────────────────────

// ListStoreStrategies godoc
//
// @Summary      List stored strategies
// @Description  Returns all strategy versions in the persistent store.
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}   map[string]interface{}
// @Router       /api/v1/store/strategies [get]
func (h *Handler) ListStoreStrategies(c *gin.Context) {}

// CreateStoreStrategy godoc
//
// @Summary      Create a stored strategy
// @Tags         Herald Store
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "Strategy payload"
// @Success      201   {object}  map[string]interface{}
// @Router       /api/v1/store/strategies [post]
func (h *Handler) CreateStoreStrategy(c *gin.Context) {}

// GetStoreStrategy godoc
//
// @Summary      Get a stored strategy
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Strategy ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/store/strategies/{id} [get]
func (h *Handler) GetStoreStrategy(c *gin.Context) {}

// UpdateStoreStrategy godoc
//
// @Summary      Update a stored strategy
// @Tags         Herald Store
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path      string                  true  "Strategy ID"
// @Param        body  body      map[string]interface{}  true  "Updated strategy"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/store/strategies/{id} [put]
func (h *Handler) UpdateStoreStrategy(c *gin.Context) {}

// DeleteStoreStrategy godoc
//
// @Summary      Delete a stored strategy
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Strategy ID"
// @Success      204
// @Router       /api/v1/store/strategies/{id} [delete]
func (h *Handler) DeleteStoreStrategy(c *gin.Context) {}

// ListStoreCases godoc
//
// @Summary      List backtest cases
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}   map[string]interface{}
// @Router       /api/v1/store/cases [get]
func (h *Handler) ListStoreCases(c *gin.Context) {}

// CreateStoreCase godoc
//
// @Summary      Create a backtest case
// @Tags         Herald Store
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "Case payload"
// @Success      201   {object}  map[string]interface{}
// @Router       /api/v1/store/cases [post]
func (h *Handler) CreateStoreCase(c *gin.Context) {}

// GetStoreCase godoc
//
// @Summary      Get a backtest case
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Case ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/store/cases/{id} [get]
func (h *Handler) GetStoreCase(c *gin.Context) {}

// RunStoreCase godoc
//
// @Summary      Run a backtest case
// @Description  Executes the stored case and saves the result.
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Case ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/store/cases/{id}/run [post]
func (h *Handler) RunStoreCase(c *gin.Context) {}

// GetStoreCaseResults godoc
//
// @Summary      List results for a case
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Case ID"
// @Success      200   {array}   map[string]interface{}
// @Router       /api/v1/store/cases/{id}/results [get]
func (h *Handler) GetStoreCaseResults(c *gin.Context) {}

// GetStoreResult godoc
//
// @Summary      Get a backtest result
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Result ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/store/results/{id} [get]
func (h *Handler) GetStoreResult(c *gin.Context) {}

// DeleteStoreResult godoc
//
// @Summary      Delete a backtest result
// @Tags         Herald Store
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Result ID"
// @Success      204
// @Router       /api/v1/store/results/{id} [delete]
func (h *Handler) DeleteStoreResult(c *gin.Context) {}

// ── Herald — watch (warm-set admin) ──────────────────────────────────────────

// ListWatches godoc
//
// @Summary      List watch configs
// @Description  Returns admin warm-set configs (indicator bootstrapping on startup).
// @Tags         Herald Admin
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}   map[string]interface{}
// @Router       /api/v1/watch [get]
func (h *Handler) ListWatches(c *gin.Context) {}

// CreateWatch godoc
//
// @Summary      Create a watch config
// @Tags         Herald Admin
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "Watch config"
// @Success      201   {object}  map[string]interface{}
// @Router       /api/v1/watch [post]
func (h *Handler) CreateWatch(c *gin.Context) {}

// GetWatch godoc
//
// @Summary      Get a watch config
// @Tags         Herald Admin
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Watch ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/watch/{id} [get]
func (h *Handler) GetWatch(c *gin.Context) {}

// DeleteWatch godoc
//
// @Summary      Delete a watch config
// @Tags         Herald Admin
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Watch ID"
// @Success      204
// @Router       /api/v1/watch/{id} [delete]
func (h *Handler) DeleteWatch(c *gin.Context) {}

// ── Herald — streams (SSE) ────────────────────────────────────────────────────

// StreamSignals godoc
//
// @Summary      SSE signal stream
// @Description  Server-Sent Events stream of live signal batches from all registered hands. Each event carries a `SignalResponse` protobuf payload encoded as JSON.
// @Tags         Herald Stream
// @Security     BearerAuth
// @Produce      text/event-stream
// @Success      200  {string}  string  "event: bar\ndata: {...}"
// @Router       /api/v1/stream/signals [get]
func (h *Handler) StreamSignals(c *gin.Context) {}

// StreamBars godoc
//
// @Summary      SSE bar stream (GET — raw OHLCV)
// @Description  EventSource-compatible SSE stream of raw OHLCV bars for a symbol. Emits a `status` event first, then `bar` events per incoming candle. Use `?tf=M1` to select timeframe.
// @Tags         Herald Stream
// @Security     BearerAuth
// @Produce      text/event-stream
// @Param        symbol  path      string  true  "Symbol, e.g. BTCUSDT"
// @Param        tf      query     string  false "Timeframe (M1, M5, H1, …)"
// @Success      200     {string}  string  "event: bar\ndata: {...}"
// @Router       /api/v1/stream/{symbol} [get]
func (h *Handler) StreamBars(c *gin.Context) {}

// StreamBarsWithIndicators godoc
//
// @Summary      SSE bar stream (POST — with indicators / script)
// @Description  SSE stream carrying bars enriched with indicator values or a script output. Requires a `fetch()` + `ReadableStream` client (not plain EventSource). Body specifies indicator configs or a script.
// @Tags         Herald Stream
// @Security     BearerAuth
// @Accept       json
// @Produce      text/event-stream
// @Param        symbol  path      string         true  "Symbol"
// @Param        body    body      StreamRequest  true  "Stream config"
// @Success      200     {string}  string         "event: bar\ndata: {...}"
// @Router       /api/v1/stream/{symbol} [post]
func (h *Handler) StreamBarsWithIndicators(c *gin.Context) {}

// ── Helm — helms ──────────────────────────────────────────────────────────────

// ListHelms godoc
//
// @Summary      List helms
// @Description  Returns all Helm accounts accessible to the authenticated user.
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}   map[string]interface{}
// @Router       /api/v1/helms [get]
func (h *Handler) ListHelms(c *gin.Context) {}

// GetHelm godoc
//
// @Summary      Get a helm
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID (UUID)"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id} [get]
func (h *Handler) GetHelm(c *gin.Context) {}

// UpdateHelm godoc
//
// @Summary      Update helm config
// @Tags         Helm
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path      string                  true  "Helm ID"
// @Param        body  body      map[string]interface{}  true  "Updated config"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id} [put]
func (h *Handler) UpdateHelm(c *gin.Context) {}

// EnableHelm godoc
//
// @Summary      Enable a helm
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id}/enable [post]
func (h *Handler) EnableHelm(c *gin.Context) {}

// DisableHelm godoc
//
// @Summary      Disable a helm
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id}/disable [post]
func (h *Handler) DisableHelm(c *gin.Context) {}

// PauseHelm godoc
//
// @Summary      Pause a helm (cascade-stops all hands)
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id}/pause [post]
func (h *Handler) PauseHelm(c *gin.Context) {}

// ResumeHelm godoc
//
// @Summary      Resume a paused helm
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id}/resume [post]
func (h *Handler) ResumeHelm(c *gin.Context) {}

// KillHelm godoc
//
// @Summary      Kill a helm (flatten all positions)
// @Description  Stops all hands and flattens their positions at the exchange. Sets helm state to halted.
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id}/kill [post]
func (h *Handler) KillHelm(c *gin.Context) {}

// HaltResetHelm godoc
//
// @Summary      Reset a halted helm
// @Description  Clears the halted state so the helm can be re-enabled.
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id}/halt/reset [post]
func (h *Handler) HaltResetHelm(c *gin.Context) {}

// GetHelmPortfolio godoc
//
// @Summary      Helm portfolio snapshot
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/helms/{id}/portfolio [get]
func (h *Handler) GetHelmPortfolio(c *gin.Context) {}

// GetHelmPositions godoc
//
// @Summary      Helm open positions
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {array}   map[string]interface{}
// @Router       /api/v1/helms/{id}/positions [get]
func (h *Handler) GetHelmPositions(c *gin.Context) {}

// GetHelmTrades godoc
//
// @Summary      Helm trade history
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {array}   map[string]interface{}
// @Router       /api/v1/helms/{id}/trades [get]
func (h *Handler) GetHelmTrades(c *gin.Context) {}

// GetHelmOrders godoc
//
// @Summary      Helm open orders
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Helm ID"
// @Success      200   {array}   map[string]interface{}
// @Router       /api/v1/helms/{id}/orders [get]
func (h *Handler) GetHelmOrders(c *gin.Context) {}

// ── Helm — hands ─────────────────────────────────────────────────────────────

// CreateHand godoc
//
// @Summary      Create a hand (autonomous bot)
// @Description  Creates a new signal-following bot under a helm. The hand starts stopped; call /start to activate.
// @Tags         Helm
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "Hand config"
// @Success      201   {object}  map[string]interface{}
// @Router       /api/v1/hands [post]
func (h *Handler) CreateHand(c *gin.Context) {}

// ListHands godoc
//
// @Summary      List hands
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}   map[string]interface{}
// @Router       /api/v1/hands [get]
func (h *Handler) ListHands(c *gin.Context) {}

// GetHand godoc
//
// @Summary      Get a hand
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID (UUID)"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id} [get]
func (h *Handler) GetHand(c *gin.Context) {}

// UpdateHand godoc
//
// @Summary      Update hand config
// @Tags         Helm
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path      string                  true  "Hand ID"
// @Param        body  body      map[string]interface{}  true  "Updated config"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id} [put]
func (h *Handler) UpdateHand(c *gin.Context) {}

// DeleteHand godoc
//
// @Summary      Delete a hand
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      204
// @Router       /api/v1/hands/{id} [delete]
func (h *Handler) DeleteHand(c *gin.Context) {}

// StartHand godoc
//
// @Summary      Start a hand
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id}/start [post]
func (h *Handler) StartHand(c *gin.Context) {}

// StopHand godoc
//
// @Summary      Stop a hand
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id}/stop [post]
func (h *Handler) StopHand(c *gin.Context) {}

// RestartHand godoc
//
// @Summary      Restart a hand
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id}/restart [post]
func (h *Handler) RestartHand(c *gin.Context) {}

// PauseHand godoc
//
// @Summary      Pause a hand
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id}/pause [post]
func (h *Handler) PauseHand(c *gin.Context) {}

// ResumeHand godoc
//
// @Summary      Resume a paused hand
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id}/resume [post]
func (h *Handler) ResumeHand(c *gin.Context) {}

// KillHand godoc
//
// @Summary      Kill a hand (flatten positions)
// @Description  Stops the hand and flattens its positions at the exchange.
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id}/kill [post]
func (h *Handler) KillHand(c *gin.Context) {}

// ReleaseHand godoc
//
// @Summary      Release a hand (leave positions live)
// @Description  Stops the hand without flattening positions. Emits position_orphaned poslog events.
// @Tags         Helm
// @Security     BearerAuth
// @Produce      json
// @Param        id    path      string  true  "Hand ID"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/hands/{id}/release [post]
func (h *Handler) ReleaseHand(c *gin.Context) {}

// ── Identity ──────────────────────────────────────────────────────────────────

// Login godoc
//
// @Summary      Login (get JWT)
// @Description  Authenticate with email + password. Returns access and refresh tokens.
// @Tags         Identity
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "{ \"email\": \"...\", \"password\": \"...\" }"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/auth/login [post]
func (h *Handler) Login(c *gin.Context) {}

// Register godoc
//
// @Summary      Register a new user
// @Tags         Identity
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "Registration payload"
// @Success      201   {object}  map[string]interface{}
// @Router       /api/v1/auth/register [post]
func (h *Handler) Register(c *gin.Context) {}

// RefreshToken godoc
//
// @Summary      Refresh access token
// @Tags         Identity
// @Accept       json
// @Produce      json
// @Param        body  body      map[string]interface{}  true  "{ \"refresh_token\": \"...\" }"
// @Success      200   {object}  map[string]interface{}
// @Router       /api/v1/auth/refresh [post]
func (h *Handler) RefreshToken(c *gin.Context) {}
