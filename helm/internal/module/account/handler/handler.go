package handler

import (
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/poslog"
	accountdto "mallow/helm/internal/module/account/dto"
	accountservice "mallow/helm/internal/module/account/service"
	helmdto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/perf"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

// Handler manages account endpoints.
type Handler struct {
	service     accountservice.Service
	helmSvc     HelmLookup
	handMgr     HandLister
	reg         *runtime.Registry
	nc          *nats.Conn
	fillLog     *perflog.FillLog
	snapshotLog perf.SnapshotLog
	posLog      poslog.Log
	logger      *slog.Logger
}

// NewHandler constructs an account handler.
func NewHandler(
	service accountservice.Service,
	helmSvc HelmLookup,
	handMgr HandLister,
	reg *runtime.Registry,
	nc *nats.Conn,
	fillLog *perflog.FillLog,
	snapshotLog perf.SnapshotLog,
	posLog poslog.Log,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		service:     service,
		helmSvc:     helmSvc,
		handMgr:     handMgr,
		reg:         reg,
		nc:          nc,
		fillLog:     fillLog,
		snapshotLog: snapshotLog,
		posLog:      posLog,
		logger:      logger.With("component", "account.handler"),
	}
}

// RegisterRoutes wires account routes under /api/v1/accounts.
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	accounts := r.Group("/api/v1/accounts")
	accounts.Use(pkgmw.TrustedHeaders())
	{
		accounts.GET("", h.getMyAccounts)
		accounts.GET("/:id", h.getAccount)
		accounts.GET("/:id/portfolio", h.portfolio)
		accounts.GET("/:id/positions", h.positions)
		accounts.GET("/:id/trades", h.trades)
		accounts.GET("/:id/fills", h.fills)
		accounts.GET("/:id/snapshots", h.snapshots)
		accounts.GET("/:id/equity", h.equity)
		accounts.GET("/:id/events", h.events)
		accounts.GET("/:id/stream/fills", h.streamFills)
		accounts.GET("/:id/stream/portfolio", h.streamPortfolio)
		accounts.GET("/:id/exchange/account", h.exchangeAccount)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func parsePage(c *gin.Context) (page, limit int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", "100"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return
}

// requireRuntime resolves the helm runtime for the given account_id, verifying ownership.
func (h *Handler) requireRuntime(c *gin.Context) (*runtime.HelmRuntime, bool) {
	accountID, helmID, ok := h.resolveAccountHelm(c)
	if !ok {
		return nil, false
	}
	_ = accountID
	rt, rErr := h.reg.Get(helmID)
	if rErr != nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "helm runtime not ready")
		return nil, false
	}
	return rt, true
}

// ── CRUD ───────────────────────────────────────────────────────────────────

// getMyAccounts godoc
// @Summary List my accounts
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param account_type query string false "Filter by account type"
// @Param is_active query bool false "Filter by active status"
// @Success 200 {object} shared.SuccessResponse[accountdto.AccountsListResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/accounts [get]
func (h *Handler) getMyAccounts(c *gin.Context) {
	userID := pkgmw.UserID(c)
	if userID == "" {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	var req accountdto.ListAccountsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid query parameters")
		return
	}

	accounts, total, err := h.service.GetByUserID(c.Request.Context(), userID, req)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	items := make([]accountdto.AccountResponse, len(accounts))
	for i, acc := range accounts {
		items[i] = accountdto.ToResponse(acc)
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Accounts retrieved successfully", accountdto.AccountsListResponse{
		Items: items,
		Total: total,
	})
}

// getAccount godoc
// @Summary Get account by ID
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Success 200 {object} shared.SuccessResponse[accountdto.AccountResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id} [get]
func (h *Handler) getAccount(c *gin.Context) {
	userID := pkgmw.UserID(c)
	if userID == "" {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	id := c.Param("id")
	if id == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "account id is required")
		return
	}

	account, err := h.service.GetByID(c.Request.Context(), id, userID)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Account retrieved successfully", accountdto.ToResponse(*account))
}

// ── Live data (from runtime) ───────────────────────────────────────────────

