package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func TestCommentCursorStoreSetGetDelete(t *testing.T) {
	service := &XiaohongshuService{commentCursorTTL: time.Minute}
	cursor := &xiaohongshu.CommentCursor{FeedID: "feed-1"}

	service.setCommentCursor("cc_feed-1_1", cursor)

	got, ok := service.getCommentCursor("cc_feed-1_1")
	if !ok {
		t.Fatal("expected stored cursor")
	}
	if got != cursor {
		t.Fatalf("stored cursor pointer changed: got %p want %p", got, cursor)
	}

	service.delCommentCursor("cc_feed-1_1")
	if _, ok := service.getCommentCursor("cc_feed-1_1"); ok {
		t.Fatal("expected deleted cursor to be absent")
	}
}

func TestCommentCursorStoreExpires(t *testing.T) {
	service := &XiaohongshuService{commentCursorTTL: 10 * time.Millisecond}

	service.setCommentCursor("cc_feed-1_2", &xiaohongshu.CommentCursor{FeedID: "feed-1"})
	time.Sleep(30 * time.Millisecond)

	if _, ok := service.getCommentCursor("cc_feed-1_2"); ok {
		t.Fatal("expected cursor to expire")
	}
}

func TestFeedDetailCommentsBatchDelegatesToActiveBrowseSession(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`if sid, ok := s.activeSessionForFeed(feedID); ok {`,
		`detail, nextCursor, hasMore, err := s.SessionDetailCommentsBatch(ctx, sid, feedID, cursor, maxItems, config)`,
		`page, err := s.acquirePageFor(detailCtx, "feed_detail_comments_batch")`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("GetFeedDetailCommentsBatch session delegation missing %s", want)
		}
	}
}

func TestCreateBrowseSessionReusesActiveSessionBeforeCreating(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	reuseChecks := []string{
		`if info, ok := s.browseSessions.ActiveInfo(); ok {`,
		`if session, err := s.browseSessions.Get(info.ID); err == nil {`,
		`info = session.Renew()`,
		`return &info, nil`,
	}
	for _, want := range reuseChecks {
		if !strings.Contains(script, want) {
			t.Fatalf("CreateBrowseSession active-session reuse missing %s", want)
		}
	}

	reuseIndex := strings.Index(script, `if info, ok := s.browseSessions.ActiveInfo(); ok {`)
	closeIndex := strings.Index(script, `s.browseSessions.CloseAll()`)
	acquireIndex := strings.Index(script, `page, err := s.acquirePageFor(ctx, "session")`)
	createIndex := strings.Index(script, `session := s.browseSessions.Create(page, s.actionState, s.browserManager.Release)`)
	if reuseIndex == -1 || closeIndex == -1 || acquireIndex == -1 || createIndex == -1 {
		t.Fatal("CreateBrowseSession expected flow markers missing")
	}
	if !(reuseIndex < closeIndex && closeIndex < acquireIndex && acquireIndex < createIndex) {
		t.Fatalf("CreateBrowseSession flow order invalid: reuse=%d close=%d acquire=%d create=%d",
			reuseIndex, closeIndex, acquireIndex, createIndex)
	}
}
