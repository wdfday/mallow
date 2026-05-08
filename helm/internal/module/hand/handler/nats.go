package handler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/natsapi"
	handDto "mallow/helm/internal/module/hand/dto"
)

// NATSHandler is the NATS request/reply transport adapter for hand operations.
type NATSHandler struct {
	handMgr HandService
	helmSvc HelmService
	subs    []*nats.Subscription
}

func NewNATSHandler(handMgr HandService, helmSvc HelmService) *NATSHandler {
	return &NATSHandler{handMgr: handMgr, helmSvc: helmSvc}
}

func (h *NATSHandler) Subscribe(nc *nats.Conn) error {
	routes := map[string]nats.MsgHandler{
		natsapi.SubjHandList:    h.list,
		natsapi.SubjHandGet:     h.get,
		natsapi.SubjHandCreate:  h.create,
		natsapi.SubjHandUpdate:  h.update,
		natsapi.SubjHandDelete:  h.delete,
		natsapi.SubjHandStart:   h.start,
		natsapi.SubjHandStop:    h.stop,
		natsapi.SubjHandRestart: h.restart,
		natsapi.SubjHandPause:   h.pause,
		natsapi.SubjHandResume:  h.resume,
		natsapi.SubjHandKill:    h.kill,
	}
	for subj, fn := range routes {
		sub, err := nc.Subscribe(subj, fn)
		if err != nil {
			return err
		}
		h.subs = append(h.subs, sub)
	}
	slog.Info("nats: hand handlers subscribed", "count", len(routes))
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
		HelmID    string `json:"helm_id"`
		AccountID string `json:"account_id"`
	}
	_ = json.Unmarshal(msg.Data, &req)

	if req.HelmID != "" {
		helmID, err := uuid.Parse(req.HelmID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("invalid helm_id"))
			return
		}
		_ = msg.Respond(natsapi.ReplyOK(h.handMgr.ListByHelm(helmID)))
		return
	}
	if req.AccountID != "" {
		accountID, err := uuid.Parse(req.AccountID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("invalid account_id"))
			return
		}
		helm, err := h.helmSvc.GetByAccount(accountID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("helm not found"))
			return
		}
		_ = msg.Respond(natsapi.ReplyOK(h.handMgr.ListByHelm(helm.ID)))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(h.handMgr.List()))
}

func (h *NATSHandler) get(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	bi, err := h.handMgr.Get(id)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(bi.Summary()))
}

func (h *NATSHandler) create(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	var req handDto.CreateHandReq
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json: " + err.Error()))
		return
	}
	if err := req.Strategy.Validate(); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	if req.AccountID != uuid.Nil && req.HelmID == uuid.Nil {
		helm, err := h.helmSvc.GetByAccount(req.AccountID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("helm not found"))
			return
		}
		req.HelmID = helm.ID
	}
	cfg := req.ToDomain()
	helm, err := h.helmSvc.Get(cfg.HelmID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("helm not found"))
		return
	}
	if err := checkCapitalAllocation(helm, h.handMgr.ListByHelm(cfg.HelmID), cfg.Position, ""); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	instance, err := h.handMgr.Create(cfg)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand created", "id", instance.Data.ID, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(instance.Summary()))
}

func (h *NATSHandler) update(msg *nats.Msg) {
	var raw struct {
		ID string `json:"id"`
		handDto.UpdateHandReq
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
	bi, err := h.handMgr.Get(id)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	patch := raw.UpdateHandReq.ToDomain()
	// Validate capital allocation when sizing changes.
	if raw.Position != nil && (raw.Position.AllocatedCapital > 0 || raw.Position.AllocatedPct > 0) {
		helm, err := h.helmSvc.Get(bi.Data.HelmID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("helm not found"))
			return
		}
		if err := checkCapitalAllocation(helm, h.handMgr.ListByHelm(bi.Data.HelmID), patch.Position, id.String()); err != nil {
			_ = msg.Respond(natsapi.ReplyErr(err.Error()))
			return
		}
	}
	if err := h.handMgr.Update(id, patch); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	bi, _ = h.handMgr.Get(id)
	_ = msg.Respond(natsapi.ReplyOK(bi.Summary()))
}

func (h *NATSHandler) delete(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if err := h.handMgr.Delete(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand deleted", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "deleted", ID: id.String()}))
}

func (h *NATSHandler) start(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if err := h.handMgr.Start(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand started", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "started", ID: id.String()}))
}

func (h *NATSHandler) stop(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if err := h.handMgr.Stop(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand stopped", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "stopped", ID: id.String()}))
}

func (h *NATSHandler) pause(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if err := h.handMgr.Pause(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "paused", ID: id.String()}))
}

func (h *NATSHandler) resume(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if err := h.handMgr.Resume(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "running", ID: id.String()}))
}

func (h *NATSHandler) kill(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if err := h.handMgr.Kill(context.Background(), id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Warn("nats: hand killed", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "stopped", ID: id.String()}))
}

func (h *NATSHandler) restart(msg *nats.Msg) {
	caller := natsapi.ParseCaller(msg.Data)
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if err := h.handMgr.Restart(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand restarted", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "running", ID: id.String()}))
}

func parseUUID(data []byte) (uuid.UUID, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(req.ID)
}
