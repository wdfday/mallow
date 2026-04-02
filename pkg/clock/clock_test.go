package clock

import (
	"testing"
	"time"
)

func TestFixedClock(t *testing.T) {
	ts := time.Date(2026, 3, 7, 10, 0, 0, 0, time.UTC)
	c := FixedClock{Time: ts}
	if got := c.Now(); !got.Equal(ts) {
		t.Fatalf("got %v, want %v", got, ts)
	}
}
