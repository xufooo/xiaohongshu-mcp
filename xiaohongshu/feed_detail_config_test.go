package xiaohongshu

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCommentLoadConfigUsesFastForMissingOrInvalidSpeed(t *testing.T) {
	for _, speed := range []string{"", "unexpected"} {
		cfg := normalizeCommentLoadConfig(CommentLoadConfig{ScrollSpeed: speed})
		if cfg.ScrollSpeed != "fast" {
			t.Fatalf("speed %q should normalize to fast, got %q", speed, cfg.ScrollSpeed)
		}
	}

	for _, speed := range []string{"slow", "normal", "fast"} {
		cfg := normalizeCommentLoadConfig(CommentLoadConfig{ScrollSpeed: speed})
		if cfg.ScrollSpeed != speed {
			t.Fatalf("valid speed %q changed to %q", speed, cfg.ScrollSpeed)
		}
	}
}

func TestSessionCommentPageLoadConfigTargetsNextFiveToTenComments(t *testing.T) {
	progress := commentProgress{Count: 12}
	sawBelowFixedNextTen := false

	for range 100 {
		cfg := sessionCommentPageLoadConfig(progress, nil)
		if cfg.MaxCommentItems < 17 || cfg.MaxCommentItems > 22 {
			t.Fatalf("expected next page target between 17 and 22, got %d", cfg.MaxCommentItems)
		}
		if cfg.MaxCommentItems < 22 {
			sawBelowFixedNextTen = true
		}
	}

	if !sawBelowFixedNextTen {
		t.Fatalf("expected randomized target below fixed next-ten target")
	}
}

func TestSessionCommentPageLoadConfigCapsTargetAtTotal(t *testing.T) {
	cfg := sessionCommentPageLoadConfig(commentProgress{Count: 18, Total: 20}, nil)

	if cfg.MaxCommentItems != 20 {
		t.Fatalf("expected target capped at total 20, got %d", cfg.MaxCommentItems)
	}
}

func TestSessionCommentPageLoadConfigFallsBackToFirstPageOnProgressError(t *testing.T) {
	cfg := sessionCommentPageLoadConfig(commentProgress{}, errors.New("progress unavailable"))

	if cfg.MaxCommentItems != 10 {
		t.Fatalf("expected first page target 10, got %d", cfg.MaxCommentItems)
	}
}

func TestCommentLoadTimeoutLeavesTimeForDetailExtraction(t *testing.T) {
	if commentLoadTimeout >= feedDetailPageTimeout {
		t.Fatalf("comment loading timeout %s must be shorter than page timeout %s",
			commentLoadTimeout, feedDetailPageTimeout)
	}
	if got := feedDetailPageTimeout - commentLoadTimeout; got != time.Minute {
		t.Fatalf("expected 1 minute for detail extraction, got %s", got)
	}
}

func TestCommentScrollSettingsUseSlowEmbeddedDefaults(t *testing.T) {
	await, delta := commentScrollSettings("slow")

	if await < time.Second {
		t.Fatalf("slow comment scroll interval = %s, want at least 1s", await)
	}
	if delta < 150 || delta > 300 {
		t.Fatalf("slow comment scroll delta = %.0f, want 150-300", delta)
	}
}

func TestCommentProgressScriptCountsParentAndSubComments(t *testing.T) {
	script := commentProgressScript()

	for _, want := range []string{`querySelectorAll(".parent-comment").length`, `querySelectorAll(".parent-comment > .children-comments > .comment-item-sub, .parent-comment > .reply-container > .list-container > .comment-item").length`} {
		if !strings.Contains(script, want) {
			t.Fatalf("commentProgressScript() missing %s in:\n%s", want, script)
		}
	}
}

