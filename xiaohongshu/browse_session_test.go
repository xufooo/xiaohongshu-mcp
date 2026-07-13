package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

func TestInferXHSReadyKindFromSessionStateUsesDetailWhenNoteOpened(t *testing.T) {
	got := inferXHSReadyKindFromSessionState("https://www.xiaohongshu.com/search_result_ai?keyword=test", true, "feed-1")
	if got != XHSReadyDetail {
		t.Fatalf("opened note should use detail kind, got %s", got)
	}
}

func TestInferXHSReadyKindFromSessionStateFallsBackToURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   XHSReadyKind
	}{
		{
			name:   "search URL",
			rawURL: "https://www.xiaohongshu.com/search_result_ai?keyword=test",
			want:   XHSReadySearch,
		},
		{
			name:   "detail URL",
			rawURL: "https://www.xiaohongshu.com/explore/feed-1",
			want:   XHSReadyDetail,
		},
		{
			name:   "home URL",
			rawURL: "https://www.xiaohongshu.com/explore",
			want:   XHSReadyHome,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferXHSReadyKindFromSessionState(tt.rawURL, false, "feed-1")
			if got != tt.want {
				t.Fatalf("inferXHSReadyKindFromSessionState() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestInferXHSReadyKindFromSessionStateRequiresFeedIDForOpenedDetail(t *testing.T) {
	got := inferXHSReadyKindFromSessionState("https://www.xiaohongshu.com/search_result_ai?keyword=test", true, "")
	if got != XHSReadySearch {
		t.Fatalf("opened session without feed ID should fall back to URL, got %s", got)
	}
}

func TestBrowseSessionManagerCreateDoesNotClosePreviousSession(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	closeCount := 0
	first := manager.Create(nil, nil, func(*hrod.Page) {
		closeCount++
	})
	manager.Create(nil, nil, nil)

	if closeCount != 0 {
		t.Fatalf("previous session close count = %d, want 0", closeCount)
	}
	if _, err := manager.Get(first.ID()); err != nil {
		t.Fatalf("previous session should remain registered: %v", err)
	}
}

func TestBrowseSessionRenewExtendsExpiry(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	session := manager.Create(nil, nil, nil)

	session.mu.Lock()
	old := time.Now().Add(time.Second)
	session.expiresAt = old
	session.mu.Unlock()

	info := session.Renew()

	if !info.ExpiresAt.After(old) {
		t.Fatalf("Renew() expires_at = %s, should be after old %s", info.ExpiresAt, old)
	}
	if info.ID != session.ID() {
		t.Fatalf("Renew() ID = %s, want %s", info.ID, session.ID())
	}
}

func TestBrowseSessionSemanticResultsUseStableRefs(t *testing.T) {
	feed := Feed{
		ID:        "feed-1",
		XsecToken: "token-1",
		NoteCard: NoteCard{
			DisplayTitle: "标题一",
			User:         User{Nickname: "作者一"},
		},
	}
	session := &BrowseSession{
		results: map[string]Feed{
			"0":      feed,
			"feed-1": feed,
		},
		seenNotes: map[string]bool{"feed-1": true},
	}

	results := session.semanticResultsLocked()
	if len(results) != 1 {
		t.Fatalf("semantic result count = %d, want 1", len(results))
	}
	if results[0].Ref != "0" || results[0].FeedID != "feed-1" || results[0].Title != "标题一" || results[0].Author != "作者一" || !results[0].Seen {
		t.Fatalf("unexpected semantic result: %+v", results[0])
	}
}

func TestBrowseSessionSemanticActionsFollowState(t *testing.T) {
	session := &BrowseSession{
		sourceURL:     "https://www.xiaohongshu.com/search_result_ai?keyword=test",
		currentFeedID: "feed-1",
		opened:        true,
		read:          true,
	}

	actions := session.semanticActionsLocked(3)
	refs := make(map[string]bool, len(actions))
	labels := make(map[string]string, len(actions))
	for _, action := range actions {
		refs[action.Ref] = true
		labels[action.Ref] = action.Label
	}

	for _, ref := range []string{"session_state", "detail_current", "like_current", "comment_current", "back_to_results", "close_session"} {
		if !refs[ref] {
			t.Fatalf("missing semantic action %q in %+v", ref, actions)
		}
	}
	if refs["open_note:0"] || refs["read_current"] {
		t.Fatalf("unexpected pre-read/result actions in %+v", actions)
	}
	if labels["back_to_results"] != "关闭当前笔记面板" {
		t.Fatalf("back action label = %q, want 关闭当前笔记面板", labels["back_to_results"])
	}
}

func TestBrowseSessionAvailableActionsIncludeDetailForOpenedNote(t *testing.T) {
	session := &BrowseSession{
		currentFeedID: "feed-1",
		opened:        true,
	}

	actions := session.availableActionsLocked(0)
	found := false
	for _, action := range actions {
		if action == "session_detail" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session_detail missing from available actions: %+v", actions)
	}
}

func TestBrowseSessionDetailRequiresOpenedNote(t *testing.T) {
	session := &BrowseSession{
		opened:    false,
		timeout:   time.Minute,
		expiresAt: time.Now().Add(time.Minute),
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	session.opToken <- struct{}{}
	t.Cleanup(session.Close)

	_, err := session.Detail(context.Background(), false, 0)
	if err == nil || !strings.Contains(err.Error(), "必须先打开笔记") {
		t.Fatalf("Detail() error = %v, want 必须先打开笔记", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := session.Detail(context.Background(), false, 0)
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "必须先打开笔记") {
			t.Fatalf("Detail() after error = %v, want 必须先打开笔记", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Detail() did not return after previous error; operation lock may not be released")
	}
}

func TestBrowseSessionDetailRequiresPage(t *testing.T) {
	session := &BrowseSession{
		id:            "session-1",
		opened:        true,
		currentFeedID: "feed-1",
		timeout:       time.Minute,
		expiresAt:     time.Now().Add(time.Minute),
		opToken:       make(chan struct{}, 1),
		closedCh:      make(chan struct{}),
		seenNotes:     make(map[string]bool),
		results:       make(map[string]Feed),
	}
	session.opToken <- struct{}{}
	t.Cleanup(session.Close)

	_, err := session.Detail(context.Background(), false, 0)
	if err == nil || !strings.Contains(err.Error(), "browse session 页面不存在") {
		t.Fatalf("Detail() error = %v, want browse session 页面不存在", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := session.Detail(context.Background(), false, 0)
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "browse session 页面不存在") {
			t.Fatalf("Detail() after error = %v, want browse session 页面不存在", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Detail() did not return after previous error; operation lock may not be released")
	}
}

func TestShouldStopSessionCommentPaging(t *testing.T) {
	tests := []struct {
		name     string
		progress commentProgress
		want     bool
	}{
		{
			name:     "not at end",
			progress: commentProgress{Count: 10, Total: 30},
			want:     false,
		},
		{
			name:     "at end marker",
			progress: commentProgress{Count: 10, Total: 30, AtEnd: true},
			want:     true,
		},
		{
			name:     "total reached",
			progress: commentProgress{Count: 30, Total: 30},
			want:     true,
		},
		{
			name:     "no comments",
			progress: commentProgress{NoComments: true},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldStopSessionCommentPaging(tt.progress); got != tt.want {
				t.Fatalf("shouldStopSessionCommentPaging() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadSessionCommentsForDetailSetsPositivePagesTargetOnce(t *testing.T) {
	progressCalls := 0
	loadCalls := 0

	loadSessionCommentsForDetail(3, sessionCommentLoadOps{
		getProgress: func() (commentProgress, error) {
			progressCalls++
			return commentProgress{Count: 7, Total: 100}, nil
		},
		load: func(config CommentLoadConfig) error {
			loadCalls++
			if config.MaxCommentItems != 67 {
				t.Fatalf("MaxCommentItems = %d, want 67", config.MaxCommentItems)
			}
			return nil
		},
	})

	if loadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", loadCalls)
	}
	if progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", progressCalls)
	}
}

func TestLoadSessionCommentsForDetailDefaultsToOnePage(t *testing.T) {
	progressCalls := 0
	loadCalls := 0

	loadSessionCommentsForDetail(0, sessionCommentLoadOps{
		getProgress: func() (commentProgress, error) {
			progressCalls++
			return commentProgress{Count: 0, Total: 10}, nil
		},
		load: func(config CommentLoadConfig) error {
			loadCalls++
			if config.MaxCommentItems < 5 || config.MaxCommentItems > 10 {
				t.Fatalf("default MaxCommentItems = %d, want between 5 and 10", config.MaxCommentItems)
			}
			return nil
		},
	})

	if loadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", loadCalls)
	}
	if progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", progressCalls)
	}
}

func TestLoadSessionCommentsForDetailDoesNotReadProgressAfterLoad(t *testing.T) {
	progressCalls := 0
	loadCalls := 0

	loadSessionCommentsForDetail(3, sessionCommentLoadOps{
		getProgress: func() (commentProgress, error) {
			progressCalls++
			return commentProgress{Count: 20, Total: 100}, nil
		},
		load: func(CommentLoadConfig) error {
			loadCalls++
			return nil
		},
	})

	if loadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", loadCalls)
	}
	if progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", progressCalls)
	}
}

func TestLoadSessionCommentsForDetailLoadsNegativePagesWithoutLimit(t *testing.T) {
	progressCalls := 0
	loadCalls := 0

	loadSessionCommentsForDetail(-1, sessionCommentLoadOps{
		getProgress: func() (commentProgress, error) {
			progressCalls++
			return commentProgress{Count: 15, Total: 100}, nil
		},
		load: func(config CommentLoadConfig) error {
			loadCalls++
			if config.MaxCommentItems != 0 {
				t.Fatalf("MaxCommentItems = %d, want 0", config.MaxCommentItems)
			}
			return nil
		},
	})

	if loadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", loadCalls)
	}
	if progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", progressCalls)
	}
}

func TestLoadSessionCommentsForDetailOverridesFallbackConfigForPositivePages(t *testing.T) {
	progressCalls := 0
	loadCalls := 0

	loadSessionCommentsForDetail(2, sessionCommentLoadOps{
		getProgress: func() (commentProgress, error) {
			progressCalls++
			return commentProgress{}, errors.New("progress unavailable")
		},
		load: func(config CommentLoadConfig) error {
			loadCalls++
			if config.MaxCommentItems != 40 {
				t.Fatalf("MaxCommentItems = %d, want 40", config.MaxCommentItems)
			}
			return nil
		},
	})

	if loadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", loadCalls)
	}
	if progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", progressCalls)
	}
}

func TestBrowseSessionRecommendedActionChoosesFirstUnseenResult(t *testing.T) {
	seenFeed := Feed{
		ID: "feed-seen",
		NoteCard: NoteCard{
			DisplayTitle: "已看过",
		},
	}
	unseenFeed := Feed{
		ID: "feed-unseen",
		NoteCard: NoteCard{
			DisplayTitle: "没看过",
		},
	}
	session := &BrowseSession{
		results: map[string]Feed{
			"0": seenFeed,
			"1": unseenFeed,
		},
		seenNotes: map[string]bool{"feed-seen": true},
	}

	action := session.recommendedActionLocked(true, session.semanticResultsLocked())
	if action == nil {
		t.Fatal("recommended action is nil")
	}
	if action.Tool != "session_open_note" || action.ResultRef != "1" || action.FeedID != "feed-unseen" {
		t.Fatalf("recommended action = %+v, want open_note result_ref=1 feed-unseen", action)
	}
	if action.Label != "打开下一张未读笔记" {
		t.Fatalf("recommended label = %q, want 打开下一张未读笔记", action.Label)
	}
}

func TestBrowseSessionRecommendedActionAvoidsWriteAfterRead(t *testing.T) {
	session := &BrowseSession{
		currentFeedID: "feed-1",
		opened:        true,
		read:          true,
	}

	action := session.recommendedActionLocked(true, nil)
	if action == nil {
		t.Fatal("recommended action is nil")
	}
	if action.Tool != "session_back" || action.Ref != "back_to_results" {
		t.Fatalf("recommended action = %+v, want session_back", action)
	}
	if action.Label != "关闭当前笔记面板" {
		t.Fatalf("recommended label = %q, want 关闭当前笔记面板", action.Label)
	}
}

func TestOpenedSessionBackActionDoesNotRequireSourceURL(t *testing.T) {
	session := &BrowseSession{
		currentFeedID: "feed-1",
		opened:        true,
		read:          true,
	}

	available := session.availableActionsLocked(0)
	found := false
	for _, action := range available {
		if action == "session_back" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session_back missing from available actions without sourceURL: %+v", available)
	}

	semantic := session.semanticActionsLocked(0)
	found = false
	for _, action := range semantic {
		if action.Tool == "session_back" && action.Label == "关闭当前笔记面板" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session_back missing from semantic actions without sourceURL: %+v", semantic)
	}
}

func TestBackUsesOverlayCloseStrategy(t *testing.T) {
	data, err := os.ReadFile("note_close.go")
	if err != nil {
		t.Fatalf("read note_close.go: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		`const noteCloseProbeDelay = 500 * time.Millisecond`,
		`page.Keyboard.Press(input.Escape)`,
		`document.querySelector('.note-container')`,
		`page.Eval`,
		`page.Navigate(sourceURL)`,
		`WaitForXHSReady(page, XHSReadyOptions{Kind: inferXHSReadyKindFromURL(sourceURL)})`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("note_close.go missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`type noteCloseProbe struct`,
		`isOnXHSDomain`,
		`page.Mouse.MoveTo`,
		`proto.Point`,
		`history.back()`,
		`.note-detail-mask`,
		`mask.Click`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("note_close.go must not contain %q", forbidden)
		}
	}
	if lines := strings.Count(strings.TrimRight(source, "\n"), "\n") + 1; lines > 80 {
		t.Fatalf("note_close.go has %d lines, want <= 80", lines)
	}
}

func TestBrowseSessionSummaryIncludesRecommendedAction(t *testing.T) {
	current := BrowseSessionCurrent{
		NextHint: "可用 session_open_note 打开 results 中的 result_ref",
	}
	action := &BrowseSessionAction{
		Tool:      "session_open_note",
		ResultRef: "1",
		FeedID:    "feed-1",
	}

	summary := browseSessionSummary(XHSReadySearch, true, 3, 1, current, action)
	wantParts := []string{
		"当前: search ready=true results=3 seen=1",
		"下一步: 可用 session_open_note 打开 results 中的 result_ref",
		"推荐: session_open_note result_ref=1 feed_id=feed-1",
	}
	for _, part := range wantParts {
		if !strings.Contains(summary, part) {
			t.Fatalf("summary %q missing %q", summary, part)
		}
	}
}

func TestBrowseSessionTimelineKeepsRecentEntries(t *testing.T) {
	session := &BrowseSession{}
	for i := 0; i < maxBrowseSessionTimelineEntries+2; i++ {
		session.recordTimelineLocked("action", "target", "ok", time.Unix(int64(i), 0), "note")
	}

	if len(session.timeline) != maxBrowseSessionTimelineEntries {
		t.Fatalf("timeline count = %d, want %d", len(session.timeline), maxBrowseSessionTimelineEntries)
	}
	if session.timeline[0].At.Unix() != 2 {
		t.Fatalf("oldest retained timeline entry = %d, want 2", session.timeline[0].At.Unix())
	}
}

func TestBrowseSessionTTLReturnsPromptlyAndCancelsActiveOperation(t *testing.T) {
	session := &BrowseSession{
		id:        "test-ttl",
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		timeout:   time.Minute,
		expiresAt: time.Now().Add(time.Minute),
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	session.opToken <- struct{}{}
	t.Cleanup(session.Close)

	tokenHeld := make(chan struct{})
	opCancelled := make(chan struct{})
	opDone := make(chan struct{})

	go func() {
		defer close(opDone)
		opCtx, err := session.beginLockedOperation(context.Background(), true)
		if err != nil {
			t.Errorf("beginLockedOperation: %v", err)
			close(tokenHeld)
			return
		}
		close(tokenHeld)
		<-opCtx.Done()
		close(opCancelled)
		session.finishOperation()
	}()
	select {
	case <-tokenHeld:
	case <-time.After(time.Second):
		t.Fatal("等待 tokenHeld 超时")
	}

	// 手动触发过期，而非依赖 timer 时序
	session.mu.Lock()
	if session.timer != nil {
		session.timer.Stop()
	}
	session.expiresAt = time.Now().Add(-time.Second)
	expiresAt := session.expiresAt
	session.mu.Unlock()
	session.closeExpired(expiresAt)

	select {
	case <-opCancelled:
	case <-time.After(time.Second):
		t.Fatal("过期应取消活跃操作")
	}
	if !session.closed {
		t.Fatal("过期后 session 应已关闭")
	}

	select {
	case <-opDone:
	case <-time.After(time.Second):
		t.Fatal("goroutine 未退出")
	}
}

func TestBrowseSessionDoesNotClosePageConcurrentlyWithOperation(t *testing.T) {
	pageClosed := make(chan struct{})
	session := &BrowseSession{
		id:        "test-page-close",
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		timeout:   time.Minute,
		expiresAt: time.Now().Add(time.Minute),
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
		onClose:   func(p *hrod.Page) { close(pageClosed) },
	}
	session.opToken <- struct{}{}
	t.Cleanup(session.Close)

	tokenHeld := make(chan struct{})
	allowFinish := make(chan struct{})
	var allowOnce sync.Once
	cleanupAllow := func() { allowOnce.Do(func() { close(allowFinish) }) }
	t.Cleanup(cleanupAllow)

	opDone := make(chan struct{})
	go func() {
		defer close(opDone)
		if _, err := session.beginLockedOperation(context.Background(), true); err != nil {
			t.Errorf("beginLockedOperation: %v", err)
			close(tokenHeld)
			cleanupAllow()
			return
		}
		close(tokenHeld)
		<-allowFinish
		session.finishOperation()
	}()
	select {
	case <-tokenHeld:
	case <-time.After(time.Second):
		t.Fatal("等待 tokenHeld 超时")
	}

	session.Close()

	select {
	case <-pageClosed:
		t.Fatal("操作持 token 时不应释放 page")
	default:
	}

	cleanupAllow()

	select {
	case <-pageClosed:
	case <-time.After(time.Second):
		t.Fatal("操作结束后应释放 page")
	}

	select {
	case <-opDone:
	case <-time.After(time.Second):
		t.Fatal("goroutine 未退出")
	}
}

func TestCreateInitialRefreshCancelledByClose(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)

	evalBlocked := make(chan struct{})
	pageClosed := make(chan struct{})

	var once sync.Once
	evalJS := func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
		once.Do(func() { close(evalBlocked) })
		<-ctx.Done()
		return nil, ctx.Err()
	}

	sessionCh := make(chan *BrowseSession, 1)
	done := make(chan struct{})
	go func() {
		s := manager.create(&hrod.Page{}, nil, func(p *hrod.Page) { close(pageClosed) }, func(session *BrowseSession) {
			session.evalJS = evalJS
		})
		sessionCh <- s
		close(done)
	}()

	select {
	case <-evalBlocked:
	case <-done:
		t.Fatal("Create 不应在 eval 返回前完成")
	case <-time.After(time.Second):
		t.Fatal("等待 initial Eval 启动超时")
	}

	manager.CloseAll()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("initial refresh 应在 Close 后退出")
	}

	select {
	case <-pageClosed:
	case <-time.After(time.Second):
		t.Fatal("onClose 应被调用")
	}

	s := <-sessionCh
	if _, err := manager.Get(s.ID()); err == nil {
		t.Fatal("已关闭的 session 不应在 manager map 中")
	}
}

func TestExpiredLongOperationCannotRenewSessionOnFinish(t *testing.T) {
	onCloseCount := 0
	manager := NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	session := manager.Create(nil, nil, func(*hrod.Page) {
		onCloseCount++
	})

	if _, err := session.beginLockedOperation(context.Background(), true); err != nil {
		t.Fatalf("beginLockedOperation: %v", err)
	}
	session.mu.Lock()
	session.expiresAt = time.Now().Add(-time.Second)
	if session.timer != nil {
		session.timer.Stop()
	}
	session.mu.Unlock()

	session.finishOperation()
	if !session.closed {
		t.Fatal("过期操作完成后 session 应已关闭")
	}
	if _, err := manager.Get(session.ID()); err == nil {
		t.Fatal("过期 session 应从 manager map 中移除")
	}
	if onCloseCount != 1 {
		t.Fatalf("onClose 调用次数 = %d，期望 1", onCloseCount)
	}
}

func TestCanceledQueuedOperationCannotRenewSession(t *testing.T) {
	session := &BrowseSession{
		id:        "test-cancel-queue",
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		timeout:   time.Minute,
		expiresAt: time.Now().Add(time.Minute),
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	session.opToken <- struct{}{}
	t.Cleanup(session.Close)

	tokenHeld := make(chan struct{})
	releaseToken := make(chan struct{})
	var releaseOnce sync.Once
	cleanupRelease := func() { releaseOnce.Do(func() { close(releaseToken) }) }
	t.Cleanup(cleanupRelease)

	opDone := make(chan struct{})
	go func() {
		defer close(opDone)
		if _, err := session.beginLockedOperation(context.Background(), true); err != nil {
			t.Errorf("beginLockedOperation: %v", err)
			close(tokenHeld)
			return
		}
		close(tokenHeld)
		select {
		case <-releaseToken:
		case <-time.After(time.Second):
			t.Errorf("等待 releaseToken 超时")
			session.finishOperation()
			return
		}
		session.finishOperation()
	}()
	select {
	case <-tokenHeld:
	case <-time.After(time.Second):
		t.Fatal("等待 tokenHeld 超时")
	}

	savedExpiresAt := session.expiresAt
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := session.beginLockedOperation(ctx, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消排队操作应返回 context.Canceled，得到 %v", err)
	}
	if !session.expiresAt.Equal(savedExpiresAt) {
		t.Fatalf("取消排队操作不应修改 expiresAt，原 %v 现 %v", savedExpiresAt, session.expiresAt)
	}
	if session.closed {
		t.Fatal("取消的排队操作不应关闭 session")
	}
	cleanupRelease()

	select {
	case <-opDone:
	case <-time.After(time.Second):
		t.Fatal("第一个操作 goroutine 未退出")
	}
}

func TestFinishOperationSkipsRefreshAfterCancellation(t *testing.T) {
	evalCount := 0
	session := newTestSession()
	session.evalJS = func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
		evalCount++
		return nil, nil
	}
	session.page = &hrod.Page{}
	t.Cleanup(session.Close)

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := session.beginLockedOperation(ctx, true); err != nil {
		t.Fatalf("beginLockedOperation: %v", err)
	}
	cancel()
	session.finishOperation()

	if evalCount != 0 {
		t.Fatalf("取消后不应有 eval，得到 %d 次调用", evalCount)
	}
}

// P1 测试: finishOperation 正常关闭 opCtx.Done

func TestFinishOperationCancelsOperationContext(t *testing.T) {
	session := newTestSession()
	t.Cleanup(session.Close)

	opCtx, err := session.beginLockedOperation(context.Background(), true)
	if err != nil {
		t.Fatalf("beginLockedOperation: %v", err)
	}

	session.finishOperation()

	select {
	case <-opCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("finishOperation 后 opCtx.Done 应关闭")
	}

	// token 可再次获取
	if _, err := session.beginLockedOperation(context.Background(), true); err != nil {
		t.Fatalf("finishOperation 后 token 可再次获取: %v", err)
	}
	session.finishOperation()
}

// P1 测试: 结束阶段无界 CDP 调用

func TestRefreshPageStateHasBoundedContext(t *testing.T) {
	var mu sync.Mutex
	evalCount := 0
	deadlineOK := 0
	evalsDone := make(chan struct{})

	s := &BrowseSession{
		closed:    false,
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
		page:      &hrod.Page{},
		evalJS: func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
			mu.Lock()
			evalCount++
			deadline, hasDeadline := ctx.Deadline()
			if hasDeadline && deadline.Sub(time.Now()) <= browseSessionRefreshTimeout+100*time.Millisecond {
				deadlineOK++
			}
			if evalCount == 2 {
				close(evalsDone)
			}
			mu.Unlock()
			return nil, context.DeadlineExceeded
		},
	}

	done := make(chan struct{})
	go func() {
		s.refreshPageState(context.Background())
		close(done)
	}()

	select {
	case <-evalsDone:
	case <-time.After(time.Second):
		t.Fatal("两次 eval 未被全部调用")
	}

	mu.Lock()
	if evalCount != 2 {
		mu.Unlock()
		t.Fatalf("eval 调用次数 = %d，期望 2", evalCount)
	}
	if deadlineOK != 2 {
		mu.Unlock()
		t.Fatalf("带有效 deadline 的 eval 次数 = %d，期望 2", deadlineOK)
	}
	mu.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refreshPageState 应在 bounded timeout 后返回")
	}
}

