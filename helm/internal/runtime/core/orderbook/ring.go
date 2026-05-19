package orderbook

// symbolState bundles exchange constraints with a fixed-cap update history
// for a single trading pair.
type symbolState struct {
	info    SymbolInfo
	history bookUpdateRing
}

// bookUpdateRing is a fixed-capacity circular buffer of BookUpdate values.
// Zero heap allocation after init; push/latest/snapshot are O(capacity).
type bookUpdateRing struct {
	buf  []BookUpdate
	head int // index of the next write slot
	size int // number of valid entries (≤ len(buf))
}

func newBookUpdateRing(capacity int) bookUpdateRing {
	if capacity <= 0 {
		capacity = defaultSymbolHistoryCapacity
	}
	return bookUpdateRing{
		buf: make([]BookUpdate, capacity),
	}
}

// push appends an update, overwriting the oldest entry when the buffer is full.
func (r *bookUpdateRing) push(update BookUpdate) {
	if len(r.buf) == 0 {
		return
	}
	r.buf[r.head] = update
	r.head = (r.head + 1) % len(r.buf)
	if r.size < len(r.buf) {
		r.size++
	}
}

// latest returns the most recently pushed update, or false if empty.
func (r *bookUpdateRing) latest() (BookUpdate, bool) {
	if r.size == 0 {
		return BookUpdate{}, false
	}
	idx := (r.head - 1 + len(r.buf)) % len(r.buf)
	return r.buf[idx], true
}

// snapshot returns up to limit updates in chronological order (oldest first).
func (r *bookUpdateRing) snapshot(limit int) []BookUpdate {
	if r.size == 0 {
		return nil
	}
	if limit <= 0 || limit > r.size {
		limit = r.size
	}
	out := make([]BookUpdate, 0, limit)
	start := (r.head - r.size + len(r.buf)) % len(r.buf)
	skip := r.size - limit
	for i := 0; i < r.size; i++ {
		if i < skip {
			continue
		}
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}
