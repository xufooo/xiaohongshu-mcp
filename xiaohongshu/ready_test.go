package xiaohongshu

import (
	"testing"
	"time"
)

func TestHomeSearchReadyRequiresInputSignal(t *testing.T) {
	probe := xhsReadyProbe{
		URL:                    "https://www.xiaohongshu.com/explore",
		HomeFeedCount:          10,
		FeedCardCount:          5,
		SearchInputInFeedsReady: false,
	}
	if isXHSReady(probe, XHSReadyHomeSearch, "", false) {
		t.Fatalf("SearchInputInFeedsReady=false 时不应判定 XHSReadyHomeSearch ready")
	}
	probe.SearchInputInFeedsReady = true
	if !isXHSReady(probe, XHSReadyHomeSearch, "", false) {
		t.Fatalf("SearchInputInFeedsReady=true 且 feed 存在时应判定 ready")
	}
}

func TestXHSReadyProbeSelectorArgsWiring(t *testing.T) {
	args := xhsReadyProbeSelectorArgs()
	if len(args) != 9 {
		t.Fatalf("selector args 长度应为 9，实际 %d", len(args))
	}
	if args[0] != SelectorSearchInput {
		t.Fatalf("args[0] searchInputSelector 应为 SelectorSearchInput，实际 %v", args[0])
	}
	if args[6] != SelectorSearchInput {
		t.Fatalf("args[6] searchInputInFeedsSelector 应为 SelectorSearchInput，实际 %v", args[6])
	}
	if args[0].(string) == SelectorSearchInputInFeeds {
		t.Fatalf("searchInputSelector 不得为 SelectorSearchInputInFeeds")
	}
	if args[6].(string) == SelectorSearchInputInFeeds {
		t.Fatalf("searchInputInFeedsSelector 不得为 SelectorSearchInputInFeeds")
	}
	if args[1] != SelectorSearchResult {
		t.Fatalf("args[1] 应为 SelectorSearchResult，实际 %v", args[1])
	}
	if args[2] != SelectorFeedCard {
		t.Fatalf("args[2] 应为 SelectorFeedCard，实际 %v", args[2])
	}
	if args[3] != SelectorFeedDetailReady {
		t.Fatalf("args[3] 应为 SelectorFeedDetailReady，实际 %v", args[3])
	}
	if args[4] != SelectorCommentBox {
		t.Fatalf("args[4] 应为 SelectorCommentBox，实际 %v", args[4])
	}
	if args[5] != SelectorLikeButton {
		t.Fatalf("args[5] 应为 SelectorLikeButton，实际 %v", args[5])
	}
	if args[7] != SelectorNotificationPage {
		t.Fatalf("args[7] 应为 SelectorNotificationPage，实际 %v", args[7])
	}
	if args[8] != SelectorNotificationTab {
		t.Fatalf("args[8] 应为 SelectorNotificationTab，实际 %v", args[8])
	}
}

func TestHomeSearchReadyRequiresThreeSecondStableWindow(t *testing.T) {
	var st xhsReadyStability
	base := time.Now()
	if st.Observe(base, true) {
		t.Fatalf("首次 true 不应立即成功")
	}
	if st.Observe(base.Add(2999*time.Millisecond), true) {
		t.Fatalf("2.999s 仍不应成功")
	}
	if !st.Observe(base.Add(3*time.Second), true) {
		t.Fatalf("连续满 3s 应成功")
	}
}

func TestHomeSearchStableWindowResetsOnFalse(t *testing.T) {
	var st xhsReadyStability
	base := time.Now()
	st.Observe(base, true)
	st.Observe(base.Add(2*time.Second), true)
	if st.Observe(base.Add(3*time.Second), false) {
		t.Fatalf("false 后不应成功")
	}
	if st.Observe(base.Add(4*time.Second), true) {
		t.Fatalf("false 后重新起算，3s 内不应成功")
	}
	if !st.Observe(base.Add(7*time.Second), true) {
		t.Fatalf("false 后重新满 3s 应成功")
	}
}

func TestHomeSearchStableWindowResetsOnProbeError(t *testing.T) {
	var st xhsReadyStability
	base := time.Now()
	st.Observe(base, true)
	st.Observe(base.Add(2*time.Second), true)
	if st.Observe(base.Add(3*time.Second), false) {
		t.Fatalf("probe error 后不应成功")
	}
	if st.Observe(base.Add(4*time.Second), true) {
		t.Fatalf("error 重置后 3s 内不应成功")
	}
}

func TestOtherReadyKindsDoNotRequireStableWindow(t *testing.T) {
	for _, kind := range []XHSReadyKind{XHSReadyHome, XHSReadySearch, XHSReadyDetail} {
		var st xhsReadyStability
		if !xhsReadyDecision(kind, &st, time.Now(), true) {
			t.Fatalf("kind=%s 单次就绪应立即成功（不得走稳定窗）", kind)
		}
	}
	var st xhsReadyStability
	if xhsReadyDecision(XHSReadyHomeSearch, &st, time.Now(), true) {
		t.Fatalf("home_search 首次 true 不应立即成功（必须走稳定窗）")
	}
}

func TestXHSReadyPollRange(t *testing.T) {
	min, max := xhsReadyPollRange(XHSReadyHomeSearch)
	if min != homeSearchPollMin || max != homeSearchPollMax {
		t.Fatalf("home_search 轮询应为 800-1200ms，得到 %v-%v", min, max)
	}
	min, max = xhsReadyPollRange(XHSReadyHome)
	if min != defaultReadyPollMin || max != defaultReadyPollMax {
		t.Fatalf("普通 kind 轮询应为 300-500ms，得到 %v-%v", min, max)
	}
}
