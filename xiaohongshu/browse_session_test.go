package xiaohongshu

import (
	"context"
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
	for _, action := range actions {
		refs[action.Ref] = true
	}

	for _, ref := range []string{"session_state", "detail_current", "like_current", "comment_current", "back_to_results", "close_session"} {
		if !refs[ref] {
			t.Fatalf("missing semantic action %q in %+v", ref, actions)
		}
	}
	if refs["open_note:0"] || refs["read_current"] {
		t.Fatalf("unexpected pre-read/result actions in %+v", actions)
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

	_, err := session.Detail(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "必须先打开笔记") {
		t.Fatalf("Detail() error = %v, want 必须先打开笔记", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := session.Detail(context.Background(), false)
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

	_, err := session.Detail(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "browse session 页面不存在") {
		t.Fatalf("Detail() error = %v, want browse session 页面不存在", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := session.Detail(context.Background(), false)
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
}

func TestBrowseSessionRecommendedActionAvoidsWriteAfterRead(t *testing.T) {
	session := &BrowseSession{
		sourceURL:     "https://www.xiaohongshu.com/search_result_ai?keyword=test",
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
