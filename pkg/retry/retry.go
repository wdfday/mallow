package retry

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"time"
)

// Policy controls retry behavior.
type Policy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	JitterRatio  float64
}

// DefaultPolicy returns a safe default policy for transient network errors.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts:  3,
		InitialDelay: 200 * time.Millisecond,
		MaxDelay:     2 * time.Second,
		Multiplier:   2.0,
		JitterRatio:  0.2,
	}
}

// Do runs fn with retries based on policy.
func Do(ctx context.Context, policy Policy, isRetryable func(error) bool, fn func(context.Context) error) error {
	policy = normalizePolicy(policy)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if !isRetryable(err) || attempt == policy.MaxAttempts {
			return err
		}

		delay := delayForAttempt(policy, attempt, rng)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	return lastErr
}

func normalizePolicy(policy Policy) Policy {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	if policy.InitialDelay <= 0 {
		policy.InitialDelay = 100 * time.Millisecond
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = 2 * time.Second
	}
	if policy.Multiplier <= 1 {
		policy.Multiplier = 2
	}
	if policy.JitterRatio < 0 {
		policy.JitterRatio = 0
	}
	if policy.JitterRatio > 1 {
		policy.JitterRatio = 1
	}
	return policy
}

func delayForAttempt(policy Policy, attempt int, rng *rand.Rand) time.Duration {
	power := math.Pow(policy.Multiplier, float64(attempt-1))
	base := float64(policy.InitialDelay) * power
	if base > float64(policy.MaxDelay) {
		base = float64(policy.MaxDelay)
	}

	if policy.JitterRatio == 0 {
		return time.Duration(base)
	}

	spread := base * policy.JitterRatio
	minVal := base - spread
	maxVal := base + spread
	if maxVal < minVal {
		minVal, maxVal = maxVal, minVal
	}

	jittered := minVal + rng.Float64()*(maxVal-minVal)
	if jittered < 0 {
		jittered = 0
	}
	return time.Duration(jittered)
}
