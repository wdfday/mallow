package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/helm/domain"
	helmDto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/module/helm/service"
	"mallow/helm/internal/runtime"
)

// NATSHandler is the NATS request/reply transport adapter for orchestrator operations.
type NATSHandler struct {
	svc     *service.Service
	handMgr HandManager
	reg     *runtime.Registry
	nc      *nats.Conn
	subs    []*nats.Subscription
}

func NewNATSHandler(svc *service.Service, handMgr HandManager, reg *runtime.Registry) *NATSHandler {
	return &NATSHandler{svc: svc, handMgr: handMgr, reg: reg}
}

func (h *NATSHandler) Subscribe(nc *nats.Conn) error {
	h.nc = nc
	routes := map[string]nats.MsgHandler{
		// User-scoped request/reply (via strategist/gateway)
		natsapi.SubjOrchList:      h.list,
		natsapi.SubjOrchGet:       h.get,
		natsapi.SubjOrchUpdate:    h.update,
		natsapi.SubjOrchEnable:    h.enable,
		natsapi.SubjOrchDisable:   h.disable,
		natsapi.SubjOrchPause:     h.pause,
		natsapi.SubjOrchResume:    h.resume,
		natsapi.SubjOrchKill:      h.kill,
		natsapi.SubjOrchResetHalt: h.resetHalt,
		natsapi.SubjOrchPortfolio: h.portfolio,
		natsapi.SubjOrchPositions: h.positions,
		natsapi.SubjOrchTrades:    h.trades,
		natsapi.SubjOrchOrders:    h.orders,
		// System events (fire-and-forget from identity/investment service)
		natsapi.SubjAccountLinked:   h.accountLinked,
		natsapi.SubjAccountUnlinked: h.accountUnlinked,
	}
	for subj, fn := range routes {
		sub, err := nc.Subscribe(subj, fn)
		if err != nil {
			return err
		}
		h.subs = append(h.subs, sub)
	}
	slog.Info("nats: orchestrator handlers subscribed", "count", len(routes))
	return nil
}

func (h *NATSHandler) Drain() {
	for _, sub := range h.subs {
		_ = sub.Drain()
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────

func (h *NATSHandler) list(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	cfgs, err := h.svc.ListByUser(userID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(helmDto.HelmsToResp(cfgs)))
}

func (h *NATSHandler) get(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	cfg, err := h.svc.Get(id)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}

	hands := h.handMgr.ListByHelm(id)
	rt, rtErr := h.reg.Get(id)
	paused := false
	var lastSyncAt *time.Time
	if rtErr == nil {
		paused = rt.IsPaused()
		if t := rt.LastSyncAt(); !t.IsZero() {
			lastSyncAt = &t
		}
	}

	_ = msg.Respond(natsapi.ReplyOK(helmDto.HelmDetailResp{
		HelmResp:   helmDto.HelmToResp(cfg),
		Hands:      hands,
		Running:    rtErr == nil,
		Paused:     paused,
		LastSyncAt: lastSyncAt,
	}))
}

func (h *NATSHandler) enable(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	if err := h.svc.Enable(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: orchestrator enabled", "id", id, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(helmDto.ActionResp{Status: "enabled", ID: id}))
}

func (h *NATSHandler) disable(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	if err := h.svc.Disable(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: orchestrator disabled", "id", id, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(helmDto.ActionResp{Status: "disabled", ID: id}))
}

// accountLinked handles the helm.accounts.linked fire-and-forget event from the identity/investment
// service. It auto-creates a disabled orchestrator for the newly linked broker account.
func (h *NATSHandler) accountLinked(msg *nats.Msg) {
	var ev natsapi.AccountLinkedEvent
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		slog.Warn("nats: helm.accounts.linked: invalid json", "err", err)
		return
	}
	accountID, err := uuid.Parse(ev.AccountID)
	if err != nil {
		slog.Warn("nats: helm.accounts.linked: invalid account_id", "err", err)
		return
	}
	userID, err := uuid.Parse(ev.UserID)
	if err != nil {
		slog.Warn("nats: helm.accounts.linked: invalid user_id", "err", err)
		return
	}

	// Fetch broker credentials from investment service — not included in the event.
	creds, err := natsapi.FetchCredentials(h.nc, ev.AccountID)
	if err != nil {
		slog.Error("nats: helm.accounts.linked: fetch credentials failed", "account_id", ev.AccountID, "err", err)
		return
	}

	cfg, err := h.svc.CreateForAccount(service.CreateForAccountReq{
		UserID:    userID,
		AccountID: accountID,
		Name:      ev.Name,
		Capital:   ev.Capital,
		Exchange: domain.ExchangeConfig{
			BrokerType: ev.BrokerType,
			AccountID:  ev.AccountRef,
			BaseURL:    ev.BaseURL,
			Demo:       ev.Demo,
			Testnet:    ev.Testnet,
		},
		Creds: creds,
	})
	if err != nil {
		slog.Error("nats: helm.accounts.linked: create orchestrator failed", "account_id", ev.AccountID, "err", err)
		return
	}
	slog.Info("nats: orchestrator auto-created (disabled)", "id", cfg.ID, "account_id", ev.AccountID)
}

// accountUnlinked handles the helm.accounts.unlinked event and auto-deletes the orchestrator.
func (h *NATSHandler) accountUnlinked(msg *nats.Msg) {
	var ev natsapi.AccountUnlinkedEvent
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		slog.Warn("nats: helm.accounts.unlinked: invalid json", "err", err)
		return
	}
	accountID, err := uuid.Parse(ev.AccountID)
	if err != nil {
		slog.Warn("nats: helm.accounts.unlinked: invalid account_id", "err", err)
		return
	}
	if err := h.svc.DeleteForAccount(accountID); err != nil {
		slog.Error("nats: helm.accounts.unlinked: delete orchestrator failed", "account_id", ev.AccountID, "err", err)
		return
	}
	slog.Info("nats: orchestrator auto-deleted", "account_id", ev.AccountID)
}

func (h *NATSHandler) update(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	var raw struct {
		ID        string                      `json:"id"`
		Name      string                      `json:"name"`
		Capital   float64                     `json:"capital"`
		Portfolio *helmDto.PortfolioConfigDTO `json:"portfolio"`
		Risk      *helmDto.RiskConfigDTO      `json:"risk"`
	}
	if err := json.Unmarshal(msg.Data, &raw); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json: " + err.Error()))
		return
	}
	id, err := uuid.Parse(raw.ID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid id"))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}

	updateReq := service.UpdateReq{Name: raw.Name, Capital: decimal.NewFromFloat(raw.Capital)}
	if raw.Portfolio != nil {
		p := raw.Portfolio.ToDomain()
		updateReq.Portfolio = &p
	}
	if raw.Risk != nil {
		r := raw.Risk.ToDomain()
		updateReq.Risk = &r
	}

	updated, err := h.svc.Update(id, updateReq)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(helmDto.HelmToResp(updated)))
}

