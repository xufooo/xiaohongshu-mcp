package xiaohongshu

import (
	"context"
	"errors"
	"testing"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// This same-package expired-active fixture has no TTL timer. It verifies the
// snapshot/close APIs, not the main package's expired CreateBrowseSession chain.
func TestFocusedExpiredActiveSnapshotAndContextClose(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	if session, ok := manager.ActiveSessionSnapshot(); ok || session != nil {
		t.Fatal("empty manager returned a candidate")
	}
	session := manager.Create(nil, nil, nil)
	calls := 0
	sentinel := errors.New("expired cleanup sentinel")
	var seenCtx context.Context
	session.mu.Lock()
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
	}
	session.expiresAt = time.Now().Add(-time.Minute)
	session.page = &hrod.Page{}
	session.onCloseContext = func(ctx context.Context, _ *hrod.Page, _, _ bool) error {
		calls++
		seenCtx = ctx
		if calls == 1 {
			return sentinel
		}
		return nil
	}
	session.mu.Unlock()
	candidate, ok := manager.ActiveSessionSnapshot()
	if !ok || candidate != session || calls != 0 || session.timer != nil {
		t.Fatal("snapshot cleaned or hid the expired active candidate")
	}
	if check := candidate.CheckReusable(context.Background()); check.Status != SessionExpired {
		t.Fatalf("expired candidate check: %+v", check)
	}
	<-session.opToken
	if closed, err := candidate.TryCloseIdleContext(context.Background()); closed || err != nil || calls != 0 {
		t.Fatalf("busy candidate must not start cleanup: closed=%t err=%v calls=%d", closed, err, calls)
	}
	session.opToken <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	closed, err := candidate.TryCloseIdleContext(ctx)
	if !closed || !errors.Is(err, sentinel) || seenCtx != ctx || calls != 1 {
		t.Fatalf("cleanup context/error lost: closed=%t err=%v calls=%d", closed, err, calls)
	}
	if len(session.opToken) != 1 || session.page == nil || manager.closing[session.id] != session {
		t.Fatal("failed cleanup must restore the token and retain resource responsibility")
	}
	if active, ok := manager.ActiveSessionSnapshot(); ok || active != nil {
		t.Fatal("closing resource must not be offered as an active candidate")
	}
	if closed, err := candidate.TryCloseIdleContext(ctx); !closed || err != nil {
		t.Fatalf("cleanup retry: closed=%t err=%v", closed, err)
	}
	if len(session.opToken) != 1 || session.page != nil || calls != 2 {
		t.Fatal("successful retry must release the token and retire the page")
	}
}

func TestFocusedCanceledCloseRetainsPageAndLeaseUntilExplicitRetry(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	session := manager.Create(nil, nil, nil)
	calls := 0
	page := &hrod.Page{}
	session.mu.Lock()
	session.page = page
	session.onCloseContext = func(ctx context.Context, got *hrod.Page, discard, leaseReleased bool) error {
		calls++
		if ctx.Err() != nil || got != page || leaseReleased {
			t.Error("first actual cleanup must retain the original page and lease")
		}
		return nil
	}
	session.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.CloseContext(ctx, session.ID()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled close: %v", err)
	}
	if calls != 0 || session.page != page || session.closeLeaseReleased || !session.closed || manager.closing[session.id] != session || len(manager.workers) != 0 {
		t.Fatal("canceled first cleanup must retain closing responsibility without callback or worker")
	}
	if err := manager.CloseContext(context.Background(), session.ID()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || session.page != nil || len(manager.closing) != 0 {
		t.Fatal("explicit retry must retire the page and release once")
	}
}

func TestFocusedLifecycleCreationJoinsCleanupError(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	createErr := errors.New("commit rejected")
	cleanupErr := errors.New("cleanup failed")
	calls := 0
	var retained *BrowseSession
	ctx := context.Background()
	_, err := manager.CreateContextWithLifecycle(ctx, nil, nil,
		func(got context.Context, _ *hrod.Page, discard, leaseReleased bool) error {
			calls++
			if (calls == 1 && got != ctx) || !discard || (calls == 1 && leaseReleased) {
				t.Error("cleanup lost creation context or lifecycle state")
			}
			if calls == 1 {
				return cleanupErr
			}
			return nil
		}, func(session *BrowseSession, insert func()) error {
			retained = session
			session.page = &hrod.Page{}
			return createErr
		})
	if !errors.Is(err, createErr) || !errors.Is(err, cleanupErr) || retained == nil {
		t.Fatalf("creation and cleanup errors must both survive: %v", err)
	}
	if manager.closing[retained.id] != retained || retained.page == nil || !retained.closeLeaseReleased {
		t.Fatal("failed cleanup lost its retry responsibility")
	}
	if err := manager.CloseContext(ctx, retained.id); err != nil || calls != 2 {
		t.Fatalf("explicit cleanup retry: calls=%d err=%v", calls, err)
	}
}

func TestFocusedCanceledLifecycleCreationDoesNotStartCleanup(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var retained *BrowseSession
	_, err := manager.CreateContextWithLifecycle(ctx, nil, nil,
		func(context.Context, *hrod.Page, bool, bool) error {
			calls++
			return nil
		}, func(session *BrowseSession, insert func()) error {
			retained = session
			session.page = &hrod.Page{}
			cancel()
			return context.Canceled
		})
	if !errors.Is(err, context.Canceled) || retained == nil || calls != 0 {
		t.Fatalf("canceled creation started cleanup: calls=%d err=%v", calls, err)
	}
	if manager.closing[retained.id] != retained || retained.closeLeaseReleased || len(manager.workers) != 0 {
		t.Fatal("canceled creation must preserve page and lease without background work")
	}
}
