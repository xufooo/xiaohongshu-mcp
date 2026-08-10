package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
	xerrors "github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

func TestMergeOpenedNoteUserFromSearchResult(t *testing.T) {
	tests := []struct {
		name    string
		content OpenedNoteContent
		feed    Feed
		wantUID string
		wantTok string
	}{
		{
			name:    "两个字段都空则补齐",
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "u1", XsecToken: "t1"}}},
			wantUID: "u1",
			wantTok: "t1",
		},
		{
			name:    "两个字段都非空则不覆盖",
			content: OpenedNoteContent{User: User{UserID: "detail-u", XsecToken: "detail-t"}},
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "search-u", XsecToken: "search-t"}}},
			wantUID: "detail-u",
			wantTok: "detail-t",
		},
		{
			name:    "仅 userId 空则只补 userId",
			content: OpenedNoteContent{User: User{XsecToken: "detail-t"}},
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "search-u", XsecToken: "search-t"}}},
			wantUID: "search-u",
			wantTok: "detail-t",
		},
		{
			name:    "仅 xsecToken 空则只补 token",
			content: OpenedNoteContent{User: User{UserID: "detail-u"}},
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "search-u", XsecToken: "search-t"}}},
			wantUID: "detail-u",
			wantTok: "search-t",
		},
		{
			name:    "搜索结果字段为空则保持空",
			content: OpenedNoteContent{User: User{UserID: "detail-u"}},
			feed:    Feed{NoteCard: NoteCard{User: User{}}},
			wantUID: "detail-u",
			wantTok: "",
		},
		{
			name:    "搜索结果只提供 token 时 userId 保持空",
			feed:    Feed{NoteCard: NoteCard{User: User{XsecToken: "search-t"}}},
			wantUID: "",
			wantTok: "search-t",
		},
		{
			name:    "搜索结果无 token 时结果保持空而非笔记 token",
			feed:    Feed{XsecToken: "note-token", NoteCard: NoteCard{User: User{UserID: "search-u"}}},
			wantUID: "search-u",
			wantTok: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := tt.content
			mergeOpenedNoteUserFromSearchResult(&content, tt.feed)
			if content.User.UserID != tt.wantUID {
				t.Errorf("UserID = %q, want %q", content.User.UserID, tt.wantUID)
			}
			if content.User.XsecToken != tt.wantTok {
				t.Errorf("XsecToken = %q, want %q", content.User.XsecToken, tt.wantTok)
			}
		})
	}
}

func TestMergeOpenedNoteUserDoesNotUseNoteToken(t *testing.T) {
	content := OpenedNoteContent{User: User{}}
	feed := Feed{XsecToken: "note-token", NoteCard: NoteCard{User: User{UserID: "u", XsecToken: "author-token"}}}
	mergeOpenedNoteUserFromSearchResult(&content, feed)
	if content.User.XsecToken != "author-token" {
		t.Errorf("XsecToken = %q, want author token", content.User.XsecToken)
	}
	if content.User.XsecToken == "note-token" {
		t.Errorf("XsecToken 不得冒充笔记 token")
	}
}

func TestMergeOpenedNoteUserFromSearchResultNilContent(t *testing.T) {
	mergeOpenedNoteUserFromSearchResult(nil, Feed{NoteCard: NoteCard{User: User{UserID: "u", XsecToken: "t"}}})
}

func TestEvalTimeoutCounterConsecutiveTimeouts(t *testing.T) {
	counter := &evalTimeoutCounter{}
	timeoutErr := context.DeadlineExceeded
	probeCalls := 0
	probeTimeout := func() error {
		probeCalls++
		return context.DeadlineExceeded
	}
	if err := counter.add(context.Background(), timeoutErr, probeTimeout); IsFatalRendererError(err) {
		t.Fatal("首次 timeout 不应 fatal")
	}
	if err := counter.add(context.Background(), timeoutErr, probeTimeout); IsFatalRendererError(err) {
		t.Fatal("连续第二次 timeout 不应 fatal")
	}
	if err := counter.add(context.Background(), timeoutErr, probeTimeout); IsFatalRendererError(err) {
		t.Fatalf("probe timeout 但无确认失效证据不应 fatal: %v", err)
	}
	if probeCalls != 1 {
		t.Fatalf("第三次 timeout 应执行一次 probe，实际 %d", probeCalls)
	}
	if counter.timeouts != 0 {
		t.Fatalf("probe 无法确认失效后应清零计数: %d", counter.timeouts)
	}
	counter = &evalTimeoutCounter{}
	_ = counter.add(context.Background(), timeoutErr, probeTimeout)
	_ = counter.add(context.Background(), nil, probeTimeout)
	if err := counter.add(context.Background(), timeoutErr, probeTimeout); IsFatalRendererError(err) {
		t.Fatal("成功 Eval 后应清零连续 timeout")
	}
}

