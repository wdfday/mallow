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

// parseHandRef parses {id, helm_id} from the message, verifies the caller owns the
// helm, and returns both IDs. Used by get/update/start/stop/kill — every hand
// operation is addressed through its owning helm.
func (h *NATSHandler) parseHandRef(msg *nats.Msg) (handID, helmID uuid.UUID, ok bool) {
	var req struct {
		ID     string `json:"id"`
		HelmID string `json:"helm_id"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json"))
		return uuid.Nil, uuid.Nil, false
	}
	handID, err := uuid.Parse(req.ID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid id"))
		return uuid.Nil, uuid.Nil, false
	}
	helmID, err = uuid.Parse(req.HelmID)
	if err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid helm_id"))
		return uuid.Nil, uuid.Nil, false
	}
	if _, ok := h.enforceHelmOwner(msg, helmID); !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return handID, helmID, true
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
	id, helmID, ok := h.parseHandRef(msg)
	if !ok {
		return
	}
	summary, err := h.handMgr.Get(id)
	if err != nil || summary.HelmID != helmID {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	_ = msg.Respond(natsapi.ReplyOK(summary))
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
	slog.Info("nats: hand created", "id", instance.ID, "caller_user_id", userID)
	_ = msg.Respond(natsapi.ReplyOK(instance))
}

func (h *NATSHandler) update(msg *nats.Msg) {
	var raw struct {
		ID     string `json:"id"`
		HelmID string `json:"helm_id"`
		handDto.UpdateHandReq
	}
	if err := json.Unmarshal(msg.Data, &raw); err != nil {
		_ = msg.Respond(natsapi.ReplyErr("invalid json: " + err.Error()))
		return
	}
	id, helmID, ok := h.parseHandRef(msg)
	if !ok {
		return
	}
	// Confirm the hand belongs to the helm in the request before mutating.
	if cur, err := h.handMgr.Get(id); err != nil || cur.HelmID != helmID {
		_ = msg.Respond(natsapi.ReplyErr("not found"))
		return
	}
	patch := raw.UpdateHandReq.ToDomain()
	if err := h.handMgr.Update(id, patch); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	summary, _ := h.handMgr.Get(id)
	_ = msg.Respond(natsapi.ReplyOK(summary))
}

func (h *NATSHandler) start(msg *nats.Msg) {
	id, helmID, ok := h.parseHandRef(msg)
	if !ok {
		return
	}
	if err := h.handMgr.Start(id, helmID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand started", "id", id)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "started", ID: id.String()}))
}

func (h *NATSHandler) stop(msg *nats.Msg) {
	id, helmID, ok := h.parseHandRef(msg)
	if !ok {
		return
	}
	if err := h.handMgr.Stop(id, helmID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand stopped", "id", id)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "stopped", ID: id.String()}))
}

func (h *NATSHandler) kill(msg *nats.Msg) {
	id, helmID, ok := h.parseHandRef(msg)
	if !ok {
		return
	}
	if err := h.handMgr.Kill(context.Background(), id, helmID); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: hand killed", "id", id)
	_ = msg.Respond(natsapi.ReplyOK(handDto.HandActionResp{Status: "killed", ID: id.String()}))
}
