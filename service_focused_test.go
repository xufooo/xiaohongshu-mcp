package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// A1 exercises an assembled lifecycle callback, not the service closure's interior.
func TestFocusedManagerHandoffDuringFatalUpgrade(t *testing.T) {
	profileDir := t.TempDir()
	manager := browser.NewManager(newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir),
		browser.WithWarmPageTTL(time.Minute), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	released := make(chan struct{})
	resume := make(chan struct{})
	var resumed atomic.Bool
	unblock := func() {
		if resumed.CompareAndSwap(false, true) {
			close(resume)
		}
	}
	t.Cleanup(func() {
		unblock()
		if err := runFixtureCloseWithWatchdog(15*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("session cleanup: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(15*time.Second, 2*time.Second, manager.Close); err != nil {
			t.Errorf("manager cleanup: %v", err)
		}
	})
	seed, err := acquireForWithWatchdog(manager, profileDir, ctx, cancel, "seed", 35*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Browser() == nil || seed.Rod == nil || seed.Rod.Browser() == nil {
		t.Fatal("A1 requires a real browser-owned page")
	}
	if err := manager.ReleaseContext(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if !manager.Stats().WarmPageCached {
		t.Fatal("seed must exercise the real warm cache")
	}
	page, fresh, err := manager.AcquireForWithSource(ctx, "session:handoff")
	if err != nil || fresh || page != seed {
		t.Fatalf("session must acquire the warm page: fresh=%t err=%v", fresh, err)
	}
	var releases, retries atomic.Int32
	session, err := sessions.CreateContextWithLifecycle(ctx, page, nil,
		func(closeCtx context.Context, closingPage *hrod.Page, discard, leaseReleased bool) error {
			if leaseReleased {
				if !discard {
					return errors.New("resource-only retry must retain fatal discard")
				}
				retries.Add(1)
				return manager.ClosePageContext(closeCtx, closingPage)
			}
			releases.Add(1)
			if err := manager.ReleaseAndCloseContext(closeCtx, closingPage); err != nil {
				return err
			}
			close(released)
			select {
			case <-resume:
				return nil
			case <-closeCtx.Done():
				return closeCtx.Err()
			}
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- sessions.CloseContext(ctx, session.ID()) }()
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if manager.Stats().WarmPageCached {
		t.Fatal("ordinary session close must not park its page")
	}
	if !sessions.CloseInvalidatedAsync(session.ID()) {
		t.Fatal("fatal upgrade must find the closing session before callback acknowledgement")
	}
	next, fresh, err := manager.AcquireForWithSource(ctx, "probe")
	if err != nil || !fresh || next == page || next == nil {
		t.Fatalf("new owner must acquire a fresh page: fresh=%t err=%v", fresh, err)
	}
	unblock()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := sessions.CloseAllContext(ctx); err != nil {
		t.Fatal(err)
	}
	if releases.Load() != 1 || retries.Load() != 1 {
		t.Fatalf("lease release/fatal resource retry = %d/%d", releases.Load(), retries.Load())
	}
	if _, err := next.Context(ctx).Eval("() => 42"); err != nil {
		t.Fatalf("fatal retry closed the new owner's page: %v", err)
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, 100*time.Millisecond)
	contender, _, probeErr := manager.AcquireForWithSource(probeCtx, "contender")
	cancelProbe()
	if contender != nil || !errors.Is(probeErr, context.DeadlineExceeded) {
		t.Fatalf("old cleanup returned the new owner's lease: page=%v err=%v", contender, probeErr)
	}
	if err := manager.ReleaseContext(ctx, next); err != nil {
		t.Fatal(err)
	}
	reused, fresh, err := manager.AcquireForWithSource(ctx, "after-fatal")
	if err != nil || fresh || reused != next {
		t.Fatalf("new owner's warm cache must remain usable: fresh=%t err=%v", fresh, err)
	}
	if err := manager.ReleaseAndCloseContext(ctx, reused); err != nil {
		t.Fatal(err)
	}
}

// A2 binds the actual service closure with a real Manager and warm cache enabled.
// It does not inject the lease-return/session-commit boundary inside that closure.
func TestFocusedServiceSessionCloseSkipsWarmCache(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		name := "ordinary"
		if fatal {
			name = "fatal"
		}
		t.Run(name, func(t *testing.T) {
			profileDir := t.TempDir()
			manager := browser.NewManager(newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir),
				browser.WithWarmPageTTL(time.Minute), browser.WithIdleTimeout(time.Minute))
			sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
			service := &XiaohongshuService{browserManager: manager, browseSessions: sessions}
			t.Cleanup(func() {
				if err := runFixtureCloseWithWatchdog(15*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
					t.Errorf("session cleanup: %v", err)
				}
				if err := runFixtureCloseWithWatchdog(15*time.Second, 2*time.Second, manager.Close); err != nil {
					t.Errorf("manager cleanup: %v", err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			page, err := acquireForWithWatchdog(manager, profileDir, ctx, cancel, "browser_operation", 35*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if page.Browser() == nil || page.Rod == nil || page.Rod.Browser() == nil {
				t.Fatal("A2 requires a real browser-owned page")
			}
			created, err := service.registerReadyBrowseSession(ctx, page, manager.Release, manager.UpdateOwner)
			if err != nil || created == nil || created.Session == nil {
				t.Fatalf("register: result=%+v err=%v", created, err)
			}
			if fatal {
				if !sessions.CloseInvalidatedAsync(created.Session.ID) {
					t.Fatal("fatal invalidation must find the service session")
				}
				err = sessions.CloseAllContext(ctx)
			} else {
				err = sessions.CloseContext(ctx, created.Session.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if manager.Stats().WarmPageCached {
				t.Fatal("service session page entered the warm cache")
			}
			if _, err := page.Context(ctx).Eval("() => 42"); err == nil {
				t.Fatal("service session target survived successful close")
			}
			next, fresh, err := manager.AcquireForWithSource(ctx, "next-owner")
			if err != nil || !fresh || next == nil || next == page {
				t.Fatalf("next owner: fresh=%t err=%v", fresh, err)
			}
			if _, err := next.Context(ctx).Eval("() => 42"); err != nil {
				t.Fatal(err)
			}
			if err := manager.ReleaseAndCloseContext(ctx, next); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFocusedNonForcedCreateCleanupContext(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "shutdown", "sentinel", "success"} {
		t.Run(mode, func(t *testing.T) {
			sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
			cleanupErr := errors.New("cleanup sentinel")
			var cleanupComplete, allowCleanup atomic.Bool
			var factoryCalls atomic.Int32
			var successFactory browser.BrowserFactory
			if mode == "success" {
				successFactory = newServiceBrowserFactoryFixture(t, nil, nil, nil, nil, true)
			}
			manager := browser.NewManager(func(ctx context.Context) (*hrod.Browser, error) {
				factoryCalls.Add(1)
				if !cleanupComplete.Load() {
					return nil, errors.New("page acquisition preceded cleanup completion")
				}
				if successFactory == nil {
					return nil, errors.New("unexpected page acquisition after cleanup failure")
				}
				return successFactory(ctx)
			})
			service := &XiaohongshuService{browserManager: manager, browseSessions: sessions}
			browserCtx, cancelBrowser := context.WithCancel(context.Background())
			defer cancelBrowser()
			controller := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(&sessionCleanupPageClient{events: make(chan *cdp.Event)})
			if err := controller.Connect(); err != nil {
				t.Fatal(err)
			}
			rodPage, err := controller.PageFromTarget("nonforced-cleanup")
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan context.Context, 1)
			session, err := sessions.CreateContextWithLifecycle(context.Background(), &hrod.Page{Rod: rodPage}, nil,
				func(closeCtx context.Context, _ *hrod.Page, _, _ bool) error {
					if allowCleanup.Load() {
						return nil
					}
					entered <- closeCtx
					if mode == "sentinel" {
						return cleanupErr
					}
					if mode == "success" {
						cleanupComplete.Store(true)
						return nil
					}
					<-closeCtx.Done()
					return closeCtx.Err()
				}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				allowCleanup.Store(true)
				if err := runFixtureCloseWithWatchdog(15*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
					t.Errorf("session cleanup: %v", err)
				}
				if err := runFixtureCloseWithWatchdog(15*time.Second, 2*time.Second, manager.Close); err != nil {
					t.Errorf("manager cleanup: %v", err)
				}
			})
			if !session.Info().ExpiresAt.After(time.Now()) {
				t.Fatal("candidate must be active and unexpired")
			}
			requestCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if mode == "deadline" {
				var deadlineCancel context.CancelFunc
				requestCtx, deadlineCancel = context.WithTimeout(requestCtx, time.Second)
				defer deadlineCancel()
			}
			done := make(chan error, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				result, err := service.CreateBrowseSession(requestCtx, false)
				if mode == "success" {
					if err == nil && (result == nil || result.Session == nil || result.Outcome != "created") {
						err = errors.New("cleanup succeeded without creating a new session")
					}
				} else if result != nil {
					err = errors.Join(err, errors.New("unexpected successful result"))
				}
				done <- err
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-joined:
				case <-time.After(3 * time.Second):
					t.Error("creation caller did not join after cancellation")
				}
			})
			var cleanupCtx context.Context
			select {
			case cleanupCtx = <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("non-forced active candidate did not enter cleanup")
			}
			wantErr := context.Canceled
			switch mode {
			case "cancel":
				cancel()
			case "deadline":
				wantErr = context.DeadlineExceeded
				if deadline, ok := cleanupCtx.Deadline(); !ok {
					t.Fatal("cleanup lost the request deadline")
				} else if requestDeadline, _ := requestCtx.Deadline(); !deadline.Equal(requestDeadline) {
					t.Fatal("cleanup changed the request deadline")
				}
			case "shutdown":
				allowCleanup.Store(true)
				closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancelClose()
				if err := service.Close(closeCtx); err != nil {
					t.Fatalf("shutdown must cancel cleanup and obtain creation gate: %v", err)
				}
			case "sentinel":
				wantErr = cleanupErr
			case "success":
				wantErr = nil
			}
			completionBudget := 3 * time.Second
			if mode == "success" {
				completionBudget = 65 * time.Second
			}
			select {
			case err := <-done:
				if !errors.Is(err, wantErr) {
					t.Fatalf("want %v, got %v", wantErr, err)
				}
			case <-time.After(completionBudget):
				t.Fatal("non-forced creation ignored cleanup cancellation/error")
			}
			wantCalls := int32(0)
			if mode == "success" {
				wantCalls = 1
			}
			if factoryCalls.Load() != wantCalls {
				t.Fatalf("factory calls = %d, want %d", factoryCalls.Load(), wantCalls)
			}
			probeCtx, cancelProbe := context.WithTimeout(context.Background(), time.Second)
			defer cancelProbe()
			gate, err := service.acquireCreateSessionGate(probeCtx, true)
			if err != nil {
				t.Fatalf("creation gate not released: %v", err)
			}
			gate()
		})
	}
}

// This transport fixture is only for B's busy candidate behavior; A1/A2 above
// deliberately use production browser owners instead.
type focusedBusyReuseClient struct {
	*sessionCleanupPageClient
	block   atomic.Bool
	entered chan struct{}
	resume  chan struct{}
}

func (c *focusedBusyReuseClient) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	if c.block.Load() && method == "Runtime.callFunctionOn" {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.resume:
		}
	}
	return c.sessionCleanupPageClient.Call(ctx, sessionID, method, params)
}

func TestFocusedNonForcedCreateBusyDoesNotClean(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &focusedBusyReuseClient{
		sessionCleanupPageClient: &sessionCleanupPageClient{events: make(chan *cdp.Event)},
		entered:                 make(chan struct{}, 1),
		resume:                  make(chan struct{}),
	}
	controller := rod.New().Context(ctx).NoDefaultDevice().ControlURL("").Client(client)
	if err := controller.Connect(); err != nil {
		t.Fatal(err)
	}
	rodPage, err := controller.PageFromTarget("busy-candidate")
	if err != nil {
		t.Fatal(err)
	}
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	var cleanups, factoryCalls atomic.Int32
	manager := browser.NewManager(func(context.Context) (*hrod.Browser, error) {
		factoryCalls.Add(1)
		return nil, errors.New("busy candidate must not acquire a page")
	})
	service := &XiaohongshuService{browseSessions: sessions, browserManager: manager}
	session, err := sessions.CreateContextWithLifecycle(ctx, &hrod.Page{Rod: rodPage}, nil,
		func(context.Context, *hrod.Page, bool, bool) error {
			cleanups.Add(1)
			return nil
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runFixtureCloseWithWatchdog(time.Second, time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("session cleanup: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(time.Second, time.Second, manager.Close); err != nil {
			t.Errorf("manager cleanup: %v", err)
		}
	})
	client.block.Store(true)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		session.CheckReusable(ctx)
	}()
	t.Cleanup(func() {
		close(client.resume)
		cancel()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("busy health check did not join")
		}
	})
	select {
	case <-client.entered:
	case <-ctx.Done():
		t.Fatal("health check did not acquire the operation token")
	}
	result, err := service.CreateBrowseSession(ctx, false)
	if err != nil || result == nil || result.Outcome != "blocked" || result.Status.Status != xiaohongshu.SessionBusy {
		t.Fatalf("busy candidate result=%+v err=%v", result, err)
	}
	candidate, ok := sessions.ActiveSessionSnapshot()
	if !ok || candidate != session || cleanups.Load() != 0 || factoryCalls.Load() != 0 {
		t.Fatal("busy creation cleaned or replaced its candidate")
	}
	gate, err := service.acquireCreateSessionGate(ctx, true)
	if err != nil {
		t.Fatalf("busy result retained creation gate: %v", err)
	}
	gate()
}
