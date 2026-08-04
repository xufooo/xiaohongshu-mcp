package humanize

import (
	"context"
	"math/rand"
	"time"
)

// randDuration returns a random duration in [min, max].
func randDuration(min, max time.Duration) time.Duration {
	if min >= max {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min)))
}

// Sleep pauses for a random short duration, useful between operations.
func Sleep(min, max time.Duration) {
	_ = SleepContext(context.Background(), min, max)
}

// SleepContext pauses for a random short duration unless ctx is cancelled.
func SleepContext(ctx context.Context, min, max time.Duration) error {
	return sleepWithContext(ctx, randDuration(min, max))
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