func (h *NATSHandler) pause(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	if err := h.svc.Pause(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: orchestrator paused", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(helmDto.ActionResp{Status: "paused", ID: id}))
}

func (h *NATSHandler) resume(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	if err := h.svc.Resume(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: orchestrator resumed", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(helmDto.ActionResp{Status: "resumed", ID: id}))
}

func (h *NATSHandler) kill(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	if err := h.svc.Kill(context.Background(), id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Warn("nats: orchestrator killed", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(helmDto.ActionResp{Status: "halted", ID: id}))
}

func (h *NATSHandler) resetHalt(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return
	}
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	if err := h.svc.ResetHalt(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: orchestrator halt reset", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(helmDto.ActionResp{Status: "active", ID: id}))
}

func (h *NATSHandler) requireRuntime(data []byte) (*runtime.HelmRuntime, error) {
	id, err := natsapi.ParseID(data)
	if err != nil {
		return nil, err
	}
	return h.reg.Get(id)
}

func (h *NATSHandler) portfolio(msg *nats.Msg) {
	rt, err := h.requireRuntime(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(helmDto.PortfolioToResp(rt.Portfolio.Summary())))
}

func (h *NATSHandler) positions(msg *nats.Msg) {
	rt, err := h.requireRuntime(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(helmDto.PositionsToResp(rt.Portfolio.Positions())))
}

func (h *NATSHandler) trades(msg *nats.Msg) {
	rt, err := h.requireRuntime(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(helmDto.TradesToResp(rt.Portfolio.Trades())))
}

func (h *NATSHandler) orders(msg *nats.Msg) {
	id, err := natsapi.ParseID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if _, rtErr := h.reg.Get(id); rtErr != nil {
		_ = msg.Respond(natsapi.ReplyErr("orchestrator runtime not active"))
		return
	}
	var allOrders []helmDto.OrderResp
	for _, bs := range h.handMgr.ListByHelm(id) {
		bi, getErr := h.handMgr.Get(bs.ID)
		if getErr == nil {
			for _, o := range bi.Runner.Orders() {
				allOrders = append(allOrders, helmDto.OrderToResp(o))
			}
		}
	}
	_ = msg.Respond(natsapi.ReplyOK(allOrders))
}
