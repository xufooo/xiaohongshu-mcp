package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// This gate sits at Rod's CDP-client boundary. It proves the query was entered
// with the operation context, not that Chrome received or processed the query.
// The fallback releases even a wrongly scoped call after the test watchdog.
type targetInfoGateClient struct {
	upstream interface {
		Call(context.Context, string, string, interface{}) ([]byte, error)
	}
	entered chan struct{}
	abort   chan struct{}
	stop    sync.Once
	blockAt int32
	calls   atomic.Int32
	cancel  context.CancelFunc
}

func (c *targetInfoGateClient) Event() <-chan *cdp.Event { return nil }

func (c *targetInfoGateClient) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	if method != "Target.getTargetInfo" {
		return c.upstream.Call(ctx, sessionID, method, params)
	}
	request, ok := params.(proto.TargetGetTargetInfo)
	if sessionID != "" || !ok || request.TargetID == "" {
		return nil, fmt.Errorf("expected browser-level query with explicit target: session=%q params=%#v", sessionID, params)
	}
	if c.calls.Add(1) == c.blockAt {
		close(c.entered)
		if c.cancel != nil {
			c.cancel()
			// A successful response concurrent with cancellation must be rejected.
			return []byte(`{"targetInfo":{"targetId":"target-1","url":"https://www.xiaohongshu.com/explore"}}`), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.abort:
			return nil, errors.New("target gate aborted by watchdog")
		}
	}
	return c.upstream.Call(ctx, sessionID, method, params)
}

func installTargetInfoGate(t *testing.T, page *hrod.Page, blockAt int32) *targetInfoGateClient {
	t.Helper()
	controller := page.Rod.Browser()
	gate := &targetInfoGateClient{
		upstream: controller.Context(controller.GetContext()),
		entered: make(chan struct{}), abort: make(chan struct{}), blockAt: blockAt,
	}
	controller.Client(gate)
	t.Cleanup(func() { gate.stop.Do(func() { close(gate.abort) }) })
	return gate
}

// On a regression, abort and join the call before fixture teardown as well.
func cancelTargetQueryAfterEntry(t *testing.T, gate *targetInfoGateClient, cancel context.CancelFunc, run func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run() }()
	joined := false
	defer func() {
		cancel()
		gate.stop.Do(func() { close(gate.abort) })
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("target query did not join after fixture abort")
			}
		}
	}()
	select {
	case <-gate.entered:
		cancel()
	case err := <-done:
		joined = true
		t.Fatalf("production path returned before query entry: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("production path did not enter Target query")
	}
	select {
	case err := <-done:
		joined = true
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("production path did not return after cancellation")
		return nil
	}
}