func TestEvalTimeoutCounterProbeSuccessResetsCounter(t *testing.T) {
	counter := &evalTimeoutCounter{}
	timeoutErr := context.DeadlineExceeded
	probeCalls := 0
	probeSuccess := func() error {
		probeCalls++
		return nil
	}
	_ = counter.add(context.Background(), timeoutErr, probeSuccess)
	_ = counter.add(context.Background(), timeoutErr, probeSuccess)
	if err := counter.add(context.Background(), timeoutErr, probeSuccess); IsFatalRendererError(err) {
		t.Fatalf("probe 成功不应 fatal: %v", err)
	}
	if probeCalls != 1 || counter.timeouts != 0 {
		t.Fatalf("probe 成功应执行一次并清零 counter: calls=%d timeouts=%d", probeCalls, counter.timeouts)
	}
	if err := counter.add(context.Background(), timeoutErr, probeSuccess); IsFatalRendererError(err) {
		t.Fatal("probe 成功清零后下一次 timeout 应重新计数")
	}
}

func TestEvalTimeoutCounterProbeFatalSentinelIsFatal(t *testing.T) {
	counter := &evalTimeoutCounter{}
	timeoutErr := context.DeadlineExceeded
	fatalErr := fmt.Errorf("%w: browser health: %v", ErrFatalRendererError, errors.New("cdp disconnected"))
	_ = counter.add(context.Background(), timeoutErr, func() error { return nil })
	_ = counter.add(context.Background(), timeoutErr, func() error { return nil })
	err := counter.add(context.Background(), timeoutErr, func() error { return fatalErr })
	if !IsFatalRendererError(err) {
		t.Fatalf("probe 返回 fatal 哨兵应 fatal: %v", err)
	}
	if !strings.Contains(err.Error(), "browser health") {
		t.Fatalf("fatal 错误应包含 probe 详情: %v", err)
	}
}

func TestEvalTimeoutCounterProbePlainErrorIsTolerated(t *testing.T) {
	counter := &evalTimeoutCounter{}
	timeoutErr := context.DeadlineExceeded
	probeErr := errors.New("renderer connection closed")
	_ = counter.add(context.Background(), timeoutErr, func() error { return nil })
	_ = counter.add(context.Background(), timeoutErr, func() error { return nil })
	err := counter.add(context.Background(), timeoutErr, func() error { return probeErr })
	if IsFatalRendererError(err) {
		t.Fatalf("无法确认失效的 probe 错误不应 fatal: %v", err)
	}
	if !errors.Is(err, timeoutErr) {
		t.Fatalf("应返回原业务 timeout: %v", err)
	}
	if counter.timeouts != 0 {
		t.Fatalf("probe 普通错误后应清零计数: %d", counter.timeouts)
	}
}

