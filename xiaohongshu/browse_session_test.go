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

	"github.com/go-rod/rod/lib/proto"
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
	if err := counter.add(context.Background(), timeoutErr, probeTimeout); !IsFatalRendererError(err) {
		t.Fatalf("连续第三次 timeout 且 probe timeout 应 fatal: %v", err)
	}
	if probeCalls != 1 {
		t.Fatalf("第三次 timeout 应执行一次 probe，实际 %d", probeCalls)
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

func TestEvalTimeoutCounterProbeErrorIsFatal(t *testing.T) {
	counter := &evalTimeoutCounter{}
	timeoutErr := context.DeadlineExceeded
	probeErr := errors.New("renderer connection closed")
	_ = counter.add(context.Background(), timeoutErr, func() error { return nil })
	_ = counter.add(context.Background(), timeoutErr, func() error { return nil })
	err := counter.add(context.Background(), timeoutErr, func() error { return probeErr })
	if !IsFatalRendererError(err) {
		t.Fatalf("probe 返回非 timeout 错误应 fatal: %v", err)
	}
	if !strings.Contains(err.Error(), probeErr.Error()) {
		t.Fatalf("fatal 错误应包含 probe 错误详情: %v", err)
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

func TestOpenNoteTwoStageState(t *testing.T) {
	session := &BrowseSession{
		seenNotes:         map[string]bool{"old": true},
		initialCommentIDs: []string{"old-comment"},
		notification:      browseNotificationState{active: true},
	}
	feed := Feed{ID: "feed-1", XsecToken: "token-1"}
	session.commitOpenNoteStage(feed, "https://example.test/search", "ref-1")
	info := session.Info()
	if !info.Opened || info.Read || info.SeenNotes[feed.ID] {
		t.Fatalf("第一阶段状态错误: %+v", info)
	}
	if len(session.GetInitialCommentIDs()) != 0 || session.notification.active {
		t.Fatal("第一阶段应清空评论 cursor 并重置通知 surface")
	}
	if len(session.timeline) != 1 || session.timeline[0].Action != "open_note" {
		t.Fatalf("第一阶段 timeline 错误: %+v", session.timeline)
	}
	info = session.commitReadNoteStage(feed.ID, []string{"c1", "c2"}, "ref-1")
	if !info.Opened || !info.Read || !info.SeenNotes[feed.ID] {
		t.Fatalf("第二阶段状态错误: %+v", info)
	}
	if got := session.GetInitialCommentIDs(); len(got) != 2 || got[0] != "c1" {
		t.Fatalf("第二阶段评论 cursor 错误: %v", got)
	}
	if len(session.timeline) != 2 || session.timeline[1].Action != "read_note" {
		t.Fatalf("第二阶段 timeline 错误: %+v", session.timeline)
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
