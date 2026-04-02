package saver

import (
	"strings"

	"stream-data/internal/model"
)

// TickSaver persists a batch of ticks to a file path.
type TickSaver interface {
	Save(ticks []model.Tick, path string) error
	Extension() string
}

// New returns a TickSaver for the given format (parquet | csv | json).
// Returns nil for unknown formats.
func New(format string) TickSaver {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "parquet":
		return ParquetSaver{}
	case "csv":
		return CSVSaver{}
	case "json":
		return JSONSaver{}
	default:
		return nil
	}
}
