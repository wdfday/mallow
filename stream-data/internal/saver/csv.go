package saver

import (
	"encoding/csv"
	"os"
	"strconv"

	"stream-data/internal/model"
)

type CSVSaver struct{}

func (CSVSaver) Extension() string { return "csv" }

func (CSVSaver) Save(ticks []model.Tick, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{"t", "s", "class", "src", "p", "v", "bid", "ask"}); err != nil {
		return err
	}
	for _, tk := range ticks {
		if err := w.Write([]string{
			strconv.FormatInt(tk.Timestamp, 10),
			tk.Symbol,
			tk.Class,
			tk.Source,
			fstr(tk.Price),
			fstr(tk.Volume),
			fstr(tk.Bid),
			fstr(tk.Ask),
		}); err != nil {
			return err
		}
	}
	return nil
}

func fstr(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
