package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// Installed after Connect on a production-owned page and root controller.
// The barrier is before forwarding the selected Target query to Chrome.
// It verifies client/context cancellation, not Chrome-side query completion.
type serviceTargetCancelClient struct {
	upstream serviceCDPCaller
	blockAt  int32
	calls    atomic.Int32
	entered  chan struct{}
	abort    chan struct{}
	stop     sync.Once
}

func (c *serviceTargetCancelClient) Event() <-chan *cdp.Event { return nil }

func (c *serviceTargetCancelClient) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	if method == "Target.getTargetInfo" {
		request, ok := params.(proto.TargetGetTargetInfo)
		if sessionID != "" || !ok || request.TargetID == "" {
			return nil, fmt.Errorf("expected browser-level explicit target query: session=%q params=%#v", sessionID, params)
		}
		if c.calls.Add(1) == c.blockAt {
			close(c.entered)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-c.abort:
				return nil, errors.New("target query gate aborted by watchdog")
			}
		}
	}
	return c.upstream.Call(ctx, sessionID, method, params)
}

func newServiceTargetCancelFixture(t *testing.T, blockAt int32) (*XiaohongshuService, *browser.Manager, *hrod.Page, *serviceTargetCancelClient) {
	t.Helper()
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
	page, err := acquireForWithWatchdog(manager, profileDir, ctx, cancel, "seed", 35*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ReleaseContext(ctx, page); err != nil {
		t.Fatal(err)
	}
	if !manager.Stats().WarmPageCached {
		t.Fatal("fixture must exercise the real Manager warm cache")
	}
	controller := page.Rod.Browser()
	gate := &serviceTargetCancelClient{
		upstream: controller.Context(controller.GetContext()), blockAt: blockAt,
		entered: make(chan struct{}), abort: make(chan struct{}),
	}
	controller.Client(gate)
	page.Browser().Rod().Rod().Client(gate)
	t.Cleanup(func() { gate.stop.Do(func() { close(gate.abort) }) })
	return service, manager, page, gate
}

func cancelEnteredServiceTarget(t *testing.T, gate *serviceTargetCancelClient, cancel context.CancelFunc, done <-chan error) error {
	t.Helper()
	joined := false
	defer func() {
		cancel()
		gate.stop.Do(func() { close(gate.abort) })
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("operation did not join after target gate abort")
			}
		}
	}()
	select {
	case <-gate.entered:
		cancel()
	case err := <-done:
		joined = true
		t.Fatalf("operation returned before target gate: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Target query did not enter gate")
	}
	select {
	case err := <-done:
		joined = true
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not return after Target query cancellation")
		return nil
	}
}

func TestCreateBrowseSessionOnURLCancellationReturnsGateAndLease(t *testing.T) {
	// Query 1 is Manager's warm probe; query 2 is EnsureReadyOn -> onURL.
	service, manager, seed, gate := newServiceTargetCancelFixture(t, 2)
	lifetime := seed.Rod.Browser().GetContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var returnedResult atomic.Bool
	go func() {
		result, err := service.CreateBrowseSession(ctx, false)
		returnedResult.Store(result != nil)
		done <- err
	}()
	if err := cancelEnteredServiceTarget(t, gate, cancel, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("creation cancellation lost: %v", err)
	}
	if returnedResult.Load() {
		t.Fatal("canceled creation returned a session result")
	}
	if _, ok := service.browseSessions.ActiveInfo(); ok {
		t.Fatal("canceled EnsureReady committed a session")
	}
	if lifetime.Err() != nil || seed.Rod.GetContext().Err() != nil {
		t.Fatal("creation canceled shared browser/page lifetime")
	}
	// ReleaseContext retains the standalone warm page even after creation fails;
	// no session owns this lease. The next owner must receive the same target.
	if !manager.Stats().WarmPageCached {
		t.Fatal("failed creation lost Manager's warm-page responsibility")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelProbe()
	releaseGate, err := service.acquireCreateSessionGate(probeCtx, false)
	if err != nil {
		t.Fatalf("creation gate leaked: %v", err)
	}
	releaseGate()
	page, fresh, err := manager.AcquireForWithSource(probeCtx, "after-canceled-create")
	if err != nil || fresh || page != seed {
		t.Fatalf("Manager lease/warm target not returned: page=%v fresh=%t err=%v", page, fresh, err)
	}
	defer manager.Release(page)
	if _, err := (proto.TargetGetTargetInfo{TargetID: page.Rod.TargetID}).Call(page.Rod.Browser().Context(probeCtx)); err != nil {
		t.Fatalf("fresh-context Target query failed: %v", err)
	}
	// An extra release would allow this contender to steal a live owner's lease.
	contendCtx, cancelContend := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelContend()
	contender, _, err := manager.AcquireForWithSource(contendCtx, "contender")
	if contender != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("creation returned lease more than once: page=%v err=%v", contender, err)
	}
}

func TestManagerWarmProbeCancellationAfterEntryReturnsLease(t *testing.T) {
	_, manager, seed, gate := newServiceTargetCancelFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var returnedPage atomic.Bool
	go func() {
		page, _, err := manager.AcquireForWithSource(ctx, "canceled-warm-probe")
		returnedPage.Store(page != nil)
		done <- err
	}()
	if err := cancelEnteredServiceTarget(t, gate, cancel, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("warm probe cancellation lost: %v", err)
	}
	if returnedPage.Load() {
		t.Fatal("canceled warm probe returned a page")
	}
	if seed.Rod.Browser().GetContext().Err() != nil {
		t.Fatal("warm probe canceled browser lifetime")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelProbe()
	if _, err := (proto.TargetGetTargetInfo{TargetID: seed.Rod.TargetID}).Call(seed.Rod.Browser().Context(probeCtx)); err != nil {
		t.Fatalf("fresh-context query failed: %v", err)
	}
	// The canceled probe's target is retained as pending cleanup, not silently
	// forgotten. A valid-context cleanup must remain possible before next use.
	if err := manager.ClosePageContext(probeCtx, seed); err != nil {
		t.Fatalf("pending warm target cleanup failed: %v", err)
	}
	page, fresh, err := manager.AcquireForWithSource(probeCtx, "after-warm-cancel")
	if err != nil || page == nil || !fresh || page == seed {
		t.Fatalf("warm probe leaked lease: page=%v fresh=%t err=%v", page, fresh, err)
	}
	if err := manager.ReleaseAndCloseContext(probeCtx, page); err != nil {
		t.Fatal(err)
	}
}