func TestEvalTimeoutCounterParentContextCancellationResetsCounter(t *testing.T) {
	counter := &evalTimeoutCounter{timeouts: 2}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probeCalls := 0
	err := counter.add(ctx, context.DeadlineExceeded, func() error {
		probeCalls++
		return context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("父 context 取消后应返回原错误: %v", err)
	}
	if counter.timeouts != 0 || probeCalls != 0 {
		t.Fatalf("父 context 取消后应清零且不 probe: timeouts=%d calls=%d", counter.timeouts, probeCalls)
	}
}

func TestIsConfirmedRendererDead(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "io.EOF", err: io.EOF, want: true},
		{name: "io.ErrUnexpectedEOF", err: io.ErrUnexpectedEOF, want: true},
		{name: "net.ErrClosed", err: net.ErrClosed, want: true},
		{name: "connection closed", err: errors.New("rpc: connection closed"), want: true},
		{name: "connection reset", err: errors.New("connection reset by peer"), want: true},
		{name: "target closed", err: errors.New("target closed"), want: true},
		{name: "target crashed", err: errors.New("target crashed"), want: true},
		{name: "session closed", err: errors.New("session closed"), want: true},
		{name: "browser closed", err: errors.New("browser has been closed"), want: true},
		{name: "websocket close", err: errors.New("websocket: close 1006"), want: true},
		{name: "broken pipe", err: errors.New("write: broken pipe"), want: true},
		{name: "cdp closed", err: errors.New("cdp: closed"), want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "普通 CDP 错误", err: errors.New("protocol error: some rpc failed"), want: false},
		{name: "上下文销毁", err: errors.New("Execution context was destroyed"), want: false},
		{name: "websocket 普通错误", err: errors.New("websocket bad handshake"), want: false},
	}
	for _, tc := range cases {
		if got := isConfirmedRendererDead(tc.err); got != tc.want {
			t.Errorf("%s: isConfirmedRendererDead(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestClassifyProbeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		fatal bool
	}{
		{name: "nil", err: nil, fatal: false},
		{name: "io.EOF 直接断连", err: io.EOF, fatal: true},
		{name: "net.ErrClosed 直接断连", err: net.ErrClosed, fatal: true},
		{name: "wrapped EOF", err: fmt.Errorf("eval: %w", io.EOF), fatal: true},
		{name: "connection reset", err: errors.New("connection reset by peer"), fatal: true},
		{name: "普通 CDP 错误容忍", err: errors.New("protocol error: some rpc failed"), fatal: false},
		{name: "上下文销毁容忍", err: errors.New("Execution context was destroyed"), fatal: false},
		{name: "websocket handshake 容忍", err: errors.New("websocket bad handshake"), fatal: false},
	}
	for _, tc := range cases {
		err := classifyProbeError(tc.err)
		if got := IsFatalRendererError(err); got != tc.fatal {
			t.Errorf("%s: classifyProbeError(%v) fatal=%v, want %v (err=%v)", tc.name, tc.err, got, tc.fatal, err)
		}
	}
}

func TestBrowseSessionCloseReleasesActivePageOnce(t *testing.T) {
	manager := NewBrowseSessionManager(time.Minute)
	var releases atomic.Int32
	session := manager.Create(nil, nil, func(*hrod.Page) {
		releases.Add(1)
	})
	opCtx, err := session.beginLockedOperation(context.Background(), true)
	if err != nil {
		t.Fatalf("beginLockedOperation: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.Close()
		}()
	}
	wg.Wait()
	if releases.Load() != 1 {
		t.Fatalf("Close 应立即释放一次，实际 %d", releases.Load())
	}
	session.finishOperation(opCtx.Err())
	session.Close()
	if releases.Load() != 1 {
		t.Fatalf("operation 收尾或重复 Close 不得二次释放，实际 %d", releases.Load())
	}
	if _, err := manager.Get(session.ID()); err == nil {
		t.Fatal("关闭后 session 应从 manager 移除")
	}
}

func TestFinishOperationSkipsRefreshAndTTLOnFatal(t *testing.T) {
	page := &hrod.Page{}
	opCtx, cancel := context.WithCancel(context.Background())
	expiresAt := time.Now().Add(time.Minute)
	var evals atomic.Int32
	session := &BrowseSession{
		opToken:       make(chan struct{}, 1),
		closedCh:      make(chan struct{}),
		opCtx:         opCtx,
		activeOp:      cancel,
		page:          page,
		touchOnFinish: true,
		expiresAt:     expiresAt,
		timeout:       time.Minute,
		evalJS: func(context.Context, *hrod.Page, string) (*proto.RuntimeRemoteObject, error) {
			evals.Add(1)
			return nil, nil
		},
	}
	session.finishOperation(fmt.Errorf("wrapped: %w", ErrFatalRendererError))
	if evals.Load() != 0 {
		t.Fatalf("fatal 收尾不得刷新页面，实际 Eval %d", evals.Load())
	}
	if !session.expiresAt.Equal(expiresAt) {
		t.Fatal("fatal 收尾不得刷新 TTL")
	}
}

func TestFinishOperationKeepsDeadlineNonFatal(t *testing.T) {
	page := &hrod.Page{}
	expiresAt := time.Now().Add(time.Minute)
	var evals atomic.Int32
	session := &BrowseSession{
		opToken:       make(chan struct{}, 1),
		closedCh:      make(chan struct{}),
		opCtx:         context.Background(),
		activeOp:      func() {},
		page:          page,
		touchOnFinish: true,
		expiresAt:     expiresAt,
		timeout:       time.Minute,
		evalJS: func(context.Context, *hrod.Page, string) (*proto.RuntimeRemoteObject, error) {
			evals.Add(1)
			return nil, context.DeadlineExceeded
		},
	}
	session.finishOperation(fmt.Errorf("wrapped: %w", context.DeadlineExceeded))
	if evals.Load() != 1 {
		t.Fatalf("普通 deadline 收尾应尝试刷新页面状态，实际 Eval %d", evals.Load())
	}
	if !session.expiresAt.After(expiresAt) {
		t.Fatal("普通 deadline 收尾应保留 session 并刷新 TTL")
	}
}

func TestOpenNoteAtomicCommit(t *testing.T) {
	session := &BrowseSession{
		seenNotes:         map[string]bool{"old": true},
		initialCommentIDs: []string{"old-comment"},
		notification:      browseNotificationState{active: true},
	}
	feed := Feed{ID: "feed-1", XsecToken: "token-1"}
	info := session.commitOpenedNote(feed, "https://example.test/search", "ref-1", []string{"c1", "c2"})
	if !info.Opened || !info.Read || !info.SeenNotes[feed.ID] {
		t.Fatalf("原子提交状态错误: %+v", info)
	}
	if got := session.GetInitialCommentIDs(); len(got) != 2 || got[0] != "c1" {
		t.Fatalf("原子提交评论 cursor 错误: %v", got)
	}
	if session.notification.active {
		t.Fatal("原子提交应重置通知 surface")
	}
	if len(session.timeline) != 2 || session.timeline[0].Action != "open_note" || session.timeline[1].Action != "read_note" {
		t.Fatalf("原子提交 timeline 错误: %+v", session.timeline)
	}
	stageTools := strings.Join(session.availableActionsLocked(1), ",")
	for _, tool := range []string{"get_note_detail", "like_feed", "favorite_feed", "comment_feed", "reply_comment_in_feed"} {
		if !strings.Contains(stageTools, tool) {
			t.Fatalf("原子提交应暴露工具 %s: %s", tool, stageTools)
		}
	}
}

func TestLivePageKindPrefersVisibleDetail(t *testing.T) {
	probe := xhsReadyProbe{
		URL:               "https://www.xiaohongshu.com/search_result?keyword=%E4%B8%89%E4%BA%9A",
		VisibleDetailCount: 1,
	}
	if kind := inferLivePageKind(probe, true); kind != XHSReadyDetail {
		t.Fatalf("搜索 URL + 详情弹层可见时应判 detail: %s", kind)
	}
	probe.VisibleDetailCount = 0
	if kind := inferLivePageKind(probe, false); kind != XHSReadySearch {
		t.Fatalf("详情弹层消失后应判 search: %s", kind)
	}
}

func TestSessionResultsCapacityAndBounds(t *testing.T) {
	feeds := make([]Feed, maxBrowseSessionResults+25)
	for i := range feeds {
		feeds[i].ID = fmt.Sprintf("feed-%d", i)
	}
	results, next := replaceSessionResults(feeds)
	if next != maxBrowseSessionResults {
		t.Fatalf("nextResultIndex=%d, want %d", next, maxBrowseSessionResults)
	}
	session := &BrowseSession{results: results, nextResultIndex: next}
	if _, err := session.resolveResult(strconv.Itoa(maxBrowseSessionResults)); err == nil || !strings.Contains(err.Error(), "索引越界") {
		t.Fatalf("越界 result_ref 应返回明确错误: %v", err)
	}
	more := []Feed{{ID: "overflow"}}
	results, next, registered := appendSessionResults(results, next, more)
	if next != maxBrowseSessionResults {
		t.Fatalf("容量满后 nextResultIndex 不应增长: %d", next)
	}
	if len(registered) != 0 {
		t.Fatalf("容量满后不得返回未登记 feed: %+v", registered)
	}
	if _, ok := results["overflow"]; ok {
		t.Fatal("容量满后不得继续缓存 feed ID")
	}
}

func TestAppendSessionResultsReturnsOnlyResolvableFeedsNearCapacity(t *testing.T) {
	initial := make([]Feed, maxBrowseSessionResults-2)
	for i := range initial {
		initial[i].ID = fmt.Sprintf("feed-%d", i)
	}
	results, next := replaceSessionResults(initial)
	more := []Feed{{ID: "feed-498"}, {ID: "feed-499"}, {ID: "feed-500"}}
	results, next, registered := appendSessionResults(results, next, more)
	if len(registered) != 2 || next != maxBrowseSessionResults {
		t.Fatalf("登记结果不符合容量上限: registered=%d next=%d", len(registered), next)
	}
	session := &BrowseSession{results: results, nextResultIndex: next}
	for _, feed := range registered {
		resolved, err := session.resolveResult(strconv.Itoa(feed.Index))
		if err != nil {
			t.Fatalf("响应 index=%d 无法 resolveResult: %v", feed.Index, err)
		}
		if resolved.ID != feed.ID {
			t.Fatalf("响应 index=%d 打开目标错误: got=%s want=%s", feed.Index, resolved.ID, feed.ID)
		}
	}
	if _, ok := results["feed-500"]; ok {
		t.Fatal("未返回 feed 不得登记到 result_ref")
	}
}

func TestPollOpenedNoteSnapshotDoesNotSwallowFatal(t *testing.T) {
	calls := 0
	_, err := pollOpenedNoteSnapshot(context.Background(), time.Second, time.Millisecond, func() (*OpenedNoteSnapshot, error) {
		calls++
		return nil, fmt.Errorf("probe: %w", ErrFatalRendererError)
	})
	if !IsFatalRendererError(err) || calls != 1 {
		t.Fatalf("fatal 应立即返回: calls=%d err=%v", calls, err)
	}
}

func TestPollOpenedNoteSnapshotRetriesNoDetail(t *testing.T) {
	calls := 0
	want := &OpenedNoteSnapshot{}
	got, err := pollOpenedNoteSnapshot(context.Background(), time.Second, time.Millisecond, func() (*OpenedNoteSnapshot, error) {
		calls++
		if calls == 1 {
			return nil, xerrors.ErrNoFeedDetail
		}
		return want, nil
	})
	if err != nil || got != want || calls != 2 {
		t.Fatalf("ErrNoFeedDetail 后应重试成功: calls=%d err=%v", calls, err)
	}
	_, err = pollOpenedNoteSnapshot(context.Background(), 0, time.Millisecond, func() (*OpenedNoteSnapshot, error) {
		return nil, xerrors.ErrNoFeedDetail
	})
	if !errors.Is(err, xerrors.ErrNoFeedDetail) {
		t.Fatalf("轮询耗尽应保留 ErrNoFeedDetail: %v", err)
	}
}

func TestPollOpenedNoteSnapshotRetriesEvalTimeout(t *testing.T) {
	calls := 0
	want := &OpenedNoteSnapshot{}
	got, err := pollOpenedNoteSnapshot(context.Background(), time.Second, time.Millisecond, func() (*OpenedNoteSnapshot, error) {
		calls++
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return want, nil
	})
	if err != nil || got != want || calls != 2 {
		t.Fatalf("Eval timeout 后应在预算内重试成功: calls=%d err=%v", calls, err)
	}
}

func TestHistoryTargetReadyDetailOverlay(t *testing.T) {
	fromProbe := xhsReadyProbe{
		URL:               "https://www.xiaohongshu.com/search_result?keyword=%E4%B8%89%E4%BA%9A",
		VisibleDetailCount: 1,
	}
	fromURL := "https://www.xiaohongshu.com/search_result?keyword=%E4%B8%89%E4%BA%9A"
	targetProbe := xhsReadyProbe{
		URL:               fromURL,
		VisibleDetailCount: 0,
		SearchResultCount:  12,
		SearchFeedCount:    12,
	}
	if !historyTargetReady(targetProbe, fromURL, XHSReadySearch, true) {
		t.Fatalf("详情弹层返回：URL 不变且 VisibleDetailCount=0 时应成功")
	}
	if historyTargetReady(fromProbe, fromURL, XHSReadySearch, true) {
		t.Fatalf("详情弹层返回：VisibleDetailCount 仍为正数时不得判定成功")
	}
}

func TestHistoryTargetReadyNonDetailRequiresURLChange(t *testing.T) {
	fromURL := "https://www.xiaohongshu.com/search_result?keyword=%E4%B8%89%E4%BA%9A"
	probe := xhsReadyProbe{
		URL:          fromURL,
		HomeFeedCount: 10,
	}
	if historyTargetReady(probe, fromURL, XHSReadyHome, false) {
		t.Fatalf("非详情后退：URL 未改变时不得判定成功")
	}
	probe.URL = "https://www.xiaohongshu.com/explore"
	if !historyTargetReady(probe, fromURL, XHSReadyHome, false) {
		t.Fatalf("非详情后退：URL 改变且目标页 ready 时应成功")
	}
}

func TestMismatchActionsExcludeDetailTools(t *testing.T) {
	session := &BrowseSession{
		results: map[string]Feed{
			"0": {ID: "feed-0"},
			"1": {ID: "feed-1"},
		},
		nextResultIndex: 2,
	}
	results := session.semanticResultsLocked()
	available, actions := session.mismatchActionsLocked(results)
	for _, tool := range []string{"get_note_detail", "like_feed", "favorite_feed", "comment_feed", "reply_comment_in_feed", "go_back"} {
		for _, a := range available {
			if a == tool {
				t.Fatalf("mismatch available actions 不得暴露详情/后退工具: %s", tool)
			}
		}
	}
	for _, tool := range []string{"open_note", "search_feeds", "get_page_state", "close_page"} {
		found := false
		for _, a := range available {
			if a == tool {
				found = true
			}
		}
		if !found {
			t.Fatalf("mismatch available actions 应包含 %s: %v", tool, available)
		}
	}
	if len(actions) != 5 {
		t.Fatalf("mismatch semantic actions 数量错误: %d -> %+v", len(actions), actions)
	}
	for _, action := range actions {
		switch action.Tool {
		case "get_note_detail", "like_feed", "favorite_feed", "comment_feed", "reply_comment_in_feed", "go_back":
			t.Fatalf("mismatch semantic actions 不得暴露详情/后退工具: %s", action.Tool)
		}
		if action.Tool == "open_note" && action.ResultRef == "" {
			t.Fatalf("mismatch open_note 必须带 result_ref: %+v", action)
		}
	}
	hasOpenNote := false
	for _, action := range actions {
		if action.Tool == "open_note" {
			hasOpenNote = true
		}
	}
	if !hasOpenNote {
		t.Fatalf("mismatch semantic actions 应包含 open_note: %+v", actions)
	}
}

func TestCheckReusableExploreUsesLightweightHealthCheck(t *testing.T) {
	page := &hrod.Page{}
	evalCalls := 0
	session := &BrowseSession{
		page:      page,
		opToken:   make(chan struct{}, 1),
		expiresAt: time.Now().Add(time.Minute),
		evalJS: func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
			evalCalls++
			return &proto.RuntimeRemoteObject{
				Type:  proto.RuntimeRemoteObjectTypeString,
				Value: gson.NewFrom(`"{\"url\":\"https://www.xiaohongshu.com/explore\",\"readyState\":\"complete\"}"`),
			}, nil
		},
	}
	session.opToken <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	check := session.CheckReusable(ctx)
	if check.Status != SessionReady || !check.Ready {
		t.Fatalf("explore 页健康时应 SessionReady，得到 %+v", check)
	}
	if check.LastError != "" {
		t.Fatalf("explore 页健康时不应有错误: %s", check.LastError)
	}
	if evalCalls != 1 {
		t.Fatalf("explore 复用应只执行一次健康 Eval，实际 %d", evalCalls)
	}
}

