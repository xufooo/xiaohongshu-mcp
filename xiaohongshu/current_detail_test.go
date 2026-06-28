package xiaohongshu

import "testing"

func TestCurrentFeedDetailMatchedIgnoresStateOnlyMatch(t *testing.T) {
	probe := currentFeedDetailProbe{
		StateMatched: true,
	}

	if currentFeedDetailMatched(probe) {
		t.Fatal("state-only match should not be treated as the current visible detail")
	}
}

func TestCurrentFeedDetailMatchedRequiresVisibleDetailWithURLMatch(t *testing.T) {
	probe := currentFeedDetailProbe{
		URLMatched:         true,
		VisibleDetailCount: 1,
	}

	if !currentFeedDetailMatched(probe) {
		t.Fatal("URL match with visible detail should be accepted")
	}
}

func TestCurrentFeedDetailMatchedAcceptsVisibleDOMMatch(t *testing.T) {
	probe := currentFeedDetailProbe{
		VisibleMatchedDetailCount: 1,
	}

	if !currentFeedDetailMatched(probe) {
		t.Fatal("visible DOM match should be accepted")
	}
}

func TestDetailReadyRequiresCurrentFeedMatch(t *testing.T) {
	probe := xhsReadyProbe{
		DetailState: true,
		DetailCount: 1,
	}

	if detailReady(probe, "feed-1") {
		t.Fatal("detail readiness for a feed must not rely on state/detail count without current-feed match")
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

