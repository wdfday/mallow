package command

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"mallow/investment/internal/module/portfolio/aggregate"
	"mallow/investment/internal/module/portfolio/event"
	"mallow/investment/internal/module/portfolio/projection"
	"mallow/investment/internal/module/portfolio/publisher"
	"mallow/investment/internal/module/portfolio/store"
)

var tracer = otel.Tracer("investment/command")

// snapshotInterval defines how many new events trigger an automatic aggregate snapshot.
const snapshotInterval = 50

// Handler loads the aggregate, applies commands, appends events, and runs projectors.
type Handler struct {
	store         store.EventStore
	snapshotStore store.SnapshotStore
	projector     *projection.Runner
	publisher     publisher.EventPublisher
	logger        *slog.Logger
}

func NewHandler(s store.EventStore, ss store.SnapshotStore, p *projection.Runner, pub publisher.EventPublisher, logger *slog.Logger) *Handler {
	return &Handler{
		store:         s,
		snapshotStore: ss,
		projector:     p,
		publisher:     pub,
		logger:        logger.With("component", "command.handler"),
	}
}

func (h *Handler) HandleRecordTransaction(ctx context.Context, cmd RecordTransaction) error {
	ctx, span := tracer.Start(ctx, "HandleRecordTransaction")
	defer span.End()
	span.SetAttributes(
		attribute.String("account_id", cmd.AccountID.String()),
		attribute.String("user_id", cmd.UserID.String()),
		attribute.String("tx_type", string(cmd.Transaction.Type)),
		attribute.String("symbol", cmd.Transaction.Symbol),
	)

	// Ensure UserID is set in the event payload
	cmd.Transaction.UserID = cmd.UserID

	a, err := h.loadAccount(ctx, cmd.AccountID, cmd.UserID)
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	if err := a.RecordTransaction(cmd.Transaction); err != nil {
		return fmt.Errorf("record transaction: %w", err)
	}
	return h.commitAndProject(ctx, a)
}

// HandleRecordTransactionBatch processes multiple transactions for the same account
// in a single aggregate load-apply-commit cycle.
func (h *Handler) HandleRecordTransactionBatch(ctx context.Context, accountID, userID uuid.UUID, txns []event.TransactionRecorded) error {
	ctx, span := tracer.Start(ctx, "HandleRecordTransactionBatch")
	defer span.End()
	span.SetAttributes(
		attribute.String("account_id", accountID.String()),
		attribute.String("user_id", userID.String()),
		attribute.Int("batch_size", len(txns)),
	)

	a, err := h.loadAccount(ctx, accountID, userID)
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	for i := range txns {
		txns[i].UserID = userID
		if err := a.RecordTransaction(txns[i]); err != nil {
			return fmt.Errorf("record transaction[%d]: %w", i, err)
		}
	}
	return h.commitAndProject(ctx, a)
}

func (h *Handler) HandleTakeSnapshot(ctx context.Context, cmd TakeSnapshot) error {
	ctx, span := tracer.Start(ctx, "HandleTakeSnapshot")
	defer span.End()
	span.SetAttributes(
		attribute.String("account_id", cmd.AccountID.String()),
		attribute.String("user_id", cmd.UserID.String()),
		attribute.String("snapshot_type", string(cmd.SnapshotType)),
	)

	a, err := h.loadAccount(ctx, cmd.AccountID, cmd.UserID)
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	if !a.Initialized {
		return nil
	}
	cmd.Payload.UserID = cmd.UserID
	cmd.Payload.SnapshotDate = cmd.SnapshotDate
	cmd.Payload.SnapshotType = cmd.SnapshotType
	if err := a.TakeSnapshot(cmd.Payload); err != nil {
		return fmt.Errorf("take snapshot: %w", err)
	}
	return h.commitAndProject(ctx, a)
}

func (h *Handler) loadAccount(ctx context.Context, accountID, userID uuid.UUID) (*aggregate.Account, error) {
	// 1. Try loading the latest aggregate snapshot
	snap, err := h.snapshotStore.LoadLatest(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}

	var a *aggregate.Account
	var afterSeq int64

	if snap != nil {
		var state store.AggregateState
		if err := json.Unmarshal(snap.State, &state); err != nil {
			h.logger.Warn("corrupt aggregate snapshot, falling back to full replay",
				"account_id", accountID, "error", err)
		} else {
			a = aggregate.RestoreFromSnapshot(state.AccountID, state.UserID, state.Initialized, state.LastSeq)
			afterSeq = state.LastSeq
		}
	}

	if a == nil {
		a = aggregate.New(accountID, userID)
	}

	// 2. Replay only events after the snapshot sequence
	events, err := h.store.LoadFrom(ctx, accountID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("load events from seq %d: %w", afterSeq, err)
	}
	a.Replay(events)
	return a, nil
}

func (h *Handler) commitAndProject(ctx context.Context, a *aggregate.Account) error {
	uncommitted := a.UncommittedEvents()
	if len(uncommitted) == 0 {
		return nil
	}

	// 1. Persist to event store (source of truth — must succeed)
	if err := h.store.Append(ctx, a.AccountID, uncommitted); err != nil {
		return fmt.Errorf("append events: %w", err)
	}
	a.ClearUncommitted()

	// 2. Run projectors synchronously (fast UPSERT — same DB)
	for _, ev := range uncommitted {
		if err := h.projector.Project(ctx, ev); err != nil {
			h.logger.Error("projector failed — read model may be stale; use /admin/rebuild to fix",
				"event_type", ev.EventType,
				"aggregate_id", ev.AggregateID,
				"sequence", ev.Sequence,
				"error", err,
			)
		}
	}

	// 3. Publish to JetStream (best-effort — non-fatal)
	for _, ev := range uncommitted {
		if err := h.publisher.Publish(ctx, ev); err != nil {
			h.logger.Warn("event publish failed",
				"event_type", ev.EventType,
				"sequence", ev.Sequence,
				"error", err,
			)
		}
	}

	// 4. Auto-snapshot: persist aggregate state every N events
	if a.LastSeq > 0 && a.LastSeq%snapshotInterval == 0 {
		h.saveAggregateSnapshot(ctx, a)
	}

	return nil
}

func (h *Handler) saveAggregateSnapshot(ctx context.Context, a *aggregate.Account) {
	state := store.AggregateState{
		AccountID:   a.AccountID,
		UserID:      a.UserID,
		Initialized: a.Initialized,
		LastSeq:     a.LastSeq,
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		h.logger.Error("marshal aggregate snapshot", "error", err)
		return
	}
	snap := store.AggregateSnapshot{
		AggregateID:   a.AccountID,
		AggregateType: "Account",
		Sequence:      a.LastSeq,
		State:         stateJSON,
	}
	if err := h.snapshotStore.Save(ctx, snap); err != nil {
		h.logger.Warn("save aggregate snapshot failed (non-fatal)", "account_id", a.AccountID, "seq", a.LastSeq, "error", err)
	}
}