func TestCommentCursorTracksScrollAndExpansionState(t *testing.T) {
	cursor := CommentCursor{
		FeedID:      "feed-1",
		Round:       3,
		ReturnedIDs: []string{"comment-1"},
		ExpandRound: 2,
		CreatedAt:   time.Unix(123, 0),
	}

	if cursor.FeedID != "feed-1" || cursor.Round != 3 || cursor.ExpandRound != 2 {
		t.Fatalf("cursor state not retained: %+v", cursor)
	}
	if len(cursor.ReturnedIDs) != 1 || cursor.ReturnedIDs[0] != "comment-1" {
		t.Fatalf("cursor returned ids not retained: %+v", cursor.ReturnedIDs)
	}
}

func TestLoadCommentsBatchAndExtractCommentsAPIsExist(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`func LoadCommentsBatch(ctx context.Context, page *hrod.Page, config CommentLoadConfig, cursor *CommentCursor, maxItems int) ([]Comment, *CommentCursor, bool, error)`,
		`scrollNoteScrollerMoved(page, scrollDelta)`,
		`nextVisibleShowMoreButton(page, config.MaxRepliesThreshold)`,
		`dispatchMouseClick(page, button.X, button.Y)`,
		`page.Timeout(2*time.Second).Eval(`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("batch comment loader missing %s", want)
		}
	}

	domSource, err := os.ReadFile("dom_extract.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(domSource), `func ExtractCommentsFromDOM(page *hrod.Page, feedID string) ([]Comment, error)`) {
		t.Fatal("ExtractCommentsFromDOM API missing")
	}
}

func TestCommentBatchRequiresObservedScrollerMovement(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`func scrollNoteScrollerMoved(page *hrod.Page, delta float64) (bool, error)`,
		`moved: scroller.scrollTop > before`,
		`评论容器未推进且尚未出现 end 标识`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("comment scroll movement contract missing %q", want)
		}
	}
}

func TestCommentBatchKeyUsesIDOrIndexedContentPrefix(t *testing.T) {
	if got := commentBatchKey(2, Comment{ID: "comment-1", Content: "ignored"}); got != "comment-1" {
		t.Fatalf("commentBatchKey() with ID = %q, want comment-1", got)
	}

	longContent := "abcdefghijklmnopqrstuvwxyz1234567890"
	if got := commentBatchKey(2, Comment{Content: longContent}); got != "idx_2_abcdefghijklmnopqrstuvwxyz1234" {
		t.Fatalf("commentBatchKey() fallback = %q", got)
	}
}

func TestLoadCommentsBatchCallsScrollToCommentsAreaBeforeBaseline(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)
	start := strings.Index(script, `func LoadCommentsBatch(`)
	end := strings.Index(script, `func commentBatchKey(`)
	if start < 0 || end < 0 || end <= start {
		t.Fatal("cannot isolate LoadCommentsBatch source")
	}
	batchSource := script[start:end]

	// A. scrollToCommentsArea 必须在 Round==0 守卫内
	anchorGuard := strings.Index(batchSource, `if batchCursor.Round == 0 {`)
	scrollArea := strings.Index(batchSource, `scrollToCommentsArea(page)`)
	if anchorGuard < 0 {
		t.Fatal("LoadCommentsBatch must guard scrollToCommentsArea with batchCursor.Round == 0")
	}
	if scrollArea < 0 {
		t.Fatal("LoadCommentsBatch must call scrollToCommentsArea(page)")
	}
	if anchorGuard >= scrollArea {
		t.Fatal("scrollToCommentsArea must be inside batchCursor.Round == 0 block")
	}

	// B. 初始滚动使用 scrollNoteScrollerMoved(page, 160)
	if !strings.Contains(batchSource, `scrollNoteScrollerMoved(page, 160)`) {
		t.Fatal("initial scroll must use scrollNoteScrollerMoved(page, 160)")
	}

	// C. batchCursor.Round++ 在初始滚动之后、第一次 collect 之前
	initScroll := strings.Index(batchSource, `scrollNoteScrollerMoved(page, 160)`)
	roundPP := strings.Index(batchSource, `batchCursor.Round++`)
	collectFunc := strings.Index(batchSource, `collect := func(limit int)`)
	if roundPP < 0 {
		t.Fatal("LoadCommentsBatch must increment Round after initial scroll")
	}
	if initScroll < 0 || roundPP < initScroll {
		t.Fatal("batchCursor.Round++ must appear after initial scroll")
	}
	if collectFunc < 0 || roundPP >= collectFunc {
		t.Fatal("batchCursor.Round++ must appear before first collect")
	}

	// D. maxRounds = 500
	if !strings.Contains(batchSource, `maxRounds := 500`) {
		t.Fatal("LoadCommentsBatch must use maxRounds = 500")
	}

	// E. 没有 cursor replay
	if strings.Contains(batchSource, `if cursor != nil && cursor.Round > 0 {`) {
		t.Fatal("LoadCommentsBatch must not contain cursor.Round restore")
	}
	if strings.Contains(batchSource, `for i := 0; i < cursor.Round; i++ {`) {
		t.Fatal("LoadCommentsBatch must not contain cursor.Round replay loop")
	}

	// F. 全文件没有 scrollNoteScrollerObserved
	if strings.Contains(script, `scrollNoteScrollerObserved`) {
		t.Fatal("feed_detail.go must not contain scrollNoteScrollerObserved")
	}
}

