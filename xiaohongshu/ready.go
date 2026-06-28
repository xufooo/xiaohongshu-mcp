package xiaohongshu

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type XHSReadyKind string

const (
	XHSReadyHome       XHSReadyKind = "home"
	XHSReadySearch     XHSReadyKind = "search"
	XHSReadyDetail     XHSReadyKind = "detail"
	XHSReadyProfile    XHSReadyKind = "profile"
	XHSReadyPublish    XHSReadyKind = "publish"
	XHSReadyCommentBox XHSReadyKind = "comment_box"
)

type XHSReadyOptions struct {
	Kind    XHSReadyKind
	FeedID  string
	Timeout time.Duration
}

type xhsReadyProbe struct {
	URL                string `json:"url"`
	Title              string `json:"title"`
	ReadyState         string `json:"ready_state"`
	ScrollY            int    `json:"scroll_y"`
	AppCount           int    `json:"app_count"`
	FeedCardCount      int    `json:"feed_card_count"`
	SearchInputCount   int    `json:"search_input_count"`
	SearchResultCount  int    `json:"search_result_count"`
	DetailCount        int    `json:"detail_count"`
	CommentBoxCount    int    `json:"comment_box_count"`
	LikeButtonCount    int    `json:"like_button_count"`
	HomeFeedCount      int    `json:"home_feed_count"`
	SearchFeedCount    int    `json:"search_feed_count"`
	ProfileState       bool   `json:"profile_state"`
	DetailState        bool   `json:"detail_state"`
	DetailFeedMatched  bool   `json:"detail_feed_matched"`
	DetailURLMatched   bool   `json:"detail_url_matched"`
	PublishSignalCount int    `json:"publish_signal_count"`
	StateFragment      string `json:"state_fragment,omitempty"`
	RiskText           string `json:"risk_text,omitempty"`
}

// WaitForXHSReady 等待页面就绪，按 kind 判断条件。
// 每 300-500ms 轮询一次 JS probe，超时返回最后一次 probe 摘要。
// 同时检测风险信号（登录失效/验证码/滑块等），发现风险立即返回。
func WaitForXHSReady(page *hrod.Page, opts XHSReadyOptions) error {
	if opts.Kind == "" {
		opts.Kind = XHSReadyHome
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}

	deadline := time.Now().Add(opts.Timeout)
	var last xhsReadyProbe
	var lastErr error

	for {
		if err := page.Err(); err != nil {
			return err
		}

		probe, err := probeXHSReady(page, opts.FeedID)
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			last = probe
			if probe.RiskText != "" {
				return fmt.Errorf("页面出现风险信号: %s; %s", probe.RiskText, formatXHSReadyProbe(probe))
			}
			if isXHSReady(probe, opts.Kind, opts.FeedID, false) {
				return nil
			}
		}

		if !time.Now().Before(deadline) {
			if lastErr == nil && isXHSReady(last, opts.Kind, opts.FeedID, true) {
				return nil
			}
			break
		}
		if err := page.SleepRandom(300*time.Millisecond, 500*time.Millisecond); err != nil {
			return err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("等待小红书页面就绪超时(kind=%s timeout=%s): %w; %s",
			opts.Kind, opts.Timeout, lastErr, formatXHSReadyProbe(last))
	}
	return fmt.Errorf("等待小红书页面就绪超时(kind=%s timeout=%s): %s",
		opts.Kind, opts.Timeout, formatXHSReadyProbe(last))
}