func TestCheckReusableRetriesOnceAfterTimeout(t *testing.T) {
	page := &hrod.Page{}
	evalCalls := 0
	session := &BrowseSession{
		page:      page,
		opToken:   make(chan struct{}, 1),
		expiresAt: time.Now().Add(time.Minute),
		evalJS: func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
			evalCalls++
			if evalCalls == 1 {
				time.Sleep(3 * time.Second)
				return nil, fmt.Errorf("健康检查超时")
			}
			return &proto.RuntimeRemoteObject{
				Type:  proto.RuntimeRemoteObjectTypeString,
				Value: gson.NewFrom(`"{\"url\":\"https://www.xiaohongshu.com/explore\",\"readyState\":\"complete\"}"`),
			}, nil
		},
	}
	session.opToken <- struct{}{}
	start := time.Now()
	check := session.CheckReusable(context.Background())
	elapsed := time.Since(start)
	if check.Status != SessionReady || !check.Ready {
		t.Fatalf("第一次超时后重试应 SessionReady，得到 %+v", check)
	}
	if evalCalls != 2 {
		t.Fatalf("超时后应重试一次，实际 %d 次 Eval", evalCalls)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("总预算应约 5s（首次2s+退避+重试），实际 %v", elapsed)
	}
}

func TestCheckReusableParentContextCanceled(t *testing.T) {
	page := &hrod.Page{}
	session := &BrowseSession{
		page:      page,
		opToken:   make(chan struct{}, 1),
		expiresAt: time.Now().Add(time.Minute),
		evalJS: func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
			return nil, ctx.Err()
		},
	}
	session.opToken <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	check := session.CheckReusable(ctx)
	if check.LastError != "请求已取消" {
		t.Fatalf("父 ctx 取消应返回请求已取消，得到 %s", check.LastError)
	}
	if check.HealthCheckedAt.IsZero() {
		t.Fatalf("HealthCheckedAt 应为完成时间，得到零值")
	}
}