func TestBatchTotalItemsOnlyUsesKnownCommentProgressTotal(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	if strings.Contains(script, `len(nextCursor.ReturnedIDs)`) {
		t.Fatal("TotalItems must not fall back to returned cursor IDs")
	}
	if !strings.Contains(script, `knownCommentTotal(commentPage)`) {
		t.Fatal("batch detail should set TotalItems only from known comment progress total")
	}
}

func TestNextShowMoreButtonOnlyTargetsReplyExpansionButtons(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	if strings.Contains(script, `querySelectorAll(".children-comments .show-more, .show-more")`) {
		t.Fatal("nextShowMoreButton must not use the broad .show-more selector")
	}
	for _, want := range []string{
		`.flatMap((parent) => Array.from(parent.querySelectorAll(":scope > .children-comments .show-more, :scope > .reply-container .show-more")))`,
		`!text.includes("展开") || text.includes("收起")`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("nextShowMoreButton missing %s", want)
		}
	}
}

func TestDOMParentCommentIDFallsBackToParentDataCommentID(t *testing.T) {
	source, err := os.ReadFile("dom_extract.go")
	if err != nil {
		t.Fatal(err)
	}
	want := `id: parent.dataset?.id || parent.getAttribute("data-comment-id") || top.dataset?.id || top.getAttribute("data-comment-id") || "",`
	if !strings.Contains(string(source), want) {
		t.Fatalf("parent comment id fallback missing parent data-comment-id")
	}
}

func TestReplyExpansionRetryUsesSlowerDelayThanCommentPolling(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`replyExpansionRetryDelay`,
		`time.Second`,
		`retry.Delay(replyExpansionRetryDelay)`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("reply expansion retry missing %s", want)
		}
	}
}

func TestReplyExpansionWaitComparesClickedParentReplyCount(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`ParentIndex int`,
		"`json:\"parentIndex\"`",
		`countReplyItems(page, button.ParentIndex)`,
		`waitReplyItemsChanged(page, button.ParentIndex, before, 7*time.Second)`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("reply expansion wait missing clicked-parent count handling %s", want)
		}
	}
}

func TestScrollNoteScrollerReturnsErrorWhenScrollerMissing(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`result == nil || !result.Value.Bool()`,
		`return fmt.Errorf("评论容器不存在")`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("scrollNoteScroller missing false-result handling %s", want)
		}
	}
}

func TestFeedDetailEvalCallsUseShortIndependentTimeouts(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	if strings.Contains(script, `page.Eval(`) {
		t.Fatal("feed_detail.go should wrap page Eval calls with page.Timeout(2*time.Second)")
	}
	if got := strings.Count(script, `page.Timeout(2*time.Second).Eval(`); got < 8 {
		t.Fatalf("expected feed_detail.go Eval calls to use 2s timeout wrappers, got %d", got)
	}
}

