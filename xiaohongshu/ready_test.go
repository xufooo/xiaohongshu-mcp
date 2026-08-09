package xiaohongshu

import (
	"testing"
	"time"
)

// TestHomeSearchSelectorIsDedicated 确认专用搜索输入框选择器与宽泛选择器不相等。
func TestHomeSearchSelectorIsDedicated(t *testing.T) {
	if SelectorSearchInputInFeeds == SelectorSearchInput {
		t.Fatalf("SelectorSearchInputInFeeds 必须独立于宽泛 SelectorSearchInput")
	}
	if !selectorSearchInputInFeedsIsDedicated(SelectorSearchInputInFeeds) {
		t.Fatalf("SelectorSearchInputInFeeds 应只指向首页专用输入框")
	}
}

func selectorSearchInputInFeedsIsDedicated(sel string) bool {
	// 专用选择器必须精确指向 #search-input-in-feeds，且不含宽泛兜底片段。
	return sel == `#search-input-in-feeds`
}

// TestHomeSearchReadyRequiresDedicatedInputSignal 首页搜索输入框未就绪时，即使存在 feed 也不得判定 ready。
func TestHomeSearchReadyRequiresDedicatedInputSignal(t *testing.T) {
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
	// 模拟 probe error：等价于 Observe(false) 重置
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
	// HomeSearch 首次 true 必须走稳定窗不立即成功
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
