package xiaohongshu

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"

	"github.com/go-rod/rod/lib/proto"
)

type XHSReadyKind string

const (
	XHSReadyHome        XHSReadyKind = "home"
	XHSReadyHomeSearch  XHSReadyKind = "home_search"
	XHSReadySearch      XHSReadyKind = "search"
	XHSReadyDetail      XHSReadyKind = "detail"
	XHSReadyProfile     XHSReadyKind = "profile"
	XHSReadyPublish     XHSReadyKind = "publish"
	XHSReadyCommentBox  XHSReadyKind = "comment_box"
	XHSReadyNotification XHSReadyKind = "notification"
)

type XHSReadyOptions struct {
	Kind           XHSReadyKind
	FeedID         string
	Timeout        time.Duration
	SelectorWatchdog *SelectorWatchdog // 非必填，有则 probe 相关选择器
}

const (
	homeSearchStableWindow = 3 * time.Second
	defaultReadyPollMin    = 300 * time.Millisecond
	defaultReadyPollMax    = 500 * time.Millisecond
	homeSearchPollMin      = 800 * time.Millisecond
	homeSearchPollMax      = 1200 * time.Millisecond
)

// xhsReadyStability 记录 HomeSearch 连续就绪时长，满足稳定窗后才算就绪。
type xhsReadyStability struct {
	readySince time.Time
}

func (s *xhsReadyStability) Observe(now time.Time, ready bool) bool {
	if !ready {
		s.readySince = time.Time{}
		return false
	}
	if s.readySince.IsZero() {
		s.readySince = now
		return false
	}
	if now.Sub(s.readySince) < homeSearchStableWindow {
		return false
	}
	return true
}

// xhsReadyPollRange 按 kind 返回轮询间隔范围；HomeSearch 低频以减轻冷启动 CPU 压力。
func xhsReadyPollRange(kind XHSReadyKind) (time.Duration, time.Duration) {
	if kind == XHSReadyHomeSearch {
		return homeSearchPollMin, homeSearchPollMax
	}
	return defaultReadyPollMin, defaultReadyPollMax
}

// xhsReadyDecision 按 kind 决定本轮 probe 结果是否算就绪。
// HomeSearch 走连续稳定窗；其他 kind 单次就绪即成功。
func xhsReadyDecision(kind XHSReadyKind, st *xhsReadyStability, now time.Time, ready bool) bool {
	if kind == XHSReadyHomeSearch {
		return st.Observe(now, ready)
	}
	return ready
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
	VisibleDetailCount int    `json:"visible_detail_count"`
	CommentBoxCount    int    `json:"comment_box_count"`
	LikeButtonCount    int    `json:"like_button_count"`
	HomeFeedCount      int    `json:"home_feed_count"`
	SearchFeedCount    int    `json:"search_feed_count"`
	ProfileState       bool   `json:"profile_state"`
	DetailState        bool   `json:"detail_state"`
	DetailFeedMatched  bool   `json:"detail_feed_matched"`
	DetailURLMatched      bool   `json:"detail_url_matched"`
	PublishSignalCount    int    `json:"publish_signal_count"`
	SearchInputInFeedsReady bool `json:"search_input_in_feeds_ready"`
	NotificationPageCount int    `json:"notification_page_count"`
	NotificationTabCount  int    `json:"notification_tab_count"`
	StateFragment         string `json:"state_fragment,omitempty"`
	RiskText              string `json:"risk_text,omitempty"`
}

