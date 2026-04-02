package saver

import (
	"github.com/parquet-go/parquet-go"

	"stream-data/internal/model"
)

type ParquetSaver struct{}

func (ParquetSaver) Extension() string { return "parquet" }

func (ParquetSaver) Save(ticks []model.Tick, path string) error {
	return parquet.WriteFile(path, ticks)
}
