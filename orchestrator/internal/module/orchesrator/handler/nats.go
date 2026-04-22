package handler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/natsapi"
	botservice "orchestrator/internal/module/bot/service"
	"orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/module/orchesrator/service"
	"orchestrator/internal/runtime"
)

// NATSHandler is the NATS request/reply transport adapter for orchestrator operations.
type NATSHandler struct {
	svc    *service.Service
	botMgr *botservice.Service
	reg    *runtime.Registry
	subs   []*nats.Subscription
}

func NewNATSHandler(svc *service.Service, botMgr *botservice.Service, reg *runtime.Registry) *NATSHandler {
	return &NATSHandler{svc: svc, botMgr: botMgr, reg: reg}
}

func (h *NATSHandler) Subscribe(nc *nats.Conn) error {
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
	_ = msg.Respond(natsapi.ReplyOK(orchListToResp(cfgs)))
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

	bots := h.botMgr.ListByOrchestrator(id)
	rt, rtErr := h.reg.Get(id)
	paused := false
	if rtErr == nil {
		paused = rt.IsPaused()
	}

	_ = msg.Respond(natsapi.ReplyOK(OrchestratorDetailResp{
		OrchestratorResp: orchToResp(cfg),
		Bots:             bots,
		Running:          rtErr == nil,
		Paused:           paused,
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
	_ = msg.Respond(natsapi.ReplyOK(ActionResp{Status: "enabled", ID: id}))
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
	_ = msg.Respond(natsapi.ReplyOK(ActionResp{Status: "disabled", ID: id}))
}

// accountLinked handles the orchestrator.accounts.linked fire-and-forget event from the identity/investment
// service. It auto-creates a disabled orchestrator for the newly linked broker account.
func (h *NATSHandler) accountLinked(msg *nats.Msg) {
	var ev natsapi.AccountLinkedEvent
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		slog.Warn("nats: orchestrator.accounts.linked: invalid json", "err", err)
		return
	}
	accountID, err := uuid.Parse(ev.AccountID)
	if err != nil {
		slog.Warn("nats: orchestrator.accounts.linked: invalid account_id", "err", err)
		return
	}
	userID, err := uuid.Parse(ev.UserID)
	if err != nil {
		slog.Warn("nats: orchestrator.accounts.linked: invalid user_id", "err", err)
		return
	}
	cfg, err := h.svc.CreateForAccount(service.CreateForAccountReq{
		UserID:    userID,
		AccountID: accountID,
		Name:      ev.Name,
		Capital:   ev.Capital,
		Exchange: domain.ExchangeConfig{
			BrokerType: ev.BrokerType,
			APIKey:     ev.APIKey,
			APISecret:  ev.APISecret,
			Passphrase: ev.Passphrase,
			AccountID:  ev.AccountRef,
			BaseURL:    ev.BaseURL,
			Demo:       ev.Demo,
			Testnet:    ev.Testnet,
		},
	})
	if err != nil {
		slog.Error("nats: orchestrator.accounts.linked: create orchestrator failed", "account_id", ev.AccountID, "err", err)
		return
	}
	slog.Info("nats: orchestrator auto-created (disabled)", "id", cfg.ID, "account_id", ev.AccountID)
}

// accountUnlinked handles the orchestrator.accounts.unlinked event and auto-deletes the orchestrator.
func (h *NATSHandler) accountUnlinked(msg *nats.Msg) {
	var ev natsapi.AccountUnlinkedEvent
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		slog.Warn("nats: orchestrator.accounts.unlinked: invalid json", "err", err)
		return
	}
	accountID, err := uuid.Parse(ev.AccountID)
	if err != nil {
		slog.Warn("nats: orchestrator.accounts.unlinked: invalid account_id", "err", err)
		return
	}
	if err := h.svc.DeleteForAccount(accountID); err != nil {
		slog.Error("nats: orchestrator.accounts.unlinked: delete orchestrator failed", "account_id", ev.AccountID, "err", err)
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
		ID        string              `json:"id"`
		Name      string              `json:"name"`
		Capital   float64             `json:"capital"`
		Portfolio *PortfolioConfigDTO `json:"portfolio"`
		Risk      *RiskConfigDTO      `json:"risk"`
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
	_ = msg.Respond(natsapi.ReplyOK(orchToResp(updated)))
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
	_ = msg.Respond(natsapi.ReplyOK(ActionResp{Status: "paused", ID: id}))
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
	_ = msg.Respond(natsapi.ReplyOK(ActionResp{Status: "resumed", ID: id}))
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
	_ = msg.Respond(natsapi.ReplyOK(ActionResp{Status: "halted", ID: id}))
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
	_ = msg.Respond(natsapi.ReplyOK(ActionResp{Status: "active", ID: id}))
}

func (h *NATSHandler) requireRuntime(data []byte) (*runtime.Orchestrator, error) {
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
	_ = msg.Respond(natsapi.ReplyOK(PortfolioToResp(rt.Portfolio.Summary())))
}

func (h *NATSHandler) positions(msg *nats.Msg) {
	rt, err := h.requireRuntime(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(PositionsToResp(rt.Portfolio.Positions())))
}

func (h *NATSHandler) trades(msg *nats.Msg) {
	rt, err := h.requireRuntime(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(TradesToResp(rt.Portfolio.Trades())))
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
	var allOrders []OrderResp
	for _, bs := range h.botMgr.ListByOrchestrator(id) {
		bi, getErr := h.botMgr.Get(bs.ID)
		if getErr == nil {
			for _, o := range bi.Bot.Orders() {
				allOrders = append(allOrders, OrderToResp(o))
			}
		}
	}
	_ = msg.Respond(natsapi.ReplyOK(allOrders))
}

// orchRespFromDomain is a local alias for internal reuse.
func orchRespFromDomain(cfg *domain.OrchestratorConfig) OrchestratorResp {
	return orchToResp(cfg)
}
