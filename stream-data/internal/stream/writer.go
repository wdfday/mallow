package stream

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"stream-data/internal/model"
	"stream-data/internal/saver"
)

// Writer batches incoming Ticks per symbol and flushes to disk when either:
//   - FlushInterval elapses (time-based), OR
//   - total buffered ticks across all symbols reaches MaxBufferSize (size-based)
//
// Each flush produces one file per symbol:
//
//	{BaseDir}/{class}/{SYMBOL}/tick_{YYYY-MM-DD_HH-MM}.{ext}
type Writer struct {
	BaseDir       string
	Saver         saver.TickSaver
	FlushInterval time.Duration
	MaxBufferSize int // flush early when total ticks exceeds this; 0 = disabled

	mu         sync.Mutex
	buffers    map[string][]model.Tick // key: symbol
	totalTicks int
}

func NewWriter(baseDir string, s saver.TickSaver, interval time.Duration, maxBuf int) *Writer {
	return &Writer{
		BaseDir:       baseDir,
		Saver:         s,
		FlushInterval: interval,
		MaxBufferSize: maxBuf,
		buffers:       make(map[string][]model.Tick),
	}
}

// Run drains ticks from ch, batches by symbol, and flushes on interval or size limit.
// Returns when ctx is cancelled (final flush before exit).
func (w *Writer) Run(ctx context.Context, ch <-chan model.Tick) {
	flushTicker := time.NewTicker(w.FlushInterval)
	defer flushTicker.Stop()

	hbTicker := time.NewTicker(30 * time.Second)
	defer hbTicker.Stop()

	var received int64

	for {
		select {
		case t, ok := <-ch:
			if !ok {
				w.flush()
				return
			}
			w.mu.Lock()
			w.buffers[t.Symbol] = append(w.buffers[t.Symbol], t)
			w.totalTicks++
			full := w.MaxBufferSize > 0 && w.totalTicks >= w.MaxBufferSize
			w.mu.Unlock()
			received++

			if full {
				w.flush()
			}

		case <-flushTicker.C:
			w.flush()

		case <-hbTicker.C:
			slog.Info("heartbeat", "received", received, "buffered", w.totalTicks)

		case <-ctx.Done():
			w.flush()
			return
		}
	}
}

func (w *Writer) flush() {
	w.mu.Lock()
	snapshot := w.buffers
	w.buffers = make(map[string][]model.Tick)
	w.totalTicks = 0
	w.mu.Unlock()

	now := time.Now().UTC()
	label := now.Format("2006-01-02_15-04")

	for symbol, ticks := range snapshot {
		if len(ticks) == 0 {
			continue
		}
		class := ticks[0].Class
		dir := filepath.Join(w.BaseDir, class, symbol)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Error("writer: mkdir failed", "symbol", symbol, "err", err)
			continue
		}
		ext := w.Saver.Extension()
		path := filepath.Join(dir, fmt.Sprintf("tick_%s.%s", label, ext))
		if err := w.Saver.Save(ticks, path); err != nil {
			slog.Error("writer: save failed", "symbol", symbol, "path", path, "err", err)
		} else {
			slog.Info("writer: flushed", "symbol", symbol, "ticks", len(ticks), "path", path)
		}
	}
}