// P2 测试: 取消错误传播

func TestSessionCommentLoadPropagatesParentCancellation(t *testing.T) {
	t.Run("context.Canceled 传播", func(t *testing.T) {
		err := loadSessionCommentsForDetail(5, sessionCommentLoadOps{
			getProgress: func() (commentProgress, error) {
				return commentProgress{}, nil
			},
			load: func(config CommentLoadConfig) error {
				return context.Canceled
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应传播 context.Canceled，得到 %v", err)
		}
	})
	t.Run("context.DeadlineExceeded 传播", func(t *testing.T) {
		err := loadSessionCommentsForDetail(5, sessionCommentLoadOps{
			getProgress: func() (commentProgress, error) {
				return commentProgress{}, nil
			},
			load: func(config CommentLoadConfig) error {
				return context.DeadlineExceeded
			},
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("应传播 context.DeadlineExceeded，得到 %v", err)
		}
	})
	t.Run("普通 DOM 错误仍被吞掉", func(t *testing.T) {
		err := loadSessionCommentsForDetail(5, sessionCommentLoadOps{
			getProgress: func() (commentProgress, error) {
				return commentProgress{}, nil
			},
			load: func(config CommentLoadConfig) error {
				return fmt.Errorf("普通 DOM 错误")
			},
		})
		if err != nil {
			t.Fatalf("普通 DOM 错误应被吞掉，得到 %v", err)
		}
	})
	t.Run("getProgress Canceled 立即返回", func(t *testing.T) {
		err := loadSessionCommentsForDetail(5, sessionCommentLoadOps{
			getProgress: func() (commentProgress, error) {
				return commentProgress{}, context.Canceled
			},
			load: func(config CommentLoadConfig) error {
				t.Fatal("getProgress 返回 Canceled 后不应调用 load")
				return nil
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应返回 context.Canceled，得到 %v", err)
		}
	})
	t.Run("getProgress DeadlineExceeded 立即返回", func(t *testing.T) {
		err := loadSessionCommentsForDetail(5, sessionCommentLoadOps{
			getProgress: func() (commentProgress, error) {
				return commentProgress{}, context.DeadlineExceeded
			},
			load: func(config CommentLoadConfig) error {
				t.Fatal("getProgress 返回 DeadlineExceeded 后不应调用 load")
				return nil
			},
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("应返回 context.DeadlineExceeded，得到 %v", err)
		}
	})
}

func TestManagerCloseRemovesSessionWithoutBlockingOnActiveOperation(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	t.Cleanup(manager.CloseAll)

	tokenHeld := make(chan struct{})
	opDone := make(chan struct{})
	go func() {
		defer close(opDone)
		opCtx, err := session.beginLockedOperation(context.Background(), true)
		if err != nil {
			t.Errorf("beginLockedOperation: %v", err)
			close(tokenHeld)
			return
		}
		close(tokenHeld)
		<-opCtx.Done()
		session.finishOperation()
	}()
	select {
	case <-tokenHeld:
	case <-time.After(time.Second):
		t.Fatal("等待 tokenHeld 超时")
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Close(session.ID())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Close 不应阻塞且应成功: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Manager.Close 不应在活跃操作上阻塞")
	}

	select {
	case <-opDone:
	case <-time.After(time.Second):
		t.Fatal("goroutine 未退出")
	}
}

// P2 测试: ClassifyRiskContext 不续 TTL

func TestClassifyRiskContextDoesNotRenewTTL(t *testing.T) {
	expiresAt := time.Now().Add(time.Minute)
	session := &BrowseSession{
		id:        "test-classify-ttl",
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		timeout:   time.Minute,
		expiresAt: expiresAt,
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	session.opToken <- struct{}{}
	t.Cleanup(session.Close)

	sig, err := session.ClassifyRiskContext(context.Background())
	if err != nil {
		t.Fatalf("ClassifyRiskContext: %v", err)
	}
	if sig.Kind != RiskNone {
		t.Fatalf("期望 RiskNone，得到 %v", sig.Kind)
	}
	if !session.expiresAt.Equal(expiresAt) {
		t.Fatal("ClassifyRiskContext 不应续 TTL")
	}
}

// P2 测试: token 就绪 + ctx 取消不丢失 token

func TestBeginLockedOperationTokenReadyAndCtxCancelled(t *testing.T) {
	expiresAt := time.Now().Add(time.Minute)
	session := &BrowseSession{
		id:        "test-token-ready-cancelled",
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		timeout:   time.Minute,
		expiresAt: expiresAt,
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	session.opToken <- struct{}{}
	t.Cleanup(session.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := session.beginLockedOperation(ctx, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("beginLockedOperation 应返回 context.Canceled，得到 %v", err)
	}
	if !session.expiresAt.Equal(expiresAt) {
		t.Fatal("失败后不应续 TTL")
	}

	// token 未被丢失，可再次获取
	if _, err := session.beginLockedOperation(context.Background(), true); err != nil {
		t.Fatalf("token 丢失: %v", err)
	}
	session.finishOperation()
}

func newTestSession() *BrowseSession {
	s := &BrowseSession{
		id:        "test-session",
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		timeout:   time.Minute,
		expiresAt: time.Now().Add(time.Minute),
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	s.opToken <- struct{}{}
	return s
}