func TestCommentLoadDeadlineStopsLateScrollAndReplyExpansion(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`remaining < 30*time.Second`,
		`停止新滚动`,
		`remaining < 15*time.Second`,
		`跳过末尾子评论展开`,
		`clickMoreReplies(page, config.MaxRepliesThreshold, remainingDeadline)`,
		`func clickMoreReplies(page *hrod.Page, maxRepliesThreshold int, remainingDeadline func() time.Duration) error`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("comment load deadline guard missing %s", want)
		}
	}
}

func TestClickMoreRepliesSoftFailsEvalTimeouts(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	for _, want := range []string{
		`if isEvalTimeout(err) {`,
		`logrus.Warnf("检查子评论展开按钮超时，跳过本轮: %v", err)`,
		`continue`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("clickMoreReplies timeout soft-fail missing %s", want)
		}
	}
}

func TestReadStageUsesNoteScrollerForPanelScrolls(t *testing.T) {
	source, err := os.ReadFile("read_stage.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	if strings.Contains(script, `Mouse.Scroll`) {
		t.Fatal("read_stage.go should use .note-scroller scrollBy for note panel scrolling")
	}
	if got := strings.Count(script, `scrollNoteScroller(page,`); got < 4 {
		t.Fatalf("read_stage.go should route panel scrolls through scrollNoteScroller, got %d calls", got)
	}
}

func TestCommentFeedUsesNoteScrollerForCommentAreaScrolls(t *testing.T) {
	source, err := os.ReadFile("comment_feed.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	if strings.Contains(script, `Mouse.Scroll`) {
		t.Fatal("comment_feed.go should use .note-scroller scrollBy for comment area scrolling")
	}
	if got := strings.Count(script, `scrollNoteScroller(page,`); got < 2 {
		t.Fatalf("comment_feed.go should route comment area scrolls through scrollNoteScroller, got %d calls", got)
	}
}

func TestReplyExpansionClickUsesHumanizedClickPoint(t *testing.T) {
	source, err := os.ReadFile("feed_detail.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)

	if !strings.Contains(script, `return page.ClickPoint(proto.Point{X: x, Y: y})`) {
		t.Fatal("dispatchMouseClick should delegate to page.ClickPoint")
	}
	if strings.Contains(script, `InputDispatchMouseEvent`) {
		t.Fatal("dispatchMouseClick should not manually dispatch CDP mouse events")
	}
}

func TestShouldWaitForInitialComments(t *testing.T) {
	tests := []struct {
		name     string
		count    string
		comments []Comment
		want     bool
	}{
		{name: "reported comments but no state list", count: "7", want: true},
		{name: "comments are already populated", count: "7", comments: []Comment{{ID: "comment-1"}}, want: false},
		{name: "reported no comments", count: "0", want: false},
		{name: "non numeric display count", count: "1.2万", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &FeedDetailResponse{
				Note:     FeedDetail{InteractInfo: InteractInfo{CommentCount: tt.count}},
				Comments: CommentList{List: tt.comments},
			}
			if got := shouldWaitForInitialComments(response); got != tt.want {
				t.Fatalf("shouldWaitForInitialComments() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldUseInitialCommentSnapshot(t *testing.T) {
	withComment := &FeedDetailResponse{Comments: CommentList{List: []Comment{{ID: "comment-1"}}}}
	empty := &FeedDetailResponse{Comments: CommentList{List: []Comment{}}}

	if !shouldUseInitialCommentSnapshot(withComment, empty) {
		t.Fatal("expected populated initial snapshot to replace an empty later snapshot")
	}
	if shouldUseInitialCommentSnapshot(empty, withComment) {
		t.Fatal("an empty initial snapshot must not replace populated comments")
	}
	if shouldUseInitialCommentSnapshot(nil, empty) {
		t.Fatal("nil initial snapshot must not be used")
	}
}
