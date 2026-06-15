package perf

import "time"

// Page is a cursor-based page request used by TradeLog and FillLog.
// After=zero value means from the beginning.
// Limit=0 defaults to a reasonable page size (implementation-defined).
type Page struct {
	After time.Time // exclusive lower bound on TS; zero = from start
	Limit int
}
