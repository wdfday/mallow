package engine

import (
	"sync"
	"time"
)

// SignalAggregator collects bot signals for the same (symbol, bar_timestamp)
// and produces one AggregatedSignal per symbol per bar.
//
// Window: all signals sharing the same bar timestamp (field T in SignalMsg)
// are grouped together. A short flush timer (flushAfter) fires after the last
// signal in a window to handle any stragglers.
//
// Rules:
//  1. "close" from any bot → AggregatedSignal{Direction: "close"} immediately.
//  2. net = mean(long strengths) - mean(short strengths)
//     net >  deadband → Long,  strength = net
//     net < -deadband → Short, strength = |net|
//     |net| ≤ deadband → none (conflicting signals, skip)
type SignalAggregator struct {
	mu         sync.Mutex
	windows    map[string]*signalWindow // key = symbol+":"+barTimestamp
	flushAfter time.Duration
	deadband   float64
	out        chan AggregatedSignal
}

type signalWindow struct {
	symbol  string
	barTS   int64
	msgs    []rawSignal
	timer   *time.Timer
	flushed bool
}

type rawSignal struct {
	botID     string
	direction string
	strength  float64
}

// NewSignalAggregator creates an aggregator.
//   - flushAfter: how long to wait after first signal before flushing (e.g. 200ms)
//   - deadband: net threshold below which signals are considered conflicting (e.g. 0.15)
func NewSignalAggregator(flushAfter time.Duration, deadband float64) *SignalAggregator {
	return &SignalAggregator{
		windows:    make(map[string]*signalWindow),
		flushAfter: flushAfter,
		deadband:   deadband,
		out:        make(chan AggregatedSignal, 64),
	}
}

// Out returns the channel of aggregated signals. Consume in a goroutine.
func (a *SignalAggregator) Out() <-chan AggregatedSignal {
	return a.out
}

// Add ingests a single bot signal. Thread-safe.
func (a *SignalAggregator) Add(symbol string, barTS int64, botID, direction string, strength float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Close is high-priority: flush immediately.
	if direction == "close" {
		a.flushNow(symbol, barTS, []rawSignal{{botID: botID, direction: "close", strength: 1.0}})
		return
	}

	key := windowKey(symbol, barTS)
	w, ok := a.windows[key]
	if !ok {
		w = &signalWindow{symbol: symbol, barTS: barTS}
		a.windows[key] = w
		// Schedule flush after flushAfter duration.
		w.timer = time.AfterFunc(a.flushAfter, func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			if !w.flushed {
				a.flush(key, w)
			}
		})
	}
	w.msgs = append(w.msgs, rawSignal{botID: botID, direction: direction, strength: strength})
}

// flush aggregates a window and sends result to out channel.
// Must be called with a.mu held.
func (a *SignalAggregator) flush(key string, w *signalWindow) {
	w.flushed = true
	if w.timer != nil {
		w.timer.Stop()
	}
	delete(a.windows, key)

	result := aggregate(w.symbol, w.barTS, w.msgs, a.deadband)
	if result.Direction == "none" {
		return
	}
	select {
	case a.out <- result:
	default:
		// drop if consumer is slow — prefer low latency
	}
}

// flushNow handles a close signal immediately, cancelling any pending window.
// Must be called with a.mu held.
func (a *SignalAggregator) flushNow(symbol string, barTS int64, msgs []rawSignal) {
	key := windowKey(symbol, barTS)
	if w, ok := a.windows[key]; ok {
		w.flushed = true
		if w.timer != nil {
			w.timer.Stop()
		}
		delete(a.windows, key)
	}
	result := aggregate(symbol, barTS, msgs, 0)
	select {
	case a.out <- result:
	default:
	}
}

// aggregate implements the aggregation rules.
func aggregate(symbol string, barTS int64, msgs []rawSignal, deadband float64) AggregatedSignal {
	var longs, shorts []rawSignal
	var sources []string

	for _, m := range msgs {
		sources = append(sources, m.botID)
		switch m.direction {
		case "long":
			longs = append(longs, m)
		case "short":
			shorts = append(shorts, m)
		case "close":
			return AggregatedSignal{
				Symbol: symbol, Timestamp: barTS,
				Direction: "close", Strength: 1.0, Sources: sources,
			}
		}
	}

	longAvg := meanStrength(longs)
	shortAvg := meanStrength(shorts)
	net := longAvg - shortAvg

	dir := "none"
	strength := 0.0
	switch {
	case net > deadband:
		dir, strength = "long", net
	case net < -deadband:
		dir, strength = "short", -net
	}

	return AggregatedSignal{
		Symbol: symbol, Timestamp: barTS,
		Direction: dir, Strength: clamp(strength),
		Sources: sources,
	}
}

func meanStrength(sigs []rawSignal) float64 {
	if len(sigs) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range sigs {
		sum += s.strength
	}
	return sum / float64(len(sigs))
}

func clamp(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}

func windowKey(symbol string, barTS int64) string {
	// Fast string key: "BTCUSDT:1710201600000"
	return symbol + ":" + int64ToStr(barTS)
}

func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