func TestTryCloseIdleClosesWhenFree(t *testing.T) {
	page := &hrod.Page{}
	closed := false
	session := &BrowseSession{
		page:      page,
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		onRemove:  func(s *BrowseSession) { closed = true },
	}
	session.opToken <- struct{}{}
	if !session.TryCloseIdle() {
		t.Fatalf("空闲 session 应可关闭")
	}
	if !closed {
		t.Fatalf("onRemove 应被调用")
	}
}

func TestTryCloseIdleRefusesWhenBusy(t *testing.T) {
	page := &hrod.Page{}
	closed := false
	session := &BrowseSession{
		page:      page,
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		onRemove:  func(s *BrowseSession) { closed = true },
	}
	// opToken 空 = 操作占用中（busy）
	if session.TryCloseIdle() {
		t.Fatalf("busy session 不应可关闭")
	}
	if closed {
		t.Fatalf("busy session 不应触发 onRemove")
	}
}

func TestSearchTimeoutIs180Seconds(t *testing.T) {
	if sessionSearchTimeout != 180*time.Second {
		t.Fatalf("sessionSearchTimeout = %v, 期望 180s", sessionSearchTimeout)
	}
}

func TestSearchDeadlineKeepsEarlierCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	searchCtx, searchCancel := context.WithTimeout(ctx, sessionSearchTimeout)
	defer searchCancel()
	if d, ok := searchCtx.Deadline(); !ok || time.Until(d) > 30*time.Second {
		t.Fatalf("search ctx deadline 不应长于 caller 的 30s，得到 %v", time.Until(d))
	}
}