// portfolio godoc
// @Summary Get live portfolio for an account
// @Description Returns real-time portfolio summary from the helm runtime (cash, equity, positions, drawdown, win rate).
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Success 200 {object} shared.SuccessResponse[helmdto.PortfolioResp]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/portfolio [get]
func (h *Handler) portfolio(c *gin.Context) {
	rt, ok := h.requireRuntime(c)
	if !ok {
		return
	}
	hands := h.handMgr.ListByHelm(rt.HelmID)
	shared.RespondWithSuccess(c, http.StatusOK, "Portfolio retrieved successfully",
		helmdto.PortfolioToResp(rt.PortfolioSummary(), helmdto.SumAllocatedCapital(hands), helmdto.SumAvailableCash(hands)))
}

// positions godoc
// @Summary List open positions for an account
// @Description Returns live open positions from the helm runtime.
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Success 200 {object} shared.SuccessResponse[[]helmdto.PositionResp]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/positions [get]
func (h *Handler) positions(c *gin.Context) {
	rt, ok := h.requireRuntime(c)
	if !ok {
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Positions retrieved successfully", helmdto.PositionsToResp(rt.Portfolio.Positions()))
}

// ── Historical data (from JetStream logs) ─────────────────────────────────

// trades godoc
// @Summary List closed trades for an account
// @Description Cursor-based pagination over HELM_TRADES JetStream stream (fan-out across all hands).
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Param cursor query int false "JetStream sequence cursor (0 = from start)"
// @Param limit query int false "Page size" default(100)
// @Success 200 {object} shared.SuccessResponse[helmdto.TradesPageResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/trades [get]
func (h *Handler) trades(c *gin.Context) {
	_, helmID, ok := h.resolveAccountHelm(c)
	if !ok {
		return
	}
	if h.posLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "trade log unavailable")
		return
	}
	_, limit := parsePage(c)
	cursorStr := c.DefaultQuery("cursor", "0")
	cursor, _ := strconv.ParseUint(cursorStr, 10, 64)

	hands := h.handMgr.ListByHelm(helmID)
	var all []poslog.TradeRecord
	for _, hs := range hands {
		page, qErr := h.posLog.TradesPaged(c.Request.Context(), helmID.String(), hs.ID.String(), cursor, limit)
		if qErr == nil {
			all = append(all, page.Trades...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ExitAt.Before(all[j].ExitAt) })
	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}
	resp := helmdto.TradesPageResp{
		Trades:  make([]helmdto.TradeResp, 0, len(all)),
		HasMore: hasMore,
		Limit:   limit,
	}
	if hasMore && len(all) > 0 {
		resp.Next = strconv.FormatUint(all[len(all)-1].Cursor+1, 10)
	}
	for _, t := range all {
		resp.Trades = append(resp.Trades, helmdto.TradeRecordToResp(t))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Trades retrieved successfully", resp)
}

// fills godoc
// @Summary List order fills for an account
// @Description Time-cursor pagination over TRADE_FILLS JetStream stream. Pass `after` (RFC3339) from previous response to page forward.
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Param after query string false "RFC3339 cursor (exclusive); omit for all"
// @Param limit query int false "Page size" default(200)
// @Success 200 {object} shared.SuccessResponse[helmdto.FillPageResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/fills [get]
func (h *Handler) fills(c *gin.Context) {
	accountID, ok := h.resolveAccount(c)
	if !ok {
		return
	}
	if h.fillLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "fill log unavailable")
		return
	}
	_, limit := parsePage(c)
	page := perf.Page{Limit: limit}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, tErr := time.Parse(time.RFC3339, afterStr); tErr == nil {
			page.After = t
		}
	}
	result, err := h.fillLog.Query(c.Request.Context(), accountID.String(), page)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := helmdto.FillPageResp{
		Fills:   make([]helmdto.FillResp, 0, len(result.Fills)),
		HasMore: result.HasMore,
		Limit:   limit,
	}
	if result.HasMore {
		resp.Next = result.Next.UTC().Format(time.RFC3339)
	}
	for _, f := range result.Fills {
		resp.Fills = append(resp.Fills, helmdto.FillToResp(f))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Fills retrieved successfully", resp)
}

