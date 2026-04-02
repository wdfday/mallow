package model

// Tick represents one real-time price update from a streaming provider.
// Parquet tags allow direct serialization; optional fields use pointer-free
// omitempty via the parquet "optional" annotation.
type Tick struct {
	Timestamp int64   `json:"t"             parquet:"t"` // Unix ms
	Symbol    string  `json:"s"             parquet:"s"`
	Class     string  `json:"class"         parquet:"class"` // stocks | crypto | forex
	Source    string  `json:"src"           parquet:"src"`   // binance | alpaca | twelvedata
	Price     float64 `json:"p"             parquet:"p"`
	Volume    float64 `json:"v,omitempty"   parquet:"v,optional"`
	Bid       float64 `json:"bid,omitempty" parquet:"bid,optional"`
	Ask       float64 `json:"ask,omitempty" parquet:"ask,optional"`
}
