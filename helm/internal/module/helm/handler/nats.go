package handler

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/natsapi"
	helmDto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/runtime"
)

// NATSHandler is the NATS request/reply transport adapter for orchestrator operations.
type NATSHandler struct {
	svc     HelmService
	handMgr HandManager
	reg     *runtime.Registry
	nc      *nats.Conn
	subs    []*nats.Subscription
}

func NewNATSHandler(svc HelmService, handMgr HandManager, reg *runtime.Registry) *NATSHandler {
	return &NATSHandler{svc: svc, handMgr: handMgr, reg: reg}
}

func (h *NATSHandler) Subscribe(nc *nats.Conn) error {
	h.nc = nc

	// Request/reply subjects — plain core NATS (low latency, no persistence needed).
	reqReply := map[string]nats.MsgHandler{
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
		natsapi.SubjOrchStats:     h.stats,
	}
	for subj, fn := range reqReply {
		sub, err := nc.Subscribe(subj, fn)
		if err != nil {
			return err
		}
		h.subs = append(h.subs, sub)
	}

	slog.Info("nats: orchestrator handlers subscribed", "req_reply", len(reqReply))
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

	updateReq := helmDto.UpdateReq{Name: raw.Name}
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
	if err := h.svc.Disable(id); err != nil {
		_ = msg.Respond(natsapi.ReplyErr(err.Error()))
		return
	}
	slog.Info("nats: orchestrator disabled via kill", "id", id, "caller_svc", caller.CallerSvc, "caller_user_id", caller.CallerUserID)
	_ = msg.Respond(natsapi.ReplyOK(helmDto.ActionResp{Status: "disabled", ID: id}))
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
	_ = msg.Respond(natsapi.ReplyOK(helmDto.PortfolioToResp(rt.PortfolioSummary())))
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

// statsHandSummary is the per-hand slice of a stats response.
type statsHandSummary struct {
	HandID           string  `json:"hand_id"`
	Symbol           string  `json:"symbol"`
	Strategy         string  `json:"strategy"`
	Status           string  `json:"status"`
	SignalsReceived  int64   `json:"signals_received"`
	SignalsFiltered  int64   `json:"signals_filtered"`
	SignalsDropped   int64   `json:"signals_dropped"`
	OrdersPlaced     int64   `json:"orders_placed"`
	OrdersFilled     int64   `json:"orders_filled"`
	OrdersFailed     int64   `json:"orders_failed"`
	TotalPnL         float64 `json:"total_pnl"`
	WinCount         int64   `json:"win_count"`
	LossCount        int64   `json:"loss_count"`
	SignalLagLastMs  int64   `json:"signal_lag_last_ms"`
	SignalQueueDepth int     `json:"signal_queue_depth"`
}

// statsHelmSummary is the per-helm slice of a stats response.
type statsHelmSummary struct {
	HelmID       string             `json:"helm_id"`
	AccountID    string             `json:"account_id"`
	BrokerType   string             `json:"broker_type"`
	Paused       bool               `json:"paused"`
	Halted       bool               `json:"halted"`
	Equity       float64            `json:"equity"`
	Cash         float64            `json:"cash"`
	DailyPnL     float64            `json:"daily_pnl"`
	DrawdownPct  float64            `json:"drawdown_pct"`
	RunningHands int                `json:"running_hands"`
	LastSyncAt   *time.Time         `json:"last_sync_at,omitempty"`
	Hands        []statsHandSummary `json:"hands"`
}

// statsGlobal is the global/aggregate portion of a stats response.
type statsGlobal struct {
	RunningHands     int   `json:"running_hands"`
	ActiveRuntimes   int   `json:"active_runtimes"`
	RouteNoHelm      int64 `json:"dispatch_route_no_helm"`
	RouteNoHand      int64 `json:"dispatch_route_no_hand"`
	NATSSignalsTotal int64 `json:"nats_signals_total"`
	NATSDispatched   int64 `json:"nats_signals_dispatched"`
	NATSMissingID    int64 `json:"nats_signals_missing_id"`
	NATSNilPayload   int64 `json:"nats_signals_nil_payload"`
}

// statsResponse is the full response for SubjOrchStats.
type statsResponse struct {
	Helms  []statsHelmSummary `json:"helms"`
	Global statsGlobal        `json:"global"`
}

// stats returns a live aggregate snapshot of all runtimes, hands, and global metrics.
// No caller_user_id required — this is a service-to-service internal endpoint used by
// the AI commander to reason about the current trading system state.
func (h *NATSHandler) stats(msg *nats.Msg) {
	rts := h.reg.All()

	resp := statsResponse{
		Helms: make([]statsHelmSummary, 0, len(rts)),
	}

	for _, rt := range rts {
		pf := rt.Portfolio.Summary()
		var lastSyncAt *time.Time
		if t := rt.LastSyncAt(); !t.IsZero() {
			lastSyncAt = &t
		}
		helmStat := statsHelmSummary{
			HelmID:      rt.HelmID.String(),
			AccountID:   rt.AccountID.String(),
			BrokerType:  rt.BrokerType,
			Paused:      rt.IsPaused(),
			Halted:      rt.RiskMgr.IsHalted(),
			Equity:      pf.Equity.InexactFloat64(),
			Cash:        pf.Cash.InexactFloat64(),
			DailyPnL:    pf.DailyPnL.InexactFloat64(),
			DrawdownPct: pf.CurrentDD,
			LastSyncAt:  lastSyncAt,
		}

		for _, hs := range rt.HandSummaries() {
			m := hs.Metrics
			if hs.Status == "running" || hs.Status == "paused" {
				helmStat.RunningHands++
			}
			helmStat.Hands = append(helmStat.Hands, statsHandSummary{
				HandID:           hs.ID,
				Symbol:           hs.Symbol,
				Strategy:         hs.StrategyName,
				Status:           hs.Status,
				SignalsReceived:  m.SignalsReceived,
				SignalsFiltered:  m.SignalsFiltered,
				SignalsDropped:   m.SignalsDropped,
				OrdersPlaced:     m.OrdersPlaced,
				OrdersFilled:     m.OrdersFilled,
				OrdersFailed:     m.OrdersFailed,
				TotalPnL:         m.TotalPnL.InexactFloat64(),
				WinCount:         m.WinCount,
				LossCount:        m.LossCount,
				SignalLagLastMs:  m.LatestSignalLagMs,
				SignalQueueDepth: m.SignalQueueDepth,
			})
		}
		resp.Helms = append(resp.Helms, helmStat)
	}

	ds := h.reg.DispatchStats()
	ns := h.reg.NATSStats()
	resp.Global = statsGlobal{
		RunningHands:     len(h.handMgr.RunningHands()),
		ActiveRuntimes:   len(rts),
		RouteNoHelm:      ds.RouteNoHelm,
		RouteNoHand:      ds.RouteNoHand,
		NATSSignalsTotal: ns.SignalsTotal,
		NATSDispatched:   ns.SignalsDispatched,
		NATSMissingID:    ns.SignalsMissingID,
		NATSNilPayload:   ns.SignalsNilPayload,
	}

	_ = msg.Respond(natsapi.ReplyOK(resp))
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
