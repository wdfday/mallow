package model

// Bar is a completed OHLCV bar for one symbol over one time interval.
type Bar struct {
	OpenTime int64 // Unix ms — floor(firstTick.Timestamp, interval)
	Symbol   string
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}
