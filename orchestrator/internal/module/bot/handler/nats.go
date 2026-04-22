package handler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"orchestrator/internal/infra/natsapi"
	"orchestrator/internal/module/bot/service"
	orchsvc "orchestrator/internal/module/orchesrator/service"
)

// NATSHandler is the NATS request/reply transport adapter for bot operations.
type NATSHandler struct {
	botMgr  *service.Service
	orchSvc *orchsvc.Service
	subs    []*nats.Subscription
}

func NewNATSHandler(botMgr *service.Service, orchSvc *orchsvc.Service) *NATSHandler {
	return &NATSHandler{botMgr: botMgr, orchSvc: orchSvc}
}

func (h *NATSHandler) Subscribe(nc *nats.Conn) error {
	routes := map[string]nats.MsgHandler{
		natsapi.SubjBotList:    h.list,
		natsapi.SubjBotGet:     h.get,
		natsapi.SubjBotCreate:  h.create,
		natsapi.SubjBotUpdate:  h.update,
		natsapi.SubjBotStart:   h.start,
		natsapi.SubjBotStop:    h.stop,
		natsapi.SubjBotRestart: h.restart,
		natsapi.SubjBotPause:   h.pause,
		natsapi.SubjBotResume:  h.resume,
		natsapi.SubjBotKill:    h.kill,
	}
	for subj, fn := range routes {
		sub, err := nc.Subscribe(subj, fn)
		if err != nil {
			return err
		}
		h.subs = append(h.subs, sub)
	}
	slog.Info("nats: bot handlers subscribed", "count", len(routes))
	return nil
}

func (h *NATSHandler) Drain() {
	for _, sub := range h.subs {
		_ = sub.Drain()
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────

func (h *NATSHandler) list(msg *nats.Msg) {
	var req struct {
		OrchestratorID string `json:"orchestrator_id"`
		AccountID      string `json:"account_id"`
	}
	_ = json.Unmarshal(msg.Data, &req)

	if req.OrchestratorID != "" {
		orchID, err := uuid.Parse(req.OrchestratorID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("invalid orchestrator_id"))
			return
		}
		_ = msg.Respond(natsapi.ReplyOK(h.botMgr.ListByOrchestrator(orchID)))
		return
	}
	if req.AccountID != "" {
		accountID, err := uuid.Parse(req.AccountID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("invalid account_id"))
			return
		}
		orch, err := h.orchSvc.GetByAccount(accountID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("orchestrator not found"))
			return
		}
		_ = msg.Respond(natsapi.ReplyOK(h.botMgr.ListByOrchestrator(orch.ID)))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(h.botMgr.List()))
}

func (h *NATSHandler) get(msg *nats.Msg) {
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	bi, err := h.botMgr.Get(id)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(bi.Summary()))
}

func (h *NATSHandler) create(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	var req CreateBotReq
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json: " + err.Error()))
		return
	}
	if req.AccountID != uuid.Nil && req.OrchestratorID == uuid.Nil {
		orch, err := h.orchSvc.GetByAccount(req.AccountID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("orchestrator not found"))
			return
		}
		req.OrchestratorID = orch.ID
	}
	cfg := req.ToDomain()
	orch, err := h.orchSvc.Get(cfg.OrchestratorID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("orchestrator not found"))
		return
	}
	if err := checkCapitalAllocation(orch, h.botMgr.ListByOrchestrator(cfg.OrchestratorID), cfg.Position, ""); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	instance, err := h.botMgr.Create(cfg)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: bot created", "id", instance.Data.ID, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(instance.Summary()))
}

func (h *NATSHandler) update(msg *nats.Msg) {
	var raw struct {
		ID string `json:"id"`
		UpdateBotReq
	}
	if err := json.Unmarshal(msg.Data, &raw); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json: " + err.Error()))
		return
	}
	bi, err := h.botMgr.Get(raw.ID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	patch := raw.UpdateBotReq.ToDomain()
	// Validate capital allocation when sizing changes.
	if raw.Position != nil && (raw.Position.AllocatedCapital > 0 || raw.Position.AllocatedPct > 0) {
		orch, err := h.orchSvc.Get(bi.Data.OrchestratorID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("orchestrator not found"))
			return
		}
		if err := checkCapitalAllocation(orch, h.botMgr.ListByOrchestrator(bi.Data.OrchestratorID), patch.Position, raw.ID); err != nil {
			_ = msg.Respond(natsapi.ReplyErr(err.Error()))
			return
		}
	}
	if err := h.botMgr.Update(raw.ID, patch); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	bi, _ = h.botMgr.Get(raw.ID)
	_ = msg.Respond(natsapi.ReplyOK(bi.Summary()))
}

func (h *NATSHandler) delete(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	if err := h.botMgr.Delete(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: bot deleted", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(BotActionResp{Status: "deleted", ID: id}))
}

func (h *NATSHandler) start(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	if err := h.botMgr.Start(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: bot started", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(BotActionResp{Status: "started", ID: id}))
}

func (h *NATSHandler) stop(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	if err := h.botMgr.Stop(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: bot stopped", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(BotActionResp{Status: "stopped", ID: id}))
}

func (h *NATSHandler) pause(msg *nats.Msg) {
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	if err := h.botMgr.Pause(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(BotActionResp{Status: "paused", ID: id}))
}

func (h *NATSHandler) resume(msg *nats.Msg) {
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	if err := h.botMgr.Resume(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(BotActionResp{Status: "running", ID: id}))
}

func (h *NATSHandler) kill(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	if err := h.botMgr.Kill(context.Background(), id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Warn("nats: bot killed", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(BotActionResp{Status: "stopped", ID: id}))
}

func (h *NATSHandler) restart(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseStringID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return
	}
	if err := h.botMgr.Restart(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: bot restarted", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(BotActionResp{Status: "running", ID: id}))
}

func parseStringID(data []byte) (string, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return "", err
	}
	return req.ID, nil
}
