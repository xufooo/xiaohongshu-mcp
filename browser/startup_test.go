package browser

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

func TestTimedOutStartupKeepsSingleFactory(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	manager := NewManager(func(context.Context) (*hrod.Browser, error) {
		calls.Add(1)
		close(entered)
		<-release
		return nil, errors.New("late factory failure")
	})
	started := newBrowserStartup(time.Minute)
	manager.starting = started
	manager.wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.startBrowser(started)
	}()
	t.Cleanup(func() {
		close(release)
		started.cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("startup worker did not join")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("factory did not start")
	}
	manager.failStartup(started)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if browser, err := manager.getBrowser(ctx); browser != nil || err == nil {
		t.Fatalf("timed-out startup must remain unavailable: browser=%v err=%v", browser, err)
	}
	manager.mu.Lock()
	retained := manager.starting == started
	manager.mu.Unlock()
	if !retained || calls.Load() != 1 {
		t.Fatalf("startup ownership lost: retained=%t factory calls=%d", retained, calls.Load())
	}
}

func TestCanceledAcquireDoesNotTakeAvailableToken(t *testing.T) {
	manager := NewManager(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.lockForOwner(ctx, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller acquired the lease: %v", err)
	}
	if len(manager.token) != 1 || manager.owner != "" {
		t.Fatal("canceled acquisition retained the token or changed its owner")
	}
	if page, err := manager.Acquire(ctx); page != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition started browser work: page=%v err=%v", page, err)
	}
	manager.mu.Lock()
	starting := manager.starting
	manager.mu.Unlock()
	if starting != nil {
		t.Fatal("canceled acquisition created a startup worker")
	}
}

func TestAcquireBlocksOnUnknownBrowserCleanup(t *testing.T) {
	unknown := hrod.NewBrowser(&headless_browser.Browser{}, humanize.Config{})
	startupErr := errors.New("startup returned an unconfirmed browser")
	var calls atomic.Int32
	manager := NewManager(func(context.Context) (*hrod.Browser, error) {
		calls.Add(1)
		return unknown, startupErr
	}, WithIdleTimeout(0))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if page, err := manager.Acquire(ctx); page != nil || !errors.Is(err, startupErr) {
		t.Fatalf("startup failure must retain its browser: page=%v err=%v", page, err)
	}
	if err := manager.lockForOwner(ctx, "lease-probe"); err != nil {
		t.Fatal(err)
	}
	defer manager.releaseToken()
	for attempt := 0; attempt < 2; attempt++ {
		if page, err := manager.Acquire(ctx); page != nil || err == nil {
			t.Fatalf("unknown cleanup must block Acquire: page=%v err=%v", page, err)
		}
	}
	manager.mu.Lock()
	retained := len(manager.pendingBrowsers) == 1 && manager.pendingBrowsers[0] == unknown && manager.browserCleanups[unknown] != nil
	owner := manager.owner
	manager.mu.Unlock()
	if !retained || calls.Load() != 1 || owner != "lease-probe" || len(manager.token) != 0 {
		t.Fatal("unknown cleanup must retain responsibility without another factory or lease release")
	}
}
