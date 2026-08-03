package xiaohongshu

import (
	"encoding/json"
	"fmt"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const (
	SelectorSearchInputInFeeds        = `#search-input-in-feeds`
	SelectorSearchInputInSearchResult = `#search-input`
	SelectorSearchInput               = SelectorSearchInputInFeeds + `, ` + SelectorSearchInputInSearchResult + `, input[placeholder*="搜索"]` // 兜底：匹配 placeholder 含"搜索"的输入框
	SelectorMarkedSearchInput         = `[data-xhs-mcp-search-input="1"]`
	SelectorSelectedSearchInput       = `[data-xhs-mcp-search-input="selected"]:not([aria-hidden="true"])`
	SelectorSearchButton              = `.search-icon, .search-btn, button[type="submit"]`
	SelectorSearchResult              = `.feeds-container, .note-list, .search-layout, div[data-v-]`
	SelectorFeedCard                  = `section.note-item, .note-item, .feeds-container section, .note-list section`
	SelectorFeedDetailReady           = `.note-detail-mask, .note-container, .interact-container, .comments-container`
	SelectorCommentBox                = `div.input-box div.content-edit p.content-input`
	SelectorCommentSubmitButton       = `.btn.submit`

	// 通知页选择器
	SelectorNotificationEntry          = `a[href="/notification"]`                              // 侧栏通知入口
	SelectorNotificationBadge          = `a[href="/notification"] .badge-container`              // 通知入口未读 badge
	SelectorNotificationPage           = `.notification-page`                                    // 通知页容器
	SelectorNotificationTab            = `.notification-page .reds-tab-item.tab-item`            // 通知 tab（3 个）
	SelectorNotificationItem           = `.notification-page .tabs-content-container .container` // 通知 item
	SelectorNotificationUserAvatar     = `.user-avatar`                                          // item 内头像链接
	SelectorNotificationNickname       = `.user-info a`                                          // item 内昵称链接
	SelectorNotificationHint           = `.interaction-hint span`                                // item 内互动提示
	SelectorNotificationTime           = `.interaction-time`                                     // item 内时间
	SelectorNotificationContent        = `.interaction-content`                                  // item 内评论内容(仅 mentions)
	SelectorNotificationReplyButton    = `.action-reply`                                         // 回复按钮(仅 mentions)
	SelectorNotificationLikeButton     = `.action-like`                                          // 点赞按钮(仅 mentions)
	SelectorNotificationLikeUse        = `.action-like svg use`                                  // 点赞状态 svg use
	SelectorNotificationReplyInput     = `textarea.comment-input`                                // 回复输入框
	SelectorNotificationReplySubmit    = `.input-buttons .submit`                                // 发送按钮
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
	NotificationEntrySpec = SelectorSpec{
		Name:        "notification_entry",
		Selector:    SelectorNotificationEntry,
		Purpose:     "侧栏通知入口",
		Required:    true,
		VisibleOnly: true,
		MaxMatches:  2,
	}
	NotificationPageSpec = SelectorSpec{
		Name:        "notification_page",
		Selector:    SelectorNotificationPage,
		Purpose:     "通知页容器",
		Required:    true,
		VisibleOnly: true,
		MaxMatches:  1,
	}
	NotificationTabSpec = SelectorSpec{
		Name:        "notification_tab",
		Selector:    SelectorNotificationTab,
		Purpose:     "通知 tab",
		Required:    true,
		VisibleOnly: true,
		MaxMatches:  3,
	}
	NotificationItemSpec = SelectorSpec{
		Name:        "notification_item",
		Selector:    SelectorNotificationItem,
		Purpose:     "通知 item",
		VisibleOnly: true,
		MaxMatches:  20,
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