func TestProductionTargetQueriesCancelAfterEntry(t *testing.T) {
	cases := []struct {
		name string
		run  func(context.Context, *BrowseSession) error
	}{
		{"ensure_ready", func(ctx context.Context, s *BrowseSession) error {
			return EnsureReadyOn(s.page.Context(ctx), ExploreURL, XHSReadyHomeSearch, 0)
		}},
		{"search", func(ctx context.Context, s *BrowseSession) error {
			_, err := NewSearchAction(s.page).Search(ctx, "cancel")
			return err
		}},
		{"search_feeds_only", func(ctx context.Context, s *BrowseSession) error {
			_, err := NewSearchAction(s.page).SearchFeedsOnly(ctx, "cancel")
			return err
		}},
		{"search_by_ui", func(ctx context.Context, s *BrowseSession) error {
			return NewSearchAction(s.page).searchByUI(ctx, s.page.Context(ctx), &evalTimeoutCounter{}, "cancel")
		}},
		{"publish_success", func(ctx context.Context, s *BrowseSession) error {
			return waitPublishSuccess(s.page.Context(ctx))
		}},
		{"login", func(ctx context.Context, s *BrowseSession) error {
			return NewLogin(s.page).Login(ctx)
		}},
		{"qrcode", func(ctx context.Context, s *BrowseSession) error {
			_, _, err := NewLogin(s.page).FetchQrcodeImage(ctx)
			return err
		}},
		{"profile", func(ctx context.Context, s *BrowseSession) error {
			return NewNavigate(s.page).ToProfilePage(ctx)
		}},
		{"current_url", func(ctx context.Context, s *BrowseSession) error {
			_, err := s.currentPageURL(ctx, nil)
			return err
		}},
		{"recover_comment_batch", func(ctx context.Context, s *BrowseSession) error {
			return s.recoverCommentBatchSession(ctx, s.page, nil, "feed-1")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &currentPageURLCDPClient{response: []byte(`{"targetInfo":{"targetId":"target-1","url":"https://www.xiaohongshu.com/explore"}}`)}
			session := newCurrentPageURLSession(t, client)
			lifetime := session.page.Rod.Browser().GetContext()
			pageCtx := session.page.Rod.GetContext()
			gate := installTargetInfoGate(t, session.page, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := cancelTargetQueryAfterEntry(t, gate, cancel, func() error { return tc.run(ctx, session) })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if lifetime.Err() != nil || pageCtx.Err() != nil {
				t.Fatal("operation canceled shared browser/page lifetime")
			}
			freshCtx, cancelFresh := context.WithTimeout(context.Background(), time.Second)
			defer cancelFresh()
			if _, err := pageTargetInfo(freshCtx, session.page); err != nil {
				t.Fatalf("fresh-context query failed: %v", err)
			}
			for _, method := range client.methodHistory {
				if method == "Page.navigate" || method == "Page.close" || method == "Browser.close" {
					t.Fatalf("canceled path navigated or closed shared resources: %v", client.methodHistory)
				}
			}
		})
	}
}

func TestPageTargetInfoCancellationAtSuccessfulReturn(t *testing.T) {
	session := newCurrentPageURLSession(t, &currentPageURLCDPClient{})
	gate := installTargetInfoGate(t, session.page, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate.cancel = cancel
	info, err := pageTargetInfo(ctx, session.page)
	if info != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("successful response must not mask cancellation: info=%v err=%v", info, err)
	}
}

func TestSearchBatchReconcileCancellationPreservesErrorAndToken(t *testing.T) {
	client := &currentPageURLCDPClient{}
	session := newCurrentPageURLSession(t, client)
	session.id = "reconcile-cancel"
	session.timeout = time.Minute
	session.expiresAt = time.Now().Add(time.Minute)
	t.Cleanup(func() {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.timer != nil {
			session.timer.Stop()
		}
	})
	session.opToken = make(chan struct{}, 1)
	session.opToken <- struct{}{}
	session.closedCh = make(chan struct{})
	session.opened = true
	session.currentFeedID = "keep-until-reconciled"
	// An invalid filter fails before any query. The first query is therefore
	// the real searchBatch deferred reconcile, not an isolated helper call.
	gate := installTargetInfoGate(t, session.page, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := cancelTargetQueryAfterEntry(t, gate, cancel, func() error {
		_, _, _, err := session.SearchBatch(ctx, "cancel", []FilterOption{{SortBy: "invalid-sort"}}, nil, 1)
		return err
	})
	if err == nil || errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "invalid-sort") {
		t.Fatalf("reconcile replaced the original validation error: %v", err)
	}
	if len(session.opToken) != 1 || session.opCtx != nil || !session.opened || session.currentFeedID != "keep-until-reconciled" {
		t.Fatal("canceled reconcile changed note state or failed to finish operation")
	}
	before := gate.calls.Load()
	session.reconcileAfterFailedSearch(ctx, errors.New("already canceled"))
	if gate.calls.Load() != before {
		t.Fatal("already canceled reconcile must skip Target query")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), time.Second)
	defer cancelProbe()
	if _, err := session.beginLockedOperation(probeCtx, false); err != nil {
		t.Fatalf("next operation cannot acquire token: %v", err)
	}
	session.releaseOperation()
}

func TestLoginURLResponseCannotMaskCancellationBeforeNavigation(t *testing.T) {
	for _, qrcode := range []bool{false, true} {
		name := "login"
		if qrcode {
			name = "qrcode"
		}
		t.Run(name, func(t *testing.T) {
			client := &currentPageURLCDPClient{}
			session := newCurrentPageURLSession(t, client)
			gate := installTargetInfoGate(t, session.page, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gate.cancel = cancel
			var err error
			if qrcode {
				_, _, err = NewLogin(session.page).FetchQrcodeImage(ctx)
			} else {
				err = NewLogin(session.page).Login(ctx)
			}
			if !errors.Is(err, context.Canceled) || client.calls != 0 {
				t.Fatalf("cancellation at URL response must prevent further I/O: err=%v methods=%v", err, client.methodHistory)
			}
		})
	}
}
