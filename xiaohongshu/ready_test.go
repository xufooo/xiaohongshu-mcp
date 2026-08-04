package xiaohongshu

import (
	"testing"
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