// WaitForXHSReady 等待页面就绪，按 kind 判断条件。
// HomeSearch 需连续稳定 homeSearchStableWindow 才返回（低频 800-1200ms 轮询）；
// 其他 kind 首次命中即成功（300-500ms 轮询）。超时后使用最后一次 scoped probe
// 完成 URL fallback 与诊断（对齐 pre，不换 full probe）。同时检测风险信号，发现风险立即返回。
func WaitForXHSReady(page *hrod.Page, opts XHSReadyOptions) error {
	if opts.Kind == "" {
		opts.Kind = XHSReadyHome
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}

	deadline := time.Now().Add(opts.Timeout)
	var last xhsReadyProbe
	var lastErr error
	var stability xhsReadyStability
	pollMin, pollMax := xhsReadyPollRange(opts.Kind)

	for {
		if err := page.Err(); err != nil {
			return err
		}

		probe, err := probeXHSReady(page, opts.Kind, opts.FeedID)
		if err != nil {
			lastErr = err
			stability.Observe(time.Now(), false)
		} else {
			lastErr = nil
			last = probe
			if probe.RiskText != "" {
				return fmt.Errorf("页面出现风险信号: %s; %s", probe.RiskText, formatXHSReadyProbe(probe))
			}
			ready := isXHSReady(probe, opts.Kind, opts.FeedID, false)
			if xhsReadyDecision(opts.Kind, &stability, time.Now(), ready) {
				probeWatchdogSelectors(page, opts)
				return nil
			}
		}

		if !time.Now().Before(deadline) {
			// 超时直接用最后一次 scoped probe 结果完成 URL fallback 与诊断（对齐 pre，不换 full probe）。
			// HomeSearch 不允许 fallback 绕过稳定窗：未连续稳定满窗即使最后一次为 true 也返回 timeout。
			if opts.Kind != XHSReadyHomeSearch && lastErr == nil && isXHSReady(last, opts.Kind, opts.FeedID, true) {
				probeWatchdogSelectors(page, opts)
				return nil
			}
			break
		}
		if err := page.SleepRandom(pollMin, pollMax); err != nil {
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

// probeWatchdogSelectors 在页面就绪后探测相关选择器健康状态。
// 优先使用 opts 中的看门狗，回退到全局 DefaultSelectorWatchdog。
func probeWatchdogSelectors(page *hrod.Page, opts XHSReadyOptions) {
	wd := opts.SelectorWatchdog
	if wd == nil {
		wd = DefaultSelectorWatchdog
	}
	if wd == nil {
		return
	}
	wd.ProbeForKind(page, opts.Kind)
}

func xhsReadyProbeSelectorArgs() []interface{} {
	return []interface{}{
		SelectorSearchInput,
		SelectorSearchResult,
		SelectorFeedCard,
		SelectorFeedDetailReady,
		SelectorCommentBox,
		SelectorLikeButton,
		SelectorSearchInput,
		SelectorNotificationPage,
		SelectorNotificationTab,
	}
}

// probeXHSReady 按 kind 缩小范围的 scoped probe：只计算当前 kind 需要的信号，
// 公共字段（URL/title/readyState/scrollY/app/risk）始终计算。
func probeXHSReady(page *hrod.Page, kind XHSReadyKind, feedID string) (xhsReadyProbe, error) {
	probeJS := `(kind, feedID, searchInputSelector, searchResultSelector, feedCardSelector, detailSelector, commentBoxSelector, likeButtonSelector, searchInputInFeedsSelector, notificationPageSelector, notificationTabSelector) => {` + xhsProbeVisibleJS + xhsProbeFeedMatchJS + xhsProbeCollectionJS + xhsProbeRiskJS + xhsSearchInputReadyJS + `
		const state = window.__INITIAL_STATE__ || {};
		const detailURLMatched = detailURLMatchesFeedID(location.href);
		const text = (document.body?.innerText || "").replace(/\s+/g, " ").slice(0, 1500);
		const riskText = riskOf(text);
		const out = {
			url: location.href.slice(0, 300),
			title: document.title.slice(0, 120),
			ready_state: document.readyState,
			scroll_y: Math.round(window.scrollY || document.scrollingElement?.scrollTop || 0),
			app_count: count("#app"),
			risk_text: riskText.slice(0, 180),
		};
		if (kind === "home" || kind === "home_search") {
			out.home_feed_count = sizeOf(unwrap(state.feed?.feeds));
			out.feed_card_count = count(feedCardSelector);
			out.detail_count = count(detailSelector);
			if (kind === "home_search") {
				out.search_input_in_feeds_ready = searchInputReady(searchInputInFeedsSelector);
			}
		} else if (kind === "search") {
			out.search_feed_count = sizeOf(unwrap(state.search?.feeds));
			out.search_result_count = count(searchResultSelector);
			out.feed_card_count = count(feedCardSelector);
		} else if (kind === "detail" || kind === "comment_box") {
			const detailMap = unwrap(state.note?.noteDetailMap);
			const detail = feedID && detailMap && Object.prototype.hasOwnProperty.call(detailMap, feedID)
				? unwrap(detailMap[feedID])
				: null;
			const detailCount = count(detailSelector);
			const visibleDetails = Array.from(document.querySelectorAll(detailSelector)).filter(visible);
			const visibleDetailMatched = Boolean(feedID && visibleDetails.some(elementMatchesFeedID));
			out.detail_count = detailCount;
			out.visible_detail_count = visibleDetails.length;
			out.detail_url_matched = detailURLMatched;
			out.detail_state = feedID ? Boolean(detail) : sizeOf(detailMap) > 0;
			out.detail_feed_matched = feedID ? ((detailURLMatched && visibleDetails.length > 0) || visibleDetailMatched) : detailCount > 0;
			out.like_button_count = visibleCount(likeButtonSelector);
			if (kind === "comment_box") {
				out.comment_box_count = visibleCount(commentBoxSelector);
			}
		} else if (kind === "profile") {
			out.profile_state = Boolean(unwrap(state.user?.userPageData));
		} else if (kind === "publish") {
			out.publish_signal_count = count("input[type='file'], .upload-input, .publish-container, .creator-container");
		} else if (kind === "notification") {
			out.notification_page_count = count(notificationPageSelector);
			out.notification_tab_count = count(notificationTabSelector);
		}
		return JSON.stringify(out);
	}`
	probeArgs := append([]interface{}{string(kind), feedID}, xhsReadyProbeSelectorArgs()...)
	result, err := evalJSDirect(page.Rod.GetContext(), page, probeJS, probeArgs...)
	return decodeXHSReadyProbe(result, err)
}

// decodeXHSReadyProbe 统一 ready probe 的 nil 检查与 JSON 解码。
func decodeXHSReadyProbe(obj *proto.RuntimeRemoteObject, err error) (xhsReadyProbe, error) {
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

// probeXHSReadyFull 完整 probe：查询全部页面选择器并汇总状态，供推断页面种类使用。
func probeXHSReadyFull(page *hrod.Page, feedID string) (xhsReadyProbe, error) {
	probeJS := `(feedID, searchInputSelector, searchResultSelector, feedCardSelector, detailSelector, commentBoxSelector, likeButtonSelector, searchInputInFeedsSelector, notificationPageSelector, notificationTabSelector) => {` + xhsProbeVisibleJS + xhsProbeFeedMatchJS + xhsProbeCollectionJS + xhsProbeRiskJS + xhsSearchInputReadyJS + `
		const state = window.__INITIAL_STATE__ || {};
		const homeFeeds = unwrap(state.feed?.feeds);
		const searchFeeds = unwrap(state.search?.feeds);
		const detailMap = unwrap(state.note?.noteDetailMap);
		const detail = feedID && detailMap && Object.prototype.hasOwnProperty.call(detailMap, feedID)
			? unwrap(detailMap[feedID])
			: null;
		const detailURLMatched = detailURLMatchesFeedID(location.href);
		const visibleDetails = Array.from(document.querySelectorAll(detailSelector)).filter(visible);
		const visibleDetailMatched = Boolean(feedID && visibleDetails.some(elementMatchesFeedID));
		const profileData = unwrap(state.user?.userPageData);
		const detailCount = count(detailSelector);
		const text = (document.body?.innerText || "").replace(/\s+/g, " ").slice(0, 1500);
		const riskText = riskOf(text);
		const searchInputInFeedsReady = searchInputReady(searchInputInFeedsSelector);
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
			visible_detail_count: visibleDetails.length,
			comment_box_count: visibleCount(commentBoxSelector),
			like_button_count: visibleCount(likeButtonSelector),
			home_feed_count: homeFeedCount,
			search_feed_count: searchFeedCount,
			profile_state: Boolean(profileData),
			detail_state: feedID ? Boolean(detail) : sizeOf(detailMap) > 0,
			detail_feed_matched: feedID ? ((detailURLMatched && visibleDetails.length > 0) || visibleDetailMatched) : detailCount > 0,
			detail_url_matched: detailURLMatched,
			publish_signal_count: count("input[type='file'], .upload-input, .publish-container, .creator-container"),
			search_input_in_feeds_ready: searchInputInFeedsReady,
			notification_page_count: count(notificationPageSelector),
			notification_tab_count: count(notificationTabSelector),
			state_fragment: stateFragment.slice(0, 220),
			risk_text: riskText.slice(0, 180),
		});
	}`
	probeArgs := append([]interface{}{feedID}, xhsReadyProbeSelectorArgs()...)
	obj, err := evalJSDirect(page.Rod.GetContext(), page, probeJS, probeArgs...)
	return decodeXHSReadyProbe(obj, err)
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
	case XHSReadyHomeSearch:
		return isHomeURL(probe.URL) &&
			(probe.HomeFeedCount > 0 || probe.FeedCardCount > 0) &&
			probe.SearchInputInFeedsReady
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
	case XHSReadyNotification:
		// URL fallback 与首个判断等价：都要 page 且 3 个 tab，直接返回。
		return probe.NotificationPageCount > 0 && probe.NotificationTabCount >= 3
	default:
		return probe.AppCount > 0
	}
}

func detailReady(probe xhsReadyProbe, feedID string) bool {
	if feedID == "" {
		return probe.DetailState || probe.DetailCount > 0 || probe.LikeButtonCount > 0
	}
	return probe.DetailFeedMatched
}

func isHomeURL(rawURL string) bool {
	return strings.Contains(rawURL, "xiaohongshu.com") &&
		!strings.Contains(rawURL, "search") &&
		!strings.Contains(rawURL, "/user/profile/") &&
		!strings.Contains(rawURL, "publish") &&
		!strings.Contains(rawURL, "/notification") &&
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
	case strings.Contains(rawURL, "/notification"):
		return XHSReadyNotification
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
