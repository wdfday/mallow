package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo_RetryThenSuccess(t *testing.T) {
	var attempts int32
	errTransient := errors.New("transient")

	err := Do(context.Background(), Policy{
		MaxAttempts:  3,
		InitialDelay: time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Multiplier:   2,
		JitterRatio:  0,
	}, func(err error) bool {
		return errors.Is(err, errTransient)
	}, func(context.Context) error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return errTransient
		}
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestDo_NonRetryableStopsImmediately(t *testing.T) {
	var attempts int32
	errPermanent := errors.New("permanent")

	err := Do(context.Background(), DefaultPolicy(), func(error) bool { return false }, func(context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return errPermanent
	})

	if !errors.Is(err, errPermanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestDo_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Do(ctx, DefaultPolicy(), func(error) bool { return true }, func(context.Context) error {
		return errors.New("transient")
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}
