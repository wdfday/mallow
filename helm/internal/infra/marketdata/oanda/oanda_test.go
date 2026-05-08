package oanda

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestHandleMessage_Price(t *testing.T) {
	var calls []struct {
		instrument string
		price      decimal.Decimal
	}
	onTick := func(instrument string, price decimal.Decimal) {
		calls = append(calls, struct {
			instrument string
			price      decimal.Decimal
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
	expected, _ := decimal.NewFromString("1.0851")
	tolerance, _ := decimal.NewFromString("0.00001")
	diff := calls[0].price.Sub(expected).Abs()
	if diff.GreaterThan(tolerance) {
		t.Errorf("price = %s, want ~%s", calls[0].price, expected)
	}
}

func TestHandleMessage_Heartbeat(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }

	msg := []byte(`{"type": "HEARTBEAT", "time": "2024-01-15T14:30:00Z"}`)
	handleMessage(msg, onTick)

	if called {
		t.Error("heartbeat should not trigger onTick")
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }

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
		want decimal.Decimal
	}{
		{"normal", []priceLevel{{"1.08500", 0}}, []priceLevel{{"1.08520", 0}}, func() decimal.Decimal { d, _ := decimal.NewFromString("1.0851"); return d }()},
		{"empty bids", nil, []priceLevel{{"1.08520", 0}}, decimal.Zero},
		{"empty asks", []priceLevel{{"1.08500", 0}}, nil, decimal.Zero},
	}

	tolerance, _ := decimal.NewFromString("0.00001")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := midPrice(tt.bids, tt.asks)
			diff := got.Sub(tt.want).Abs()
			if diff.GreaterThan(tolerance) {
				t.Errorf("midPrice() = %s, want %s", got, tt.want)
			}
		})
	}
}
