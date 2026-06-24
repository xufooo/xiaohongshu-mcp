package browser

import (
	"context"
	"errors"
	"testing"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

func TestManagerStartupPanicReturnsError(t *testing.T) {
	manager := NewManager(func() *hrod.Browser {
		panic("launch failed")
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := manager.Acquire(ctx)
	if err == nil {
		t.Fatal("Acquire should return startup error")
	}
}

func TestManagerStartupRespectsContext(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	manager := NewManager(func() *hrod.Browser {
		close(started)
		<-finish
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := manager.Acquire(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire error = %v, want context deadline exceeded", err)
	}

	lockCtx, lockCancel := context.WithTimeout(context.Background(), time.Second)
	defer lockCancel()
	if err := manager.lock(lockCtx); err != nil {
		t.Fatalf("startup must not hold operation token: %v", err)
	}
	manager.releaseToken()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("browser startup did not begin")
	}
	close(finish)
}
