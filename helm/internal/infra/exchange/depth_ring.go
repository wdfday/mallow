package exchange

import (
	"sync"
	"sync/atomic"
)

// depthRingCap is the fixed capacity of DepthRing.
//
// Must be a power of two — this lets the index wrap use a bitmask
// (h & depthRingMask) instead of modulo (h % cap), avoiding a division
// instruction on every read/write.
//
// Why 512: at @depth5@100ms each update arrives every 100ms.
//
//	512 entries ≈ 51 seconds of history.
//
// That is more than enough to warm up any indicator (RSI-14 needs 14,
// MACD needs 26, Bollinger 20, etc.). 256 would also work; 512 costs
// ~180 KB of stack-allocated array (512 × ~360 B per L2Snapshot) which
// is negligible — so we round up for safety.
const depthRingCap = 512
const depthRingMask = depthRingCap - 1

// DepthRing is a fixed-capacity ring buffer of L2Snapshot.
//
// Backing storage is a compile-time array [depthRingCap]L2Snapshot —
// one allocation at construction, never grown, no cap field needed.
//
// Window semantics (two monotonic counters):
//
//	tail ≤ head   →   valid entries occupy indices [tail, head)
//
//	not full  (head < cap):  tail = 0,        head grows
//	full      (head ≥ cap):  tail = head-cap, both advance together
//
// So the window always slides forward; oldest entry is always at tail,
// newest at head-1.
//
// Concurrency:
//   - Latest()        — fully lock-free via atomic.Pointer[L2Snapshot]
//   - Oldest / Last   — short sync.RWMutex around the slice copy
//   - Push            — single writer assumed (the WS receive goroutine)
type DepthRing struct {
	buf  [depthRingCap]L2Snapshot
	head atomic.Uint64
	tail atomic.Uint64
	mu   sync.RWMutex
	tip  atomic.Pointer[L2Snapshot] // latest snapshot, lock-free
}

// Push writes a new snapshot. Must be called from a single goroutine.
// When the ring is full, tail advances in step with head.
func (r *DepthRing) Push(s L2Snapshot) {
	h := r.head.Add(1) - 1
	if h >= depthRingCap {
		r.tail.Store(h - depthRingCap + 1)
	}

	r.mu.Lock()
	r.buf[h&depthRingMask] = s
	r.mu.Unlock()

	clone := s
	r.tip.Store(&clone)
}

// Latest returns the most recently pushed snapshot. Lock-free.
func (r *DepthRing) Latest() (L2Snapshot, bool) {
	p := r.tip.Load()
	if p == nil {
		return L2Snapshot{}, false
	}
	return *p, true
}

// Oldest returns up to n snapshots in chronological order (oldest first).
// Use for warm-up: feed the slice straight into an indicator loop.
func (r *DepthRing) Oldest(n int) []L2Snapshot {
	tail := r.tail.Load()
	head := r.head.Load()
	length := head - tail
	if length == 0 || n == 0 {
		return nil
	}
	if uint64(n) > length {
		n = int(length)
	}
	out := make([]L2Snapshot, n)
	r.mu.RLock()
	for i := 0; i < n; i++ {
		out[i] = r.buf[(tail+uint64(i))&depthRingMask]
	}
	r.mu.RUnlock()
	return out
}

// Last returns up to n snapshots in reverse chronological order (newest first).
func (r *DepthRing) Last(n int) []L2Snapshot {
	tail := r.tail.Load()
	head := r.head.Load()
	length := head - tail
	if length == 0 || n == 0 {
		return nil
	}
	if uint64(n) > length {
		n = int(length)
	}
	out := make([]L2Snapshot, n)
	r.mu.RLock()
	for i := 0; i < n; i++ {
		out[i] = r.buf[(head-1-uint64(i))&depthRingMask]
	}
	r.mu.RUnlock()
	return out
}

// Head returns the monotonic write counter.
func (r *DepthRing) Head() uint64 { return r.head.Load() }

// Tail returns the monotonic read floor — entries before this index have been
// overwritten and are no longer accessible.
func (r *DepthRing) Tail() uint64 { return r.tail.Load() }

// Len returns the number of valid snapshots currently in the ring.
func (r *DepthRing) Len() int { return int(r.head.Load() - r.tail.Load()) }

// Cap returns the fixed capacity of the ring.
func (r *DepthRing) Cap() int { return depthRingCap }
