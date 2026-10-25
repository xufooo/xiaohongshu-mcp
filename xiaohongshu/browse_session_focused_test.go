package xiaohongshu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
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

func TestFeedCursorRejectsABAAndConsumedClones(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	session := manager.Create(nil, nil, nil)
	filterKey := filterKeyFromFilters(nil)
	start := func(keyword string) *FeedCursor {
		t.Helper()
		if err := session.validateFeedCursor(nil, FeedPageSearch, keyword, filterKey); err != nil {
			t.Fatal(err)
		}
		session.invalidateFeedCursor()
		cursor := &FeedCursor{Kind: FeedPageSearch, Keyword: keyword, FilterKey: filterKey}
		session.advanceFeedCursor(cursor)
		return cursor
	}
	oldA := start("A")
	oldAClone := *oldA
	start("B")
	newA := start("A")
	if err := session.validateFeedCursor(&oldAClone, FeedPageSearch, "A", filterKey); err == nil {
		t.Fatal("A to B to A revived an old cursor")
	}
	clone := *newA
	if err := session.validateFeedCursor(&clone, FeedPageSearch, "A", "wrong-filter"); err == nil {
		t.Fatal("filter mismatch accepted")
	}
	if err := session.validateFeedCursor(&clone, FeedPageSearch, "A", filterKey); err != nil {
		t.Fatal(err)
	}
	if err := session.validateFeedCursor(&clone, FeedPageSearch, "A", filterKey); err != nil {
		t.Fatal("an uncommitted cursor must remain usable after a failed operation")
	}
	session.advanceFeedCursor(&clone)
	if err := session.validateFeedCursor(newA, FeedPageSearch, "A", filterKey); err == nil {
		t.Fatal("copying a cursor duplicated its consumption right")
	}
}

func TestFeedCursorConcurrentClonesConsumeOnceUnderOperationToken(t *testing.T) {
	client := &searchP0CDPClient{feedBatch: true}
	rawPage, _ := newSearchP0Element(t, client)
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	session := manager.Create(nil, nil, nil)
	session.page = (&hrod.Page{Rod: rawPage, Keyboard: rawPage.Keyboard}).Context(context.Background())
	session.evalJS = nil
	cursor := &FeedCursor{Kind: FeedPageHome}
	session.advanceFeedCursor(cursor)
	first, second := *cursor, *cursor
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := session.beginLockedOperation(ctx, false); err != nil {
		t.Fatal(err)
	}
	type result struct {
		feeds []Feed
		next  *FeedCursor
		err   error
	}
	results := make(chan result, 2)
	for _, clone := range []*FeedCursor{&first, &second} {
		go func(c *FeedCursor) {
			feeds, next, _, err := session.ListFeedsBatch(ctx, c, 1)
			results <- result{feeds: feeds, next: next, err: err}
		}(clone)
	}
	session.releaseOperation()
	successes := 0
	for range 2 {
		select {
		case got := <-results:
			if got.err == nil {
				successes++
				if len(got.feeds) != 1 || got.feeds[0].ID != "a" || got.next == nil {
					t.Fatalf("successful batch lost its result: %+v", got)
				}
			} else if errors.Is(got.err, context.DeadlineExceeded) {
				t.Fatalf("cursor contender did not finish: %v", got.err)
			}
		case <-ctx.Done():
			t.Fatal("cursor contenders failed to join")
		}
	}
	if successes != 1 || client.collections != 1 || len(session.opToken) != 1 {
		t.Fatalf("cursor forked or token leaked: successes=%d collections=%d tokens=%d", successes, client.collections, len(session.opToken))
	}
}

func TestFeedCursorRejectsWrongLivePageBeforeCollect(t *testing.T) {
	client := &currentPageURLCDPClient{response: []byte(`{"targetInfo":{"targetId":"target-1","url":"https://www.xiaohongshu.com/search_result?keyword=B"}}`)}
	pageSession := newCurrentPageURLSession(t, client)
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	session := manager.Create(nil, nil, nil)
	session.page = pageSession.page
	session.evalJS = nil
	cursor := &FeedCursor{Kind: FeedPageHome}
	session.advanceFeedCursor(cursor)
	_, next, _, err := session.ListFeedsBatch(context.Background(), cursor, 1)
	if err == nil || next != nil || client.runtimeCall != 0 || len(session.opToken) != 1 {
		t.Fatalf("wrong live route reached collection: next=%v runtime=%d err=%v", next, client.runtimeCall, err)
	}
}

