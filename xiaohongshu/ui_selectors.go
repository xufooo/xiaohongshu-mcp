package xiaohongshu

import (
	"encoding/json"
	"fmt"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const (
	SelectorSearchInputInFeeds        = `#search-input`
	SelectorSearchInputInSearchResult = `#search-input`
	SelectorSearchInput               = `#search-input`
	SelectorMarkedSearchInput         = `[data-xhs-mcp-search-input="1"]`
	SelectorSearchButton              = `.search-icon, .search-btn, button[type="submit"]`
	SelectorSearchResult              = `.feeds-container, .note-list, .search-layout, div[data-v-]`
	SelectorFeedCard                  = `section.note-item, .note-item, .feeds-container section, .note-list section`
	SelectorFeedDetailReady           = `.note-detail-mask, .note-container, .interact-container, .comments-container`
	SelectorCommentBox                = `div.input-box div.content-edit span, div.input-box div.content-edit p.content-input`
)

type SelectorSpec struct {
	Name        string `json:"name"`
	Selector    string `json:"selector"`
	Purpose     string `json:"purpose,omitempty"`
	Required    bool   `json:"required,omitempty"`
	VisibleOnly bool   `json:"visible_only,omitempty"`
	MaxMatches  int    `json:"max_matches,omitempty"`
}

type SelectorProbeResult struct {
	Name         string   `json:"name"`
	Selector     string   `json:"selector"`
	Count        int      `json:"count"`
	VisibleCount int      `json:"visible_count"`
	Samples      []string `json:"samples,omitempty"`
}

var (
	SearchInputSpec = SelectorSpec{
		Name:        "search_input",
		Selector:    SelectorSearchInput,
		Purpose:     "搜索框",
		Required:    true,
		VisibleOnly: true,
		MaxMatches:  2,
	}
	SearchResultSpec = SelectorSpec{
		Name:        "search_result",
		Selector:    SelectorFeedCard,
		Purpose:     "搜索结果卡片",
		Required:    true,
		VisibleOnly: true,
		MaxMatches:  2,
	}
	FeedDetailReadySpec = SelectorSpec{
		Name:       "feed_detail_ready",
		Selector:   SelectorFeedDetailReady,
		Purpose:    "笔记详情页主体",
		Required:   true,
		MaxMatches: 2,
	}
	CommentBoxSpec = SelectorSpec{
		Name:        "comment_box",
		Selector:    SelectorCommentBox,
		Purpose:     "评论输入框",
		VisibleOnly: true,
		MaxMatches:  2,
	}
	LikeButtonSpec = SelectorSpec{
		Name:        "like_button",
		Selector:    SelectorLikeButton,
		Purpose:     "点赞按钮",
		VisibleOnly: true,
		MaxMatches:  2,
	}
)

// ProbeSelectors 用单次 JS eval 批量探测选择器命中情况。
func ProbeSelectors(page *hrod.Page, specs []SelectorSpec) ([]SelectorProbeResult, error) {
	obj, err := page.Eval(`(specs) => {
		const visible = (el) => {
			if (!el || !el.isConnected) return false;
			if (typeof el.checkVisibility === "function") {
				return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
			}
			if (el.offsetParent !== null) return true;
			const rect = el.getBoundingClientRect();
			const style = window.getComputedStyle(el);
			return style.display !== "none" &&
				style.visibility !== "hidden" &&
				Number(style.opacity || "1") > 0 &&
				rect.width > 0 &&
				rect.height > 0;
		};
		const sampleText = (el) => (el.textContent || "")
			.replace(/\s+/g, " ")
			.trim()
			.slice(0, 80);
		const results = (Array.isArray(specs) ? specs : []).map((spec) => {
			const name = spec.name || spec.Name || "";
			const selector = spec.selector || spec.Selector || "";
			let elements = [];
			let samples = [];
			try {
				elements = Array.from(document.querySelectorAll(selector));
				samples = elements.slice(0, 2).map(sampleText).filter(Boolean);
			} catch (err) {
				samples = ["selector error: " + String(err).slice(0, 60)];
			}
			return {
				name,
				selector,
				count: elements.length,
				visible_count: elements.filter(visible).length,
				samples,
			};
		});
		return JSON.stringify(results);
	}`, specs)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("selector probe returned nil")
	}

	var results []SelectorProbeResult
	if err := json.Unmarshal([]byte(obj.Value.Str()), &results); err != nil {
		return nil, err
	}
	return results, nil
}