// snapshots godoc
// @Summary List portfolio snapshots for an account
// @Description Time-cursor pagination over PORTFOLIO_SNAPSHOTS JetStream stream.
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Param after query string false "RFC3339 cursor (exclusive); omit for all"
// @Param limit query int false "Page size" default(100)
// @Success 200 {object} shared.SuccessResponse[helmdto.SnapshotPageResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/snapshots [get]
func (h *Handler) snapshots(c *gin.Context) {
	_, helmID, ok := h.resolveAccountHelm(c)
	if !ok {
		return
	}
	if h.snapshotLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "portfolio log unavailable")
		return
	}
	_, limit := parsePage(c)
	page := perf.Page{Limit: limit}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, tErr := time.Parse(time.RFC3339, afterStr); tErr == nil {
			page.After = t
		}
	}
	result, err := h.snapshotLog.Query(c.Request.Context(), helmID.String(), "", page)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := helmdto.SnapshotPageResp{
		Snapshots: make([]helmdto.SnapshotResp, 0, len(result.Snapshots)),
		HasMore:   result.HasMore,
		Limit:     limit,
	}
	if result.HasMore {
		resp.Next = result.Next.UTC().Format(time.RFC3339)
	}
	for _, s := range result.Snapshots {
		resp.Snapshots = append(resp.Snapshots, helmdto.SnapshotToResp(s))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Snapshots retrieved successfully", resp)
}

// equity godoc
// @Summary Equity curve for an account
// @Description Time-cursor pagination over HELM_EQUITY JetStream stream, fan-out across all hands under the account's helm.
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Param after query string false "RFC3339 cursor (exclusive); omit for all"
// @Param limit query int false "Page size" default(200)
// @Success 200 {object} shared.SuccessResponse[helmdto.EquityPageResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/equity [get]
func (h *Handler) equity(c *gin.Context) {
	_, helmID, ok := h.resolveAccountHelm(c)
	if !ok {
		return
	}
	if h.snapshotLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "equity log unavailable")
		return
	}
	_, limit := parsePage(c)
	page := perf.Page{Limit: limit}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, tErr := time.Parse(time.RFC3339, afterStr); tErr == nil {
			page.After = t
		}
	}
	hands := h.handMgr.ListByHelm(helmID)
	var all []perf.Snapshot
	for _, hs := range hands {
		result, qErr := h.snapshotLog.Query(c.Request.Context(), helmID.String(), hs.ID.String(), page)
		if qErr == nil {
			all = append(all, result.Snapshots...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].TS.Before(all[j].TS) })
	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}
	resp := helmdto.EquityPageResp{
		Points:  make([]helmdto.EquityPointResp, 0, len(all)),
		HasMore: hasMore,
		Limit:   limit,
	}
	if hasMore && len(all) > 0 {
		resp.Next = all[len(all)-1].TS.UTC().Format(time.RFC3339)
	}
	for _, p := range all {
		resp.Points = append(resp.Points, helmdto.EquityPointResp{
			HandID:        p.HandID,
			TS:            p.TS,
			Equity:        p.Equity.InexactFloat64(),
			Cash:          p.Cash.InexactFloat64(),
			RealizedPnL:   p.RealizedPnL.InexactFloat64(),
			UnrealizedPnL: p.UnrealizedPnL.InexactFloat64(),
		})
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Equity retrieved successfully", resp)
}

// exchangeAccount godoc
// @Summary Get live exchange account snapshot for an account
// @Description Calls the exchange REST API directly and returns cash, equity, positions and per-asset balances.
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Success 200 {object} shared.SuccessResponse[helmdto.ExchangeAccountResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/exchange/account [get]
func (h *Handler) exchangeAccount(c *gin.Context) {
	rt, ok := h.requireRuntime(c)
	if !ok {
		return
	}
	syncer, ok := rt.Exchange.(exchange.AccountSyncer)
	if !ok {
		shared.RespondWithError(c, http.StatusBadRequest, "exchange does not support account sync")
		return
	}
	snap, err := syncer.SyncAccount(c.Request.Context(), rt.Creds, nil)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadGateway, err.Error())
		return
	}
	positions := make([]helmdto.ExchangePositionResp, len(snap.Positions))
	for i, p := range snap.Positions {
		positions[i] = helmdto.ExchangePositionResp{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
	}
	balances := make([]helmdto.AssetBalanceResp, len(snap.Balances))
	for i, b := range snap.Balances {
		balances[i] = helmdto.AssetBalanceResp{Asset: b.Asset, Free: b.Free}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Account retrieved successfully", helmdto.ExchangeAccountResp{
		Cash:      snap.Cash,
		Equity:    snap.Equity,
		Positions: positions,
		Balances:  balances,
	})
}
