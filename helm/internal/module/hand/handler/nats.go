package handler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/natsapi"
	handDto "mallow/helm/internal/module/hand/dto"
	handservice "mallow/helm/internal/module/hand/service"
)

// NATSHandler is the NATS request/reply transport adapter for hand operations.
type NATSHandler struct {
	handMgr HandService
	helmSvc HelmService
	reg     RuntimeRegistry
	subs    []*nats.Subscription
}

func NewNATSHandler(handMgr HandService, helmSvc HelmService, reg RuntimeRegistry) *NATSHandler {
	return &NATSHandler{handMgr: handMgr, helmSvc: helmSvc, reg: reg}
}

func (h *NATSHandler) Subscribe(nc *nats.Conn) error {
	routes := map[string]nats.MsgHandler{
		natsapi.SubjHandList:   h.list,
		natsapi.SubjHandGet:    h.get,
		natsapi.SubjHandCreate: h.create,
		natsapi.SubjHandUpdate: h.update,
		natsapi.SubjHandStart:  h.start,
		natsapi.SubjHandStop:   h.stop,
		natsapi.SubjHandPause:  h.natsPause,
		natsapi.SubjHandResume: h.natsResume,
		natsapi.SubjHandKill:   h.kill,
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

// ── ownership helpers ─────────────────────────────────────────────────────────

// requireCaller parses CallerMeta from the message and returns the validated
// caller user ID. Responds with an error and returns false if absent or invalid.
func (h *NATSHandler) requireCaller(msg *nats.Msg) (uuid.UUID, bool) {
	caller := natsapi.ParseCaller(msg.Data)
	userID, err := uuid.Parse(caller.CallerUserID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("missing caller_user_id"))
		return uuid.Nil, false
	}
	return userID, true
}

// enforceHelmOwner parses CallerMeta + helmID from the message and verifies that
// the caller owns the specified helm. Used by create (checks helm before hand exists).
func (h *NATSHandler) enforceHelmOwner(msg *nats.Msg, helmID uuid.UUID) (uuid.UUID, bool) {
	userID, ok := h.requireCaller(msg)
	if !ok {
		return uuid.Nil, false
	}
	if err := h.helmSvc.CheckOwner(helmID, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return uuid.Nil, false
	}
	return userID, true
}

// enforceHandOwner resolves the hand's parent helm and verifies the caller owns it.
// Used by update/delete/start/stop/kill where the hand already exists.
func (h *NATSHandler) enforceHandOwner(msg *nats.Msg, handID uuid.UUID) (uuid.UUID, bool) {
	userID, ok := h.requireCaller(msg)
	if !ok {
		return uuid.Nil, false
	}
	summary, err := h.handMgr.GetSummary(handID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return uuid.Nil, false
	}
	if err := h.helmSvc.CheckOwner(summary.HelmID, userID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return uuid.Nil, false
	}
	return userID, true
}

// ── Handlers ──────────────────────────────────────────────────────────────────

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
	var req handDto.CreateHandReq
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json: " + err.Error()))
		return
	}
	if err := req.Strategy.Validate(); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	// Resolve helm ID from account ID if needed.
	if req.AccountID != uuid.Nil && req.HelmID == uuid.Nil {
		helm, err := h.helmSvc.GetByAccount(req.AccountID)
		if err != nil {
			_ = msg.Respond(natsapi.ReplyErr("helm not found"))
			return
		}
		req.HelmID = helm.ID
	}
	// Enforce: caller must own the target helm.
	userID, ok := h.enforceHelmOwner(msg, req.HelmID)
	if !ok {
		return
	}
	cfg := req.ToDomain()
	rt, err := h.reg.Get(cfg.HelmID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("helm runtime not available"))
		return
	}
	// Use .Cash (liquid cash) consistent with the HTTP handler.
	if overflow, _ := handservice.CheckCapitalAllocation(rt.Portfolio.Summary().Cash.InexactFloat64(), h.handMgr.ListByHelm(cfg.HelmID), cfg.AllocatedCapital, ""); overflow != nil {
		_ = msg.Respond(natsapi.ReplyErr(overflow.Error))
		return
	}
	// multi-hand same-symbol allowed — net qty vs per-hand qty gap deferred
	// if err := handservice.CheckSymbolConflict(h.handMgr.ListByHelm(cfg.HelmID), cfg.Symbols, ""); err != nil {
	// 	_ = msg.Respond(natsapi.ReplyErr(err.Error()))
	// 	return
	// }
	instance, err := h.handMgr.Create(cfg)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand created", "id", instance.Data.ID, "caller_user_id", userID)
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
	// Enforce ownership before reading or mutating.
	if _, ok := h.enforceHandOwner(msg, id); !ok {
		return
	}
	patch := raw.UpdateHandReq.ToDomain()
	if err := h.handMgr.Update(id, patch); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	bi, _ := h.handMgr.Get(id)
	_ = msg.Respond(natsapi.ReplyOK(bi.Summary()))
}

func (h *NATSHandler) start(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if _, ok := h.enforceHandOwner(msg, id); !ok {
		return
	}
	if err := h.handMgr.Start(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand started", "id", id)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "started", ID: id.String()}))
}

func (h *NATSHandler) stop(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if _, ok := h.enforceHandOwner(msg, id); !ok {
		return
	}
	if err := h.handMgr.Stop(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand stopped", "id", id)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "stopped", ID: id.String()}))
}

func (h *NATSHandler) kill(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if _, ok := h.enforceHandOwner(msg, id); !ok {
		return
	}
	if err := h.handMgr.Kill(context.Background(), id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Warn("nats: hand killed", "id", id)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "killed", ID: id.String()}))
}

func (h *NATSHandler) natsPause(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if _, ok := h.enforceHandOwner(msg, id); !ok {
		return
	}
	if err := h.handMgr.Pause(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand paused", "id", id)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "paused", ID: id.String()}))
}

func (h *NATSHandler) natsResume(msg *nats.Msg) {
	id, err := parseUUID(msg.Data)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json or id"))
		return
	}
	if _, ok := h.enforceHandOwner(msg, id); !ok {
		return
	}
	if err := h.handMgr.Resume(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand resumed", "id", id)
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