func probeXHSReady(page *hrod.Page, feedID string) (xhsReadyProbe, error) {
	obj, err := page.Eval(`(feedID, searchInputSelector, searchResultSelector, feedCardSelector, detailSelector, commentBoxSelector, likeButtonSelector) => {
		const count = (selector) => {
			try { return document.querySelectorAll(selector).length; } catch (_) { return 0; }
		};
		const visibleCount = (selector) => {
			try {
				return Array.from(document.querySelectorAll(selector)).filter((el) => {
					if (!el || !el.isConnected) return false;
					if (typeof el.checkVisibility === "function") {
						return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
					}
					return el.offsetParent !== null;
				}).length;
			} catch (_) {
				return 0;
			}
		};
		const unwrap = (value) => {
			if (value && typeof value === "object") {
				if ("value" in value) return value.value;
				if ("_value" in value) return value._value;
			}
			return value;
		};
		const sizeOf = (value) => {
			value = unwrap(value);
			if (Array.isArray(value)) return value.length;
			if (value && typeof value === "object") return Object.keys(value).length;
			return value ? 1 : 0;
		};
		const state = window.__INITIAL_STATE__ || {};
		const homeFeeds = unwrap(state.feed?.feeds);
		const searchFeeds = unwrap(state.search?.feeds);
		const detailMap = unwrap(state.note?.noteDetailMap);
		const detail = feedID && detailMap && Object.prototype.hasOwnProperty.call(detailMap, feedID)
			? unwrap(detailMap[feedID])
			: null;
		const visible = (el) => {
			if (!el || !el.isConnected) return false;
			if (typeof el.checkVisibility === "function") {
				return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
			}
			const rect = el.getBoundingClientRect();
			const style = window.getComputedStyle(el);
			return style.display !== "none" &&
				style.visibility !== "hidden" &&
				Number(style.opacity || "1") > 0 &&
				rect.width > 1 &&
				rect.height > 1;
		};
		const detailURLMatched = Boolean(feedID && location.href.includes(feedID));
		const visibleDetails = Array.from(document.querySelectorAll(detailSelector)).filter(visible);
		const visibleDetailMatched = Boolean(feedID && visibleDetails.some((el) => {
			const data = JSON.stringify(el.dataset || {});
			const links = Array.from(el.querySelectorAll("a[href]")).map((a) => a.href).join(" ");
			return data.includes(feedID) || links.includes(feedID);
		}));
		const profileData = unwrap(state.user?.userPageData);
		const detailCount = count(detailSelector);
		const text = (document.body?.innerText || "").replace(/\s+/g, " ").slice(0, 1500);
		const riskKeywords = [
			"登录已过期", "登录失效", "请先登录", "请登录", "扫码登录",
			"验证码", "滑块", "安全验证", "请验证", "人机验证",
			"操作频繁", "访问太频繁", "账号异常", "风险提示"
		];
		const risk = riskKeywords.find((keyword) => text.includes(keyword)) || "";
		const riskIndex = risk ? text.indexOf(risk) : -1;
		const riskText = risk
			? text.slice(Math.max(0, riskIndex - 40), Math.min(text.length, riskIndex + 100))
			: "";
		const homeFeedCount = sizeOf(homeFeeds);
		const searchFeedCount = sizeOf(searchFeeds);
		const stateFragment = JSON.stringify({
			homeFeeds: homeFeedCount,
			searchFeeds: searchFeedCount,
			noteMap: sizeOf(detailMap),
			profile: Boolean(profileData),
			feedMatched: Boolean(detail),
		});
		return JSON.stringify({
			url: location.href.slice(0, 300),
			title: document.title.slice(0, 120),
			ready_state: document.readyState,
			scroll_y: Math.round(window.scrollY || document.scrollingElement?.scrollTop || 0),
			app_count: count("#app"),
			feed_card_count: count(feedCardSelector),
			search_input_count: visibleCount(searchInputSelector),
			search_result_count: count(searchResultSelector),
			detail_count: detailCount,
			comment_box_count: visibleCount(commentBoxSelector),
			like_button_count: visibleCount(likeButtonSelector),
			home_feed_count: homeFeedCount,
			search_feed_count: searchFeedCount,
			profile_state: Boolean(profileData),
			detail_state: feedID ? Boolean(detail) : sizeOf(detailMap) > 0,
			detail_feed_matched: feedID ? ((detailURLMatched && visibleDetails.length > 0) || visibleDetailMatched) : detailCount > 0,
			detail_url_matched: detailURLMatched,
			publish_signal_count: count("input[type='file'], .upload-input, .publish-container, .creator-container"),
			state_fragment: stateFragment.slice(0, 220),
			risk_text: riskText.slice(0, 180),
		});
	}`, feedID, SelectorSearchInput, SelectorSearchResult, SelectorFeedCard, SelectorFeedDetailReady, SelectorCommentBox, SelectorLikeButton)
	if err != nil {
		return xhsReadyProbe{}, err
	}
	if obj == nil {
		return xhsReadyProbe{}, fmt.Errorf("ready probe returned nil")
	}

	var probe xhsReadyProbe
	if err := json.Unmarshal([]byte(obj.Value.Str()), &probe); err != nil {
		return xhsReadyProbe{}, err
	}
	return probe, nil
}

