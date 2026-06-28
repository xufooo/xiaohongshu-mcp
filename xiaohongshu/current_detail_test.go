package xiaohongshu

import (
	"context"
	"errors"
	"testing"

	"github.com/go-rod/rod/lib/cdp"
)

func TestCurrentFeedDetailMatchedIgnoresStateOnlyMatch(t *testing.T) {
	probe := currentFeedDetailProbe{
		StateMatched: true,
	}

	if currentFeedDetailMatched(probe, "feed-1") {
		t.Fatal("state-only match should not be treated as the current visible detail")
	}
}

func TestCurrentFeedDetailMatchedRequiresVisibleDetailWithURLMatch(t *testing.T) {
	probe := currentFeedDetailProbe{
		URLMatched:         true,
		VisibleDetailCount: 1,
	}

	if !currentFeedDetailMatched(probe, "feed-1") {
		t.Fatal("URL match with visible detail should be accepted")
	}
}

func TestCurrentFeedDetailMatchedAcceptsVisibleDOMMatch(t *testing.T) {
	probe := currentFeedDetailProbe{
		VisibleMatchedDetailCount: 1,
	}

	if !currentFeedDetailMatched(probe, "feed-1") {
		t.Fatal("visible DOM match should be accepted")
	}
}

func TestCurrentFeedDetailMatchedRejectsFeedIDSubstringCollision(t *testing.T) {
	probe := currentFeedDetailProbe{
		URL:                "https://www.xiaohongshu.com/explore/not-abc-note",
		VisibleDetailCount: 1,
	}

	if currentFeedDetailMatched(probe, "abc") {
		t.Fatal("feed ID substring collision should not be treated as a URL match")
	}
}

func TestCurrentFeedDetailMatchedRejectsStateMatchedVisibleDetail(t *testing.T) {
	probe := currentFeedDetailProbe{
		VisibleDetailCount: 1,
		StateMatched:       true,
	}

	if currentFeedDetailMatched(probe, "feed-1") {
		t.Fatal("state match with visible detail should not be accepted without URL or DOM match")
	}
}

func TestCurrentFeedDetailMatchedRejectsHiddenDetail(t *testing.T) {
	probe := currentFeedDetailProbe{
		URLMatched: true,
	}

	if currentFeedDetailMatched(probe, "feed-1") {
		t.Fatal("hidden detail should not be accepted even when URL matches")
	}
}

func TestDetailReadyRejectsDifferentLoadedNote(t *testing.T) {
	probe := xhsReadyProbe{
		DetailCount: 1,
	}

	if detailReady(probe, "feed-1") {
		t.Fatal("detail readiness for a feed must not rely on detail count without current-feed match")
	}
}

func TestDetailReadyAcceptsMatchedVisibleDetail(t *testing.T) {
	probe := xhsReadyProbe{
		DetailFeedMatched: true,
		DetailCount:       1,
	}

	if !detailReady(probe, "feed-1") {
		t.Fatal("matched visible detail should be ready")
	}
}

func TestDetailReadyRejectsStateMatchedVisibleDetailForFeedID(t *testing.T) {
	probe := xhsReadyProbe{
		DetailState: true,
		DetailCount: 1,
	}

	if detailReady(probe, "feed-1") {
		t.Fatal("feed-specific readiness should require a detail feed match")
	}
}

func TestDetailURLMatchesFeedIDWithQueryParam(t *testing.T) {
	rawURL := "https://www.xiaohongshu.com/explore?xsec_token=abc&feedID=feed-1"

	if !detailURLMatchesFeedID(rawURL, "feed-1") {
		t.Fatal("query parameter value should match feed ID")
	}
}

func TestDetailURLMatchesFeedIDWithPathSegment(t *testing.T) {
	rawURL := "https://www.xiaohongshu.com/explore/feed-1"

	if !detailURLMatchesFeedID(rawURL, "feed-1") {
		t.Fatal("path segment should match feed ID")
	}
}

func TestDetailURLMatchesFeedIDRejectsPathSubstringCollision(t *testing.T) {
	rawURL := "https://www.xiaohongshu.com/explore/not-feed-1-but-contains"

	if detailURLMatchesFeedID(rawURL, "feed-1") {
		t.Fatal("path substring collision should not match feed ID")
	}
}

func TestDetailURLMatchesFeedIDRejectsQueryValueSubstringCollision(t *testing.T) {
	rawURL := "https://www.xiaohongshu.com/explore?source=not-feed-1-but-contains"

	if detailURLMatchesFeedID(rawURL, "feed-1") {
		t.Fatal("query value substring collision should not match feed ID")
	}
}

func TestDetailURLMatchesFeedIDRejectsEmptyFeedID(t *testing.T) {
	rawURL := "https://www.xiaohongshu.com/explore/feed-1"

	if detailURLMatchesFeedID(rawURL, "") {
		t.Fatal("empty feed ID should not match")
	}
}

func TestIsTransientCurrentDetailProbeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "context canceled",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "cdp context not found",
			err:  cdp.ErrCtxNotFound,
			want: true,
		},
		{
			name: "cdp context destroyed",
			err:  cdp.ErrCtxDestroyed,
			want: true,
		},
		{
			name: "execution context destroyed string",
			err:  errors.New("Execution context was destroyed"),
			want: true,
		},
		{
			name: "permanent probe error",
			err:  errPermanentCurrentDetailProbe,
			want: false,
		},
		{
			name: "permanent generic error",
			err:  errors.New("json decode failed"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientCurrentDetailProbeError(tt.err); got != tt.want {
				t.Fatalf("isTransientCurrentDetailProbeError() = %v, want %v", got, tt.want)
			}
		})
	}
}
