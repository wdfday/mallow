package exchange_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"mallow/helm/internal/infra/exchange"
)

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "fake" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestIsAmbiguousPlaceError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel ambiguous", fmt.Errorf("place: %w", exchange.ErrAmbiguous), true},
		{"sentinel rejected wins over substring", fmt.Errorf("timeout but %w", exchange.ErrRejected), false},
		{"context deadline", context.DeadlineExceeded, true},
		{"net.Error timeout", fmt.Errorf("dial: %w", fakeTimeout{}), true},
		{"substring 503", errors.New("binance: HTTP 503 service unavailable"), true},
		{"substring i/o timeout", errors.New("read tcp: i/o timeout"), true},
		{"definitive reject -1013", errors.New("<APIError> code=-1013, msg=Filter failure: LOT_SIZE"), false},
		{"insufficient balance", errors.New("code=-2010 insufficient balance"), false},
	}
	for _, c := range cases {
		if got := exchange.IsAmbiguousPlaceError(c.err); got != c.want {
			t.Errorf("%s: IsAmbiguousPlaceError = %v, want %v", c.name, got, c.want)
		}
	}
}
