package oanda

import (
	"testing"
)

func TestHandleMessage_Price(t *testing.T) {
	var calls []struct {
		instrument string
		price      float64
	}
	onTick := func(instrument string, price float64) {
		calls = append(calls, struct {
			instrument string
			price      float64
		}{instrument, price})
	}

	msg := []byte(`{
		"type": "PRICE",
		"instrument": "EUR_USD",
		"bids": [{"price": "1.08500", "liquidity": 1000000}],
		"asks": [{"price": "1.08520", "liquidity": 1000000}],
		"time": "2024-01-15T14:30:00.000000000Z"
	}`)

	handleMessage(msg, onTick)

	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].instrument != "EUR_USD" {
		t.Errorf("instrument = %q, want EUR_USD", calls[0].instrument)
	}
	// Mid-price: (1.08500 + 1.08520) / 2 = 1.08510
	expected := 1.0851
	if calls[0].price < expected-0.00001 || calls[0].price > expected+0.00001 {
		t.Errorf("price = %f, want ~%f", calls[0].price, expected)
	}
}

func TestHandleMessage_Heartbeat(t *testing.T) {
	called := false
	onTick := func(string, float64) { called = true }

	msg := []byte(`{"type": "HEARTBEAT", "time": "2024-01-15T14:30:00Z"}`)
	handleMessage(msg, onTick)

	if called {
		t.Error("heartbeat should not trigger onTick")
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	called := false
	onTick := func(string, float64) { called = true }

	handleMessage([]byte("not json"), onTick)

	if called {
		t.Error("invalid JSON should not trigger onTick")
	}
}

func TestMidPrice(t *testing.T) {
	tests := []struct {
		name string
		bids []priceLevel
		asks []priceLevel
		want float64
	}{
		{"normal", []priceLevel{{"1.08500", 0}}, []priceLevel{{"1.08520", 0}}, 1.0851},
		{"empty bids", nil, []priceLevel{{"1.08520", 0}}, 0},
		{"empty asks", []priceLevel{{"1.08500", 0}}, nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := midPrice(tt.bids, tt.asks)
			if got < tt.want-0.00001 || got > tt.want+0.00001 {
				t.Errorf("midPrice() = %f, want %f", got, tt.want)
			}
		})
	}
}
