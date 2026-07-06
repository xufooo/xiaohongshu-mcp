package xiaohongshu

import (
	"errors"
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
		t.Fatalf("expected one minute for detail extraction, got %s", got)
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