func isXHSReady(probe xhsReadyProbe, kind XHSReadyKind, feedID string, allowURLFallback bool) bool {
	switch kind {
	case XHSReadyHome:
		if probe.HomeFeedCount > 0 || probe.FeedCardCount > 0 {
			return true
		}
		return allowURLFallback &&
			probe.AppCount > 0 &&
			isHomeURL(probe.URL) &&
			probe.DetailCount == 0
	case XHSReadySearch:
		if probe.SearchFeedCount > 0 {
			return true
		}
		return allowURLFallback &&
			probe.AppCount > 0 &&
			strings.Contains(probe.URL, "search") &&
			probe.SearchResultCount > 0
	case XHSReadyDetail:
		return detailReady(probe, feedID)
	case XHSReadyProfile:
		if probe.ProfileState {
			return true
		}
		return allowURLFallback &&
			probe.AppCount > 0 &&
			strings.Contains(probe.URL, "/user/profile/")
	case XHSReadyPublish:
		return probe.PublishSignalCount > 0 ||
			(probe.AppCount > 0 && strings.Contains(probe.URL, "publish"))
	case XHSReadyCommentBox:
		return detailReady(probe, feedID) && probe.CommentBoxCount > 0
	default:
		return probe.AppCount > 0
	}
}

func detailReady(probe xhsReadyProbe, feedID string) bool {
	if feedID != "" && !probe.DetailFeedMatched {
		return false
	}
	if feedID != "" {
		return probe.DetailCount > 0 || probe.LikeButtonCount > 0
	}
	return probe.DetailState || probe.DetailCount > 0 || probe.LikeButtonCount > 0
}

func isHomeURL(rawURL string) bool {
	return strings.Contains(rawURL, "xiaohongshu.com") &&
		!strings.Contains(rawURL, "search") &&
		!strings.Contains(rawURL, "/user/profile/") &&
		!strings.Contains(rawURL, "publish") &&
		!isDetailURL(rawURL)
}

func isDetailURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.Contains(rawURL, "/discovery/item") ||
			strings.Contains(rawURL, "/explore/")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "/explore" || path == "" {
		return false
	}
	if path == "/discovery/item" {
		return true
	}
	return strings.HasPrefix(path, "/explore/") ||
		strings.HasPrefix(path, "/discovery/item/")
}

func inferXHSReadyKindFromURL(rawURL string) XHSReadyKind {
	switch {
	case strings.Contains(rawURL, "search"):
		return XHSReadySearch
	case strings.Contains(rawURL, "/user/profile/"):
		return XHSReadyProfile
	case strings.Contains(rawURL, "publish"):
		return XHSReadyPublish
	default:
		return XHSReadyHome
	}
}

func formatXHSReadyProbe(probe xhsReadyProbe) string {
	data, err := json.Marshal(probe)
	if err != nil {
		return fmt.Sprintf("url=%s title=%s readyState=%s state=%s",
			probe.URL, probe.Title, probe.ReadyState, probe.StateFragment)
	}
	return string(data)
}
