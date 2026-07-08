package xiaohongshu

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
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
	}
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
	}
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
		`page.Keyboard.Press("Escape")`,
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
