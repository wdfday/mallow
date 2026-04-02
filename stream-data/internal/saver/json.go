package saver

import (
	"encoding/json"
	"os"

	"stream-data/internal/model"
)

type JSONSaver struct{}

func (JSONSaver) Extension() string { return "json" }

func (JSONSaver) Save(ticks []model.Tick, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(ticks)
}
