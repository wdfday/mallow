package consumer

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/fx"

	"mallow/investment/internal/module/portfolio/command"
	"mallow/investment/internal/module/portfolio/event"
	"mallow/pkg/contracts"
)

// TransactionMsg is the shared NATS payload for investment.transactions.{accountID}.
type TransactionMsg = contracts.InvestmentTransactionMsg

const (
	// maxBatchSize triggers an immediate flush when the buffer reaches this count.
	maxBatchSize = 64
	// flushInterval triggers a flush after this duration even if the buffer is not full.
	flushInterval = 500 * time.Millisecond
	// fetchSize is how many messages to pull per non-blocking NATS fetch.
	fetchSize = 32
	// fetchWait is the max wait for each non-blocking fetch call.
	fetchWait = 50 * time.Millisecond
)

// Consumer polls INVESTMENT_TRANSACTIONS via JetStream pull subscription
// and flushes batches when maxBatchSize or flushInterval is reached.
type Consumer struct {
	sub     *nats.Subscription
	handler *command.Handler
	logger  *slog.Logger
}

func New(
	lc fx.Lifecycle,
	js nats.JetStreamContext,
	handler *command.Handler,
	logger *slog.Logger,
) (*Consumer, error) {
	sub, err := js.PullSubscribe(
		"investment.transactions.>",
		"investment-svc",
		nats.BindStream("INVESTMENT_TRANSACTIONS"),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return nil, err
	}

	c := &Consumer{sub: sub, handler: handler, logger: logger}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go c.poll(ctx)
			return nil
		},
		OnStop: func(_ context.Context) error {
			return sub.Unsubscribe()
		},
	})

	return c, nil
}

// accountBatch groups parsed transactions destined for the same account.
type accountBatch struct {
	accountID uuid.UUID
	userID    uuid.UUID
	txns      []event.TransactionRecorded
	msgs      []*nats.Msg
}

// buffer holds the pending messages waiting to be flushed.
type buffer struct {
	batches map[uuid.UUID]*accountBatch
	badMsgs []*nats.Msg
	size    int // total good messages buffered
}

func newBuffer() *buffer {
	return &buffer{batches: make(map[uuid.UUID]*accountBatch)}
}

func (buf *buffer) add(msg *nats.Msg, ab *accountBatch) {
	b, ok := buf.batches[ab.accountID]
	if !ok {
		b = &accountBatch{accountID: ab.accountID, userID: ab.userID}
		buf.batches[ab.accountID] = b
	}
	b.txns = append(b.txns, ab.txns[0])
	b.msgs = append(b.msgs, msg)
	buf.size++
}

func (buf *buffer) addBad(msg *nats.Msg) {
	buf.badMsgs = append(buf.badMsgs, msg)
}

func (buf *buffer) empty() bool {
	return buf.size == 0 && len(buf.badMsgs) == 0
}

func (c *Consumer) poll(ctx context.Context) {
	buf := newBuffer()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c.flush(shutdownCtx, buf)
			cancel()
			return
		case <-ticker.C:
			if !buf.empty() {
				c.flush(ctx, buf)
				buf = newBuffer()
			}
		default:
			msgs, err := c.sub.Fetch(fetchSize, nats.MaxWait(fetchWait))
			if err != nil && err != nats.ErrTimeout {
				c.logger.Warn("JetStream fetch error", "error", err)
				continue
			}

			for _, msg := range msgs {
				ab, err := c.parse(msg)
				if err != nil {
					c.logger.Error("parse transaction msg", "error", err)
					buf.addBad(msg)
					continue
				}
				buf.add(msg, ab)
			}

			// Size-triggered flush
			if buf.size >= maxBatchSize {
				c.flush(ctx, buf)
				buf = newBuffer()
				ticker.Reset(flushInterval)
			}
		}
	}
}

func (c *Consumer) flush(ctx context.Context, buf *buffer) {
	for _, msg := range buf.badMsgs {
		msg.Nak()
	}

	for _, b := range buf.batches {
		if err := c.processBatch(ctx, b); err != nil {
			c.logger.Error("process batch",
				"account_id", b.accountID,
				"size", len(b.txns),
				"error", err,
			)
			for _, msg := range b.msgs {
				msg.Nak()
			}
		} else {
			for _, msg := range b.msgs {
				msg.Ack()
			}
		}
	}
}

func (c *Consumer) parse(msg *nats.Msg) (*accountBatch, error) {
	var tm TransactionMsg
	if err := json.Unmarshal(msg.Data, &tm); err != nil {
		return nil, err
	}

	accountID, err := uuid.Parse(tm.AccountID)
	if err != nil {
		return nil, err
	}
	userID, err := uuid.Parse(tm.UserID)
	if err != nil {
		return nil, err
	}

	tx := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionType(tm.Type),
		Symbol:          tm.Symbol,
		Name:            tm.Name,
		AssetType:       tm.AssetType,
		Exchange:        tm.Exchange,
		Currency:        tm.Currency,
		Quantity:        tm.Quantity,
		PricePerUnit:    tm.PricePerUnit,
		Amount:          tm.Amount,
		Fees:            tm.Fees,
		Commission:      tm.Commission,
		Tax:             tm.Tax,
		TransactionDate: tm.TransactionDate,
		Broker:          tm.Broker,
		ExternalID:      tm.ExternalID,
		Source:          "sync",
		BotID:           tm.BotID,
		Notes:           tm.Notes,
	}

	return &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns:      []event.TransactionRecorded{tx},
	}, nil
}

func (c *Consumer) processBatch(ctx context.Context, b *accountBatch) error {
	if len(b.txns) == 1 {
		return c.handler.HandleRecordTransaction(ctx, command.RecordTransaction{
			AccountID:   b.accountID,
			UserID:      b.userID,
			Transaction: b.txns[0],
		})
	}
	return c.handler.HandleRecordTransactionBatch(ctx, b.accountID, b.userID, b.txns)
}