func TestFeedCursorCancellationDuringFinalRefreshRemainsRetryable(t *testing.T) {
	for _, kind := range []FeedPageKind{FeedPageHome, FeedPageSearch} {
		t.Run(string(kind), func(t *testing.T) {
			client := &searchP0CDPClient{feedBatch: true, searchRoute: kind == FeedPageSearch}
			rawPage, _ := newSearchP0Element(t, client)
			manager := NewBrowseSessionManager(time.Minute)
			t.Cleanup(manager.CloseAll)
			session := manager.Create(nil, nil, nil)
			session.page = (&hrod.Page{Rod: rawPage, Keyboard: rawPage.Keyboard}).Context(context.Background())
			cursor := &FeedCursor{Kind: kind, ReturnedIDs: []string{"previous"}}
			if kind == FeedPageSearch {
				cursor.Keyword = "same"
				cursor.FilterKey = filterKeyFromFilters(nil)
			}
			session.advanceFeedCursor(cursor)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			refreshes := 0
			session.evalJS = func(refreshCtx context.Context, _ *hrod.Page, _ string) (*proto.RuntimeRemoteObject, error) {
				refreshes++
				if refreshes == 1 {
					cancel()
					return nil, refreshCtx.Err()
				}
				return nil, nil
			}
			batch := func(callCtx context.Context, c *FeedCursor) ([]Feed, *FeedCursor, bool, error) {
				if kind == FeedPageSearch {
					return session.SearchBatch(callCtx, "same", nil, c, 1)
				}
				return session.ListFeedsBatch(callCtx, c, 1)
			}
			feeds, next, _, err := batch(ctx, cursor)
			if !errors.Is(err, context.Canceled) || feeds != nil || next != nil || refreshes != 1 {
				t.Fatalf("finalize cancellation returned a batch: feeds=%v next=%v refreshes=%d err=%v", feeds, next, refreshes, err)
			}
			if cursor.position.consumed || len(cursor.ReturnedIDs) != 1 || session.nextResultIndex != 0 || len(session.opToken) != 1 {
				t.Fatal("canceled finalize consumed cursor, registered results, or leaked token")
			}
			feeds, next, more, err := batch(context.Background(), cursor)
			if err != nil || len(feeds) != 1 || feeds[0].ID != "a" || next == nil || !more || refreshes != 2 {
				t.Fatalf("retry lost batch: feeds=%v next=%v more=%t refreshes=%d err=%v", feeds, next, more, refreshes, err)
			}
			if !cursor.position.consumed || len(cursor.ReturnedIDs) != 1 || next.position == cursor.position {
				t.Fatal("commit must consume only the shared position and return a distinct successor")
			}
			if _, _, _, err := batch(context.Background(), cursor); err == nil {
				t.Fatal("committed input replayed")
			}
		})
	}
}

func TestFeedCursorFinalRefreshErrorDoesNotConsumePosition(t *testing.T) {
	for _, kind := range []FeedPageKind{FeedPageHome, FeedPageSearch} {
		for _, failure := range []error{context.DeadlineExceeded, ErrFatalRendererError} {
			t.Run(string(kind)+"/"+failure.Error(), func(t *testing.T) {
				client := &searchP0CDPClient{feedBatch: true, searchRoute: kind == FeedPageSearch}
				rawPage, _ := newSearchP0Element(t, client)
				manager := NewBrowseSessionManager(time.Minute)
				t.Cleanup(manager.CloseAll)
				session := manager.Create(nil, nil, nil)
				session.page = (&hrod.Page{Rod: rawPage, Keyboard: rawPage.Keyboard}).Context(context.Background())
				session.evalJS = func(context.Context, *hrod.Page, string) (*proto.RuntimeRemoteObject, error) {
					return nil, failure
				}
				cursor := &FeedCursor{Kind: kind}
				if kind == FeedPageSearch {
					cursor.Keyword = "same"
					cursor.FilterKey = filterKeyFromFilters(nil)
				}
				session.advanceFeedCursor(cursor)
				var next *FeedCursor
				var err error
				if kind == FeedPageSearch {
					_, next, _, err = session.SearchBatch(context.Background(), "same", nil, cursor, 1)
				} else {
					_, next, _, err = session.ListFeedsBatch(context.Background(), cursor, 1)
				}
				if !errors.Is(err, failure) || next != nil || cursor.position.consumed || len(cursor.ReturnedIDs) != 0 || session.nextResultIndex != 0 || len(session.opToken) != 1 {
					t.Fatalf("refresh failure committed batch or lost error: next=%v err=%v", next, err)
				}
			})
		}
	}
}