func TestSearchDeadlineReleasesOperationToken(t *testing.T) {
	session := &BrowseSession{
		page:      &hrod.Page{},
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		expiresAt: time.Now().Add(time.Minute),
		timeout:   time.Minute,
		evalJS: func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
			return &proto.RuntimeRemoteObject{
				Type:  proto.RuntimeRemoteObjectTypeString,
				Value: gson.NewFrom(`"{\"url\":\"https://www.xiaohongshu.com/explore\",\"scroll_y\":0}"`),
			}, nil
		},
	}
	defer session.close()
	session.opToken <- struct{}{}
	opCtx, err := session.beginLockedOperation(context.Background(), true)
	if err != nil {
		t.Fatalf("beginLockedOperation 失败: %v", err)
	}
	_ = opCtx
	session.finishOperation(context.DeadlineExceeded)
	select {
	case <-session.opToken:
	default:
		t.Fatalf("deadline 后 opToken 应可再次取得")
	}
}

func TestSearchDeadlineKeepsSession(t *testing.T) {
	session := &BrowseSession{
		page:      &hrod.Page{},
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		expiresAt: time.Now().Add(time.Minute),
		timeout:   time.Minute,
		evalJS: func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
			return &proto.RuntimeRemoteObject{
				Type:  proto.RuntimeRemoteObjectTypeString,
				Value: gson.NewFrom(`"{\"url\":\"https://www.xiaohongshu.com/explore\",\"scroll_y\":0}"`),
			}, nil
		},
	}
	defer session.close()
	session.opToken <- struct{}{}
	before := session.expiresAt
	opCtx, err := session.beginLockedOperation(context.Background(), true)
	if err != nil {
		t.Fatalf("beginLockedOperation 失败: %v", err)
	}
	_ = opCtx
	session.finishOperation(context.DeadlineExceeded)
	if session.closed {
		t.Fatalf("deadline 不应关闭 session")
	}
	if session.page == nil {
		t.Fatalf("deadline 不应清空 page")
	}
	if !session.expiresAt.After(before) {
		t.Fatalf("deadline 后 TTL 应被续期")
	}
}
