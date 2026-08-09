package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	rodinput "github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

const (
	searchInputWaitTimeout         = 45 * time.Second
	searchResultsWaitTimeout       = 30 * time.Second
	searchFilterRefreshWaitTimeout = 20 * time.Second
	aiResponseWaitTimeout          = 3 * time.Second
	aiResponsePollInterval         = 500 * time.Millisecond
	aiResponseTextLimit            = 20_000
)

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// filterGroup 筛选项分组
type filterGroup struct {
	Label   string
	Options []string
}

// pendingFilter 待定位的筛选项
type pendingFilter struct {
	GroupLabel string
	OptionText string
}

// filterGroups 筛选选项分组定义，按标签组织
var filterGroups = []filterGroup{
	{Label: "排序依据", Options: []string{"综合", "最新", "最多点赞", "最多评论", "最多收藏"}},
	{Label: "笔记类型", Options: []string{"不限", "视频", "图文"}},
	{Label: "发布时间", Options: []string{"不限", "一天内", "一周内", "半年内"}},
	{Label: "搜索范围", Options: []string{"不限", "已看过", "未看过", "已关注"}},
	{Label: "位置距离", Options: []string{"不限", "同城", "附近"}},
}

// collectFilters 校验并展开 FilterOption 为 pendingFilter 列表
func collectFilters(filter FilterOption) ([]pendingFilter, error) {
	var pfs []pendingFilter

	add := func(groupLabel, value string) error {
		if err := validateFilterValue(groupLabel, value); err != nil {
			return fmt.Errorf("%s错误: %w", groupLabel, err)
		}
		pfs = append(pfs, pendingFilter{GroupLabel: groupLabel, OptionText: value})
		return nil
	}

	if filter.SortBy != "" {
		if err := add("排序依据", filter.SortBy); err != nil {
			return nil, err
		}
	}
	if filter.NoteType != "" {
		if err := add("笔记类型", filter.NoteType); err != nil {
			return nil, err
		}
	}
	if filter.PublishTime != "" {
		if err := add("发布时间", filter.PublishTime); err != nil {
			return nil, err
		}
	}
	if filter.SearchScope != "" {
		if err := add("搜索范围", filter.SearchScope); err != nil {
			return nil, err
		}
	}
	if filter.Location != "" {
		if err := add("位置距离", filter.Location); err != nil {
			return nil, err
		}
	}
	return pfs, nil
}

// validateFilterValue 校验取值是否在 filterGroups 中
func validateFilterValue(groupLabel, value string) error {
	for _, g := range filterGroups {
		if g.Label == groupLabel {
			for _, opt := range g.Options {
				if opt == value {
					return nil
				}
			}
			return fmt.Errorf("「%s」不是有效的 %s 取值（有效: %v）", value, groupLabel, g.Options)
		}
	}
	return fmt.Errorf("未知的筛选组: %s", groupLabel)
}

const feedIDsJS = `() => {
	const feeds = window.__INITIAL_STATE__?.search?.feeds;
	if (!feeds) return "";
	const raw = feeds?.value !== undefined ? feeds.value : (feeds?._value !== undefined ? feeds._value : feeds);
	if (!Array.isArray(raw)) return "";
	const ids = raw.slice(0, 30).map(item => {
		const u = item?.id || item?.noteId || item?.note_id || "";
		return String(u);
	}).filter(id => id !== "");
	return ids.join(",");
}`

type SearchAction struct {
	page *hrod.Page
}

func NewSearchAction(page *hrod.Page) *SearchAction {
	return &SearchAction{page: page}
}

func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) (SearchPageResult, error) {
	preSearchCounter := &evalTimeoutCounter{}
	var previousAIState *aiStateProbe
	pageBeforeSearch := s.page.Context(ctx)
	if !isCurrentSearchPage(pageBeforeSearch, keyword) || len(filters) > 0 {
		previousAIState = &aiStateProbe{}
		probe, err := probeAIResponseState(ctx, pageBeforeSearch, preSearchCounter, "", -1)
		if err == nil {
			previousAIState = &probe
		}
	}

	searchCounter := &evalTimeoutCounter{}
	page, feeds, err := s.searchFeeds(ctx, searchCounter, keyword, filters...)
	if err != nil {
		return SearchPageResult{}, err
	}

	result := SearchPageResult{Feeds: feeds}
	postSearchCounter := &evalTimeoutCounter{}
	aiChat, err := readAIResponseFromState(ctx, page, postSearchCounter, previousAIState)
	if err != nil {
		logrus.WithError(err).Warn("读取搜索页 AI 回复失败")
		return result, nil
	}
	result.AIChat = aiChat
	return result, nil
}

func (s *SearchAction) searchFeeds(ctx context.Context, counter *evalTimeoutCounter, keyword string, filters ...FilterOption) (*hrod.Page, []Feed, error) {
	page := s.page.Context(ctx)

	// 导航前校验筛选值，及时报错
	var pfs []pendingFilter
	for _, f := range filters {
		collected, err := collectFilters(f)
		if err != nil {
			return nil, nil, err
		}
		pfs = append(pfs, collected...)
	}

	// 检查当前页面是否已在该关键词搜索结果上，是则跳过搜索
	if !isCurrentSearchPage(page, keyword) {
		if err := s.searchByUI(ctx, page, counter, keyword); err != nil {
			return nil, nil, err
		}
		humanize.Delay(ctx, humanize.AfterNavigate)
	}

	feeds, err := s.collectResults(ctx, page, counter, pfs)
	if err != nil {
		return nil, nil, err
	}
	return page, feeds, nil
}

// SearchFeedsOnly 仅返回笔记列表（不附带 AI 回复），供无需 AI 的搜索路径使用。
func (s *SearchAction) SearchFeedsOnly(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	counter := &evalTimeoutCounter{}
	_, feeds, err := s.searchFeeds(ctx, counter, keyword, filters...)
	if err != nil {
		return nil, err
	}
	return feeds, nil
}

func (s *SearchAction) searchByUI(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, keyword string) error {
	// 使用 Info() 读取 URL（非阻塞），避免在冷启动/blank 页面上执行 DOM Eval。
	searchSelector, err := prepareSearchPage(
		func() string {
			info, infoErr := page.Rod.Info()
			if infoErr != nil || info == nil {
				return ""
			}
			return info.URL
		},
		page.Navigate,
	)
	if err != nil {
		return err
	}

	// 等搜索框出现，不使用WaitLoad因为小红书是SPA。
	input, err := waitForSearchInput(ctx, page, counter, searchInputWaitTimeout, searchSelector)
	if err != nil {
		return fmt.Errorf("未找到搜索框: %w", err)
	}

	if err := input.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击搜索框失败: %w", err)
	}
	// Vue 控制的输入框需要先 JS 清空再键入，否则旧词残留
	if _, err := evalJS(ctx, counter, page, `() => {
		const el = document.activeElement;
		if (el) { el.select(); document.execCommand('delete', false); }
	}`); err != nil {
		return fmt.Errorf("清空搜索框失败: %w", err)
	}
	if err := input.Input(keyword); err != nil {
		return fmt.Errorf("输入关键词失败: %w", err)
	}
	baseline, err := captureSearchResultsBaseline(ctx, page, counter)
	if err != nil {
		return fmt.Errorf("捕获搜索结果基线失败: %w", err)
	}

	if err := page.Actor().Keyboard.Press(rodinput.Enter); err != nil {
		return fmt.Errorf("提交搜索失败: %w", err)
	}

	if err := waitForSearchResultsWithURLFallback(keyword, baseline, searchResultsFallbackHooks{
		wait: func(b searchResultsBaseline) error {
			return waitForSearchResults(ctx, page, counter, keyword, b)
		},
		pageErr:  page.Err,
		navigate: func(url string) error {
			counter.reset()
			return page.Navigate(url)
		},
	}); err != nil {
		return err
	}
	return nil
}

type searchResultsFallbackHooks struct {
	wait     func(searchResultsBaseline) error
	pageErr  func() error
	navigate func(string) error
}

func waitForSearchResultsWithURLFallback(keyword string, baseline searchResultsBaseline, hooks searchResultsFallbackHooks) error {
	err := hooks.wait(baseline)
	if err == nil {
		return nil
	}
	if IsFatalRendererError(err) {
		return err
	}
	if ctxErr := hooks.pageErr(); ctxErr != nil {
		return fmt.Errorf("等待搜索结果失败: %w (context: %w)", err, ctxErr)
	}
	logrus.Warnf("UI搜索结果未就绪，使用搜索URL兜底: %v", err)
	if navErr := hooks.navigate(makeSearchURL(keyword)); navErr != nil {
		return fmt.Errorf("等待搜索结果失败: %w; URL兜底导航失败: %w", err, navErr)
	}
	if waitErr := hooks.wait(searchResultsBaseline{}); waitErr != nil {
		return fmt.Errorf("等待搜索结果失败: %w; URL兜底等待搜索结果失败: %w", err, waitErr)
	}
	return nil
}

type searchResultsBaseline struct {
	StateSignature string
	DOMSignature   string
}

func captureSearchResultsBaseline(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) (searchResultsBaseline, error) {
	probe, err := probeSearchResultsKeyword(ctx, page, counter, "")
	if err != nil {
		return searchResultsBaseline{}, err
	}
	return searchResultsBaseline{
		StateSignature: probe.StateSignature,
		DOMSignature:   probe.DOMSignature,
	}, nil
}

func waitForSearchResults(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, keyword string, baseline searchResultsBaseline) error {
	deadline := time.Now().Add(searchResultsWaitTimeout)
	var last searchResultsKeywordProbe
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return err
		}

		probe, err := probeSearchResultsKeyword(ctx, page, counter, keyword)
		if err != nil {
			if IsFatalRendererError(err) {
				return err
			}
			lastErr = err
		} else {
			last = probe
			lastErr = nil
			if searchResultsReady(probe, baseline) {
				return nil
			}
		}

		if err := page.Sleep(300 * time.Millisecond); err != nil {
			return err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("等待搜索结果超时(%s): %w", searchResultsWaitTimeout, lastErr)
	}
	if last.HasStateKeyword && !last.KeywordMatched {
		return fmt.Errorf("搜索结果关键词不匹配: expected=%q state_keyword=%q url_keyword=%q input_keyword=%q visible_cards=%v",
			keyword, last.StateKeyword, last.URLKeyword, last.InputKeyword, last.HasVisibleCards)
	}
	if last.HasStateKeyword && !last.HasStateFeeds {
		return fmt.Errorf("搜索状态结果未加载: keyword=%q state_keyword=%q", keyword, last.StateKeyword)
	}
	return fmt.Errorf("搜索结果未加载: keyword=%q state_keyword=%q url_keyword=%q input_keyword=%q visible_cards=%v",
		keyword, last.StateKeyword, last.URLKeyword, last.InputKeyword, last.HasVisibleCards)
}

type searchResultsKeywordProbe struct {
	StateKeyword     string `json:"state_keyword"`
	HasStateKeyword  bool   `json:"has_state_keyword"`
	KeywordMatched   bool   `json:"keyword_matched"`
	URLKeyword       string `json:"url_keyword"`
	HasURLKeyword    bool   `json:"has_url_keyword"`
	URLKeywordMatched bool   `json:"url_keyword_matched"`
	InputKeyword     string `json:"input_keyword"`
	InputMatched     bool   `json:"input_matched"`
	OnSearchPage     bool   `json:"on_search_page"`
	HasStateFeeds    bool   `json:"has_state_feeds"`
	HasVisibleCards  bool   `json:"has_visible_cards"`
	StateSignature   string `json:"state_signature"`
	DOMSignature     string `json:"dom_signature"`
}

func probeSearchResultsKeyword(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, keyword string) (searchResultsKeywordProbe, error) {
	obj, err := evalJS(ctx, counter, page, `(keyword, feedCardSelector, searchInputSelector, markedSearchInputSelector) => {
		const unwrap = (value) => {
			if (value && typeof value === "object") {
				if ("value" in value) return value.value;
				if ("_value" in value) return value._value;
			}
			return value;
		};
		const normalize = (value) => String(value ?? "").trim();
		const noteIDFromHref = (href) => {
			const match = String(href || "").match(/\/(?:explore|discovery\/item)\/([^/?#]+)/);
			return match ? decodeURIComponent(match[1]) : "";
		};
		const visible = (el) => {
			if (!el || !el.isConnected) return false;
			const style = window.getComputedStyle(el);
			const rect = el.getBoundingClientRect();
			return style.display !== "none" &&
				style.visibility !== "hidden" &&
				Number(style.opacity || "1") > 0 &&
				rect.width > 1 &&
				rect.height > 1;
		};
		const inputValue = (el) => normalize("value" in el ? el.value : (el.innerText || el.textContent));
		const stateSignature = (items) => {
			if (!Array.isArray(items) || items.length === 0) return "";
			return JSON.stringify(items.slice(0, 6).map((item) => {
				item = unwrap(item) || {};
				const noteCard = unwrap(item.noteCard) || {};
				return [
					item.id || item.noteId || item.note_id || noteCard.noteId || "",
					noteCard.displayTitle || item.title || item.desc || "",
				].join("|");
			}));
		};
		const domSignature = () => {
			const cards = Array.from(document.querySelectorAll(feedCardSelector)).filter(visible).slice(0, 6);
			if (cards.length === 0) return "";
			return JSON.stringify(cards.map((card) => {
				const link = Array.from(card.querySelectorAll("a[href]"))
					.find((a) => /\/(?:explore|discovery\/item)\//.test(a.href));
				const href = link?.href || "";
				const title = normalize(card.querySelector(".title, .note-title, [class*='title']")?.textContent || link?.textContent || "");
				return [
					card.dataset?.noteId || card.dataset?.id || noteIDFromHref(href),
					title,
					href,
				].join("|");
			}));
		};
		const urlKeyword = () => {
			try {
				const params = new URL(location.href).searchParams;
				for (const name of ["keyword", "search_key", "query", "q"]) {
					const raw = params.get(name);
					if (raw) {
						const decoded = raw.includes("%") ? (() => { try { return decodeURIComponent(raw); } catch (_) { return raw; } })() : raw;
						return normalize(decoded);
					}
				}
			} catch (_) {}
			return "";
		};
		const search = window.__INITIAL_STATE__?.search;
		const stateKeyword = unwrap(search?.searchKeyword);
		const stateKeywordText = normalize(stateKeyword);
		const urlKeywordText = urlKeyword();
		const markedInput = document.querySelector(markedSearchInputSelector);
		const searchInput = markedInput || Array.from(document.querySelectorAll(searchInputSelector)).find(visible);
		const inputKeyword = searchInput ? inputValue(searchInput) : "";
		const feeds = unwrap(search?.feeds);
		const hasStateFeeds = Array.isArray(feeds) && feeds.length > 0;
		const domSig = domSignature();
		return JSON.stringify({
			state_keyword: stateKeywordText.slice(0, 120),
			has_state_keyword: stateKeywordText !== "",
			keyword_matched: stateKeywordText === "" || stateKeywordText === normalize(keyword),
			url_keyword: urlKeywordText.slice(0, 120),
			has_url_keyword: urlKeywordText !== "",
			url_keyword_matched: urlKeywordText !== "" && urlKeywordText === normalize(keyword),
			input_keyword: inputKeyword.slice(0, 120),
			input_matched: inputKeyword !== "" && inputKeyword === normalize(keyword),
			on_search_page: /\/search/i.test(location.pathname),
			has_state_feeds: hasStateFeeds,
			has_visible_cards: domSig !== "",
			state_signature: stateSignature(feeds).slice(0, 500),
			dom_signature: domSig.slice(0, 500),
		});
	}`, keyword, SelectorFeedCard, SelectorSearchInput, SelectorSelectedSearchInput)
	if err != nil {
		return searchResultsKeywordProbe{}, err
	}
	if obj == nil {
		return searchResultsKeywordProbe{}, fmt.Errorf("搜索结果关键词探测无返回")
	}

	var probe searchResultsKeywordProbe
	if err := json.Unmarshal([]byte(obj.Value.Str()), &probe); err != nil {
		return searchResultsKeywordProbe{}, err
	}
	return probe, nil
}

func searchResultsReady(probe searchResultsKeywordProbe, baseline searchResultsBaseline) bool {
	stateReady := probe.HasStateKeyword &&
		probe.KeywordMatched &&
		probe.HasStateFeeds &&
		signatureChanged(probe.StateSignature, baseline.StateSignature)
	domReady := probe.URLKeywordMatched &&
		probe.HasVisibleCards &&
		signatureChanged(probe.DOMSignature, baseline.DOMSignature)
	inputReady := probe.InputMatched &&
		probe.HasVisibleCards &&
		(probe.OnSearchPage || baseline.StateSignature != "" || baseline.DOMSignature != "") &&
		searchResultsChanged(probe, baseline)
	return stateReady || domReady || inputReady
}

func signatureChanged(current, baseline string) bool {
	return baseline == "" || (current != "" && current != baseline)
}

func searchResultsChanged(probe searchResultsKeywordProbe, baseline searchResultsBaseline) bool {
	if baseline.StateSignature == "" && baseline.DOMSignature == "" {
		return true
	}
	if baseline.StateSignature != "" && probe.StateSignature != "" && probe.StateSignature != baseline.StateSignature {
		return true
	}
	if baseline.DOMSignature != "" && probe.DOMSignature != "" && probe.DOMSignature != baseline.DOMSignature {
		return true
	}
	return false
}

// findFilterOption 按组标签+选项文本定位筛选项，返回 hrod 元素。
func findFilterOption(page *hrod.Page, pf pendingFilter) (*hrod.Element, error) {
	groups, err := page.Elements("div.filter-panel div.filters")
	if err != nil {
		return nil, fmt.Errorf("查找筛选组失败: %w", err)
	}
	for _, group := range groups {
		label, err := group.Element(":scope > span")
		if err != nil {
			continue
		}
		text, err := label.Text()
		if err != nil {
			continue
		}
		if strings.TrimSpace(text) != pf.GroupLabel {
			continue
		}
		tags, err := group.Elements("div.tags")
		if err != nil || len(tags) == 0 {
			return nil, fmt.Errorf("「%s」没有选项", pf.GroupLabel)
		}
		for _, tag := range tags {
			t, err := tag.Text()
			if err != nil {
				continue
			}
			if strings.TrimSpace(t) == pf.OptionText {
				return tag, nil
			}
		}
		return nil, fmt.Errorf("「%s」里没有「%s」", pf.GroupLabel, pf.OptionText)
	}
	return nil, fmt.Errorf("筛选面板里没有「%s」组", pf.GroupLabel)
}

// readFeedIDs 从 __INITIAL_STATE__ 读取当前搜索结果 feed ID 列表
func readFeedIDs(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) (string, error) {
	result, err := evalJS(ctx, counter, page, feedIDsJS)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return result.Value.Str(), nil
}

// waitFeedsChanged 轮询等待搜索结果 ID 列表发生变化
func waitFeedsChanged(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, before string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return false
		}
		after, err := readFeedIDs(ctx, page, counter)
		if err == nil && after != "" && after != before {
			return true
		}
		if err := page.Sleep(300 * time.Millisecond); err != nil {
			return false
		}
	}
	return false
}

type searchInputProbe struct {
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	ReadyState         string   `json:"readyState"`
	HasApp             bool     `json:"hasApp"`
	HasSearchInput     bool     `json:"hasSearchInput"`
	SearchInputVisible bool     `json:"searchInputVisible"`
	InputSummary       []string `json:"inputSummary"`
	BodyText           string   `json:"bodyText"`
}

func waitForSearchInput(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, timeout time.Duration, searchSelector string) (*hrod.Element, error) {
	deadline := time.Now().Add(timeout)
	var last searchInputProbe
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return nil, err
		}

		probe, err := probeSearchInput(ctx, page, counter, searchSelector, SelectorSearchInputInFeeds+", "+SelectorSearchInputInSearchResult)
		if err != nil {
			lastErr = err
		} else {
			last = probe
			if probe.HasSearchInput && probe.SearchInputVisible {
				input, err := page.Element(SelectorSelectedSearchInput)
				if err == nil {
					return input, nil
				}
				lastErr = err
			}
		}

		if err := page.Sleep(300 * time.Millisecond); err != nil {
			return nil, err
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("等待搜索框超时(%s): %w; %s", timeout, lastErr, formatSearchInputProbe(last))
	}
	return nil, fmt.Errorf("等待搜索框超时(%s): %s", timeout, formatSearchInputProbe(last))
}

func probeSearchInput(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, searchSelector, primarySelector string) (searchInputProbe, error) {
	obj, err := evalJS(ctx, counter, page, `(searchSelector, primarySelector) => {
		const visible = (el) => {
			if (!el || !el.isConnected) return false;
			const style = window.getComputedStyle(el);
			const rect = el.getBoundingClientRect();
			return style.display !== "none" &&
				style.visibility !== "hidden" &&
				Number(style.opacity || "1") > 0 &&
				rect.width > 1 &&
				rect.height > 1 &&
				rect.bottom > 0 &&
				rect.right > 0 &&
				rect.top < window.innerHeight &&
				rect.left < window.innerWidth;
		};
		const label = (el) => [
			el.tagName,
			el.getAttribute("type") || "",
			el.getAttribute("class") || "",
			el.getAttribute("id") || "",
			el.getAttribute("placeholder") || "",
			el.getAttribute("aria-label") || "",
			el.getAttribute("role") || "",
			el.getAttribute("data-placeholder") || "",
			(el.innerText || "").slice(0, 40),
		].join(" ");
		document.querySelectorAll('[data-xhs-mcp-search-input="1"], [data-xhs-mcp-search-input="selected"]').forEach((el) => {
			el.removeAttribute("data-xhs-mcp-search-input");
		});
		// 优先查稳定 ID 选择器，查不到再查 placeholder 兜底
		const clickHit = (el) => {
			const rect = el.getBoundingClientRect();
			const x = Math.min(Math.max(rect.left + rect.width / 2, 1), window.innerWidth - 1);
			const y = Math.min(Math.max(rect.top + rect.height / 2, 1), window.innerHeight - 1);
			const hit = document.elementFromPoint(x, y);
			return !!hit && (hit === el || el.contains(hit));
		};
		let searchInput = Array.from(document.querySelectorAll(primarySelector)).find((el) => visible(el) && clickHit(el));
		if (!searchInput) {
			searchInput = Array.from(document.querySelectorAll(searchSelector)).find((el) => visible(el) && clickHit(el));
		}
		if (searchInput) {
			searchInput.setAttribute("data-xhs-mcp-search-input", "selected");
		}
		const inputs = Array.from(document.querySelectorAll('input, textarea, [contenteditable="true"]'))
			.slice(0, 8)
			.map((el) => label(el).replace(/\s+/g, " ").trim() + " visible=" + visible(el));
		return JSON.stringify({
			url: location.href,
			title: document.title,
			readyState: document.readyState,
			hasApp: !!document.querySelector("#app"),
			hasSearchInput: !!searchInput,
			searchInputVisible: !!searchInput && visible(searchInput),
			inputSummary: inputs,
			bodyText: (document.body?.innerText || "").replace(/\s+/g, " ").slice(0, 180),
		});
		}`, searchSelector, primarySelector)
	if err != nil {
		return searchInputProbe{}, err
	}
	if obj == nil {
		return searchInputProbe{}, fmt.Errorf("搜索框探测无返回")
	}

	var probe searchInputProbe
	if err := json.Unmarshal([]byte(obj.Value.Str()), &probe); err != nil {
		return searchInputProbe{}, err
	}
	return probe, nil
}

func formatSearchInputProbe(probe searchInputProbe) string {
	data, err := json.Marshal(probe)
	if err != nil {
		return fmt.Sprintf("url=%s title=%s readyState=%s hasApp=%v hasSearchInput=%v",
			probe.URL, probe.Title, probe.ReadyState, probe.HasApp, probe.HasSearchInput)
	}
	return string(data)
}

func (s *SearchAction) collectResults(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, pfs []pendingFilter) ([]Feed, error) {
	appliedFilters := false
	var stateRefreshed bool

	// 如果有筛选条件，则应用筛选
	if len(pfs) > 0 {
		stageErr := func(stage string, t0 time.Time, err error, tag string) error {
			fields := logrus.Fields{
				"stage":      stage,
				"elapsed_ms": time.Since(t0).Milliseconds(),
				"error":      err.Error(),
			}
			if tag != "" {
				fields["tag"] = tag
			}
			logrus.WithFields(fields).Error("筛选阶段失败")
			return fmt.Errorf("筛选阶段 %s 失败", stage)
		}

		filterCtx, cancel := context.WithTimeout(page.Actor().Ctx(), searchFilterRefreshWaitTimeout)
		defer cancel()
		filterPage := page.Context(filterCtx)

		t0 := time.Now()
		filterButton, err := filterPage.Element("div.filter")
		if err != nil {
			return nil, stageErr("filter_button_lookup", t0, err, "")
		}

		t0 = time.Now()
		if err := filterButton.Hover(); err != nil {
			return nil, stageErr("filter_button_hover", t0, err, "")
		}
		humanize.Delay(filterCtx, humanize.BeforeClick)

		t0 = time.Now()
		for {
			panel, panelErr := evalJS(filterCtx, counter, filterPage, `() => document.querySelector('.filter-panel') !== null`)
			if panelErr != nil {
				return nil, stageErr("filter_panel_wait", t0, panelErr, "")
			}
			if panel != nil && panel.Value.Bool() {
				break
			}
			if sleepErr := filterPage.Sleep(300 * time.Millisecond); sleepErr != nil {
				return nil, stageErr("filter_panel_wait", t0, sleepErr, "")
			}
		}

		before, _ := readFeedIDs(ctx, page, counter)

		for _, pf := range pfs {
			option, err := findFilterOption(page, pf)
			if err != nil {
				return nil, stageErr("filter_option_lookup", time.Now(), err, pf.OptionText)
			}

			// 点击前拟人延迟（跟进上游 humanize.Delay + BeforeClick）
			humanize.Delay(filterCtx, humanize.BeforeClick)
			if err := filterCtx.Err(); err != nil {
				return nil, stageErr("filter_option_delay", time.Now(), err, pf.OptionText)
			}

			t0 = time.Now()
			if err := humanize.ClickNoWait(option.Rod); err != nil {
				return nil, stageErr("filter_option_click", t0, err, pf.OptionText)
			}
		}

		stateRefreshed = waitFeedsChanged(ctx, page, counter, before, searchFilterRefreshWaitTimeout)
		appliedFilters = true
	}

	sources, sourceErr := extractSearchFeedSources(ctx, page, counter, true)
	domFeeds, stateFeeds := sources.DOM, sources.State
	domErr, stateErr := sourceErr, sourceErr
	if sourceErr == nil && len(domFeeds) == 0 {
		domErr = errors.ErrNoFeeds
	}
	if sourceErr == nil && len(stateFeeds) == 0 {
		stateErr = errors.ErrNoFeeds
	}

	if appliedFilters {
		if stateRefreshed && stateErr == nil && len(stateFeeds) > 0 {
			return mergeFeedsByID(stateFeeds, domFeeds), nil
		}
		if domErr == nil && len(domFeeds) > 0 {
			return domFeeds, nil
		}
		if domErr != nil {
			return nil, domErr
		}
		return nil, stateErr
	}

	if domErr == nil && len(domFeeds) > 0 {
		return mergeFeedsByID(domFeeds, stateFeeds), nil
	}
	if stateErr == nil && len(stateFeeds) > 0 {
		return stateFeeds, nil
	}
	if domErr != nil {
		return nil, domErr
	}
	return nil, stateErr
}

func collectSearchFeeds(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) ([]Feed, error) {
	sources, sourceErr := extractSearchFeedSources(ctx, page, counter, true)
	domFeeds, stateFeeds := sources.DOM, sources.State
	domErr, stateErr := sourceErr, sourceErr
	if sourceErr == nil && len(domFeeds) == 0 {
		domErr = errors.ErrNoFeeds
	}
	if sourceErr == nil && len(stateFeeds) == 0 {
		stateErr = errors.ErrNoFeeds
	}
	if domErr == nil && len(domFeeds) > 0 {
		return mergeFeedsByID(domFeeds, stateFeeds), nil
	}
	if stateErr == nil && len(stateFeeds) > 0 {
		return stateFeeds, nil
	}
	if domErr != nil {
		return nil, domErr
	}
	return nil, stateErr
}

type aiStateProbe struct {
	OneboxInfo         json.RawMessage `json:"onebox_info"`
	DQAInstantElements json.RawMessage `json:"dqa_instant_elements"`
	Active             bool            `json:"active"`
	SearchRoundID      json.RawMessage `json:"search_round_id"`
	UserMessageID      json.RawMessage `json:"user_message_id"`
	DomAIMessageID     string          `json:"dom_ai_message_id"`
	DomAITextLength    int             `json:"dom_ai_text_length"`
	DomAIText          string          `json:"dom_ai_text"`
}

func readAIResponseFromState(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, previousState *aiStateProbe) (*AIChatReply, error) {
	if previousState == nil {
		deadline := time.Now().Add(aiResponseWaitTimeout)
		var lastReply *AIChatReply
		previousDOMMessageID := ""
		previousDOMTextLength := -1
		for {
			if !time.Now().Before(deadline) {
				return lastReply, nil
			}
			probe, err := probeAIResponseState(ctx, page, counter, previousDOMMessageID, previousDOMTextLength)
			if err != nil {
				if IsFatalRendererError(err) {
					return nil, err
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return lastReply, nil
			}
			previousDOMMessageID = probe.DomAIMessageID
			previousDOMTextLength = probe.DomAITextLength
			reply, pending := normalizeAIResponse(probe)
			if reply != nil {
				lastReply = reply
			}
			if !pending {
				return lastReply, nil
			}
			sleepFor := min(aiResponsePollInterval, time.Until(deadline))
			if err := page.Sleep(sleepFor); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return lastReply, nil
			}
		}
	}

	// 非 AI 激活搜索 → 读一次即可（没有 AI 内容）
	if !previousState.Active {
		probe, err := probeAIResponseState(ctx, page, counter, previousState.DomAIMessageID, previousState.DomAITextLength)
		if err != nil {
			return nil, err
		}
		reply, _ := normalizeAIResponse(probe)
		return reply, nil
	}

	// AI 激活状态 → 有界轮询等待回复
	deadline := time.Now().Add(aiResponseWaitTimeout)
	var lastReply *AIChatReply
	var prevRoundID, prevMsgID string
	previousDOMMessageID := previousState.DomAIMessageID
	previousDOMTextLength := previousState.DomAITextLength
	if previousState.SearchRoundID != nil {
		json.Unmarshal(previousState.SearchRoundID, &prevRoundID)
	}
	if previousState.UserMessageID != nil {
		json.Unmarshal(previousState.UserMessageID, &prevMsgID)
	}

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return lastReply, nil
		}
		probe, err := probeAIResponseState(ctx, page, counter, previousDOMMessageID, previousDOMTextLength)
		if err != nil {
			if IsFatalRendererError(err) {
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if lastReply != nil {
				return lastReply, nil
			}
			return nil, err
		}
		domChanged := probe.DomAIMessageID != previousDOMMessageID || probe.DomAITextLength != previousDOMTextLength
		previousDOMMessageID = probe.DomAIMessageID
		previousDOMTextLength = probe.DomAITextLength

		// 用 round/message ID 判断是否有新回复，不依赖正文内容
		var curRoundID, curMsgID string
		if probe.SearchRoundID != nil {
			json.Unmarshal(probe.SearchRoundID, &curRoundID)
		}
		if probe.UserMessageID != nil {
			json.Unmarshal(probe.UserMessageID, &curMsgID)
		}
		idsChanged := curRoundID != prevRoundID || curMsgID != prevMsgID

		reply, pending := normalizeAIResponse(probe)
		if (idsChanged || domChanged) && reply != nil {
			lastReply = reply
		}
		if !pending {
			return lastReply, nil
		}
		if !time.Now().Before(deadline) {
			return lastReply, nil
		}
		sleepFor := min(aiResponsePollInterval, time.Until(deadline))
		if err := page.Sleep(sleepFor); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if lastReply != nil {
				return lastReply, nil
			}
			return nil, err
		}
	}
}

func probeAIResponseState(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, previousDOMMessageID string, previousDOMTextLength int) (aiStateProbe, error) {
	result, err := evalJS(ctx, counter, page, `(previousDOMMessageID, previousDOMTextLength, textLimit) => {
		const hasOwn = (value, key) => Object.prototype.hasOwnProperty.call(value, key);
		const isObject = (value) => value !== null && typeof value === "object";

		const unwrapRef = (value) => {
			const seen = new Set();
			let current = value;
			while (isObject(current) && !seen.has(current)) {
				seen.add(current);
				if ((current.__v_isReactive || current.__v_isReadonly) &&
					isObject(current.__v_raw) && current.__v_raw !== current) {
					current = current.__v_raw;
					continue;
				}
				if (current.__v_isRef === true) {
					const next = current.value;
					if (next === current) break;
					current = next;
					continue;
				}
				if (hasOwn(current, "_value")) {
					const next = current._value;
					if (next === current) break;
					current = next;
					continue;
				}
				if (hasOwn(current, "value")) {
					const next = current.value;
					if (next === current) break;
					current = next;
					continue;
				}
				break;
			}
			return current;
		};

		const essentialKey = (key) => {
			const normalized = key.toLowerCase().replaceAll("_", "");
			return ["content", "desc", "pending", "done", "status", "round", "searchroundid", "usermessageid", "roundid", "messageid", "id"].includes(normalized);
		};
		const essentialMemo = new WeakMap();
		const containsEssentialKey = (value, visiting = new Set()) => {
			value = unwrapRef(value);
			if (!isObject(value)) return false;
			if (essentialMemo.has(value)) return essentialMemo.get(value);
			if (visiting.has(value)) return false;
			visiting.add(value);
			let found = false;
			for (const [key, child] of Object.entries(value)) {
				if (essentialKey(key) || containsEssentialKey(child, visiting)) {
					found = true;
					break;
				}
			}
			visiting.delete(value);
			essentialMemo.set(value, found);
			return found;
		};
		const cloneComplete = (value, seen = new Set()) => {
			value = unwrapRef(value);
			if (typeof value === "string" || typeof value === "boolean" || typeof value === "number" || value == null) return value;
			if (!isObject(value) || seen.has(value)) return undefined;
			seen.add(value);
			if (Array.isArray(value)) {
				const items = value.map((item) => cloneComplete(item, seen)).filter((item) => item !== undefined);
				seen.delete(value);
				return items;
			}
			const cloned = {};
			for (const [key, child] of Object.entries(value)) {
				const nested = cloneComplete(child, seen);
				if (nested !== undefined) cloned[key] = nested;
			}
			seen.delete(value);
			return cloned;
		};
		const project = (value, depth = 0, seen = new Set()) => {
			value = unwrapRef(value);
			if (typeof value === "string") return value.slice(0, 3000);
			if (typeof value === "boolean" || typeof value === "number" || value == null) return value;
			if (!isObject(value) || seen.has(value)) return undefined;
			seen.add(value);
			if (Array.isArray(value)) {
				const items = [];
				for (const item of value) {
					const hasEssential = containsEssentialKey(item);
					if ((!hasEssential && depth >= 8) || (!hasEssential && items.length >= 50)) continue;
					const nested = project(item, depth + 1, seen);
					if (nested !== undefined) items.push(nested);
				}
				seen.delete(value);
				return items;
			}
			const exact = new Set([
				"content", "answer", "summary", "text", "markdown", "desc", "elements", "items", "list",
				"children", "data", "result", "response", "message", "onebox", "dqa", "hasmore",
				"isgenerating", "generating", "loading", "streaming", "isend", "finished", "completed", "done", "status",
			]);
			const projected = {};
			for (const [key, child] of Object.entries(value)) {
				const normalized = key.toLowerCase().replaceAll("_", "");
				if (essentialKey(key)) {
					const nested = cloneComplete(child);
					if (nested !== undefined) projected[key] = nested;
					continue;
				}
				const hasEssential = containsEssentialKey(child);
				if (!hasEssential && depth >= 8) continue;
				if (!hasEssential && !exact.has(normalized) && !["content", "answer", "summary", "element", "message"].some((part) => normalized.includes(part))) continue;
				const nested = project(child, depth + 1, seen);
				if (nested !== undefined) projected[key] = nested;
			}
			seen.delete(value);
			return projected;
		};

		const search = unwrapRef(window.__INITIAL_STATE__?.search);

		// 页面版本不同，AI 字段可能位于 search 或其 AI 子状态中。
		const roots = search ? [
			search,
			unwrapRef(search.aiSearch),
			unwrapRef(search.aiWendian),
			unwrapRef(search.wendian),
			unwrapRef(search.aiChat),
		].filter(isObject) : [];
		const pick = (...names) => {
			for (const root of roots) {
				for (const name of names) {
					if (hasOwn(root, name)) return unwrapRef(root[name]);
				}
			}
			return undefined;
		};

		const domAI = (() => {
			try {
				const messages = document.querySelectorAll('.ai-message-finished, .ai-message');
				const aiMsg = messages.length > 0 ? messages[messages.length - 1] : null;
				if (aiMsg) {
					const text = aiMsg.textContent.trim();
					if (text.length > 50) {
						const messageID = aiMsg.getAttribute("data-message-id") || aiMsg.getAttribute("data-msg-id") || aiMsg.id || "";
						const textLength = Math.min(text.length, textLimit);
						return {
							messageID,
							textLength,
							text: messageID !== previousDOMMessageID || textLength !== previousDOMTextLength ? text.slice(0, textLimit) : "",
						};
					}
				}
				const scrollBody = document.querySelector('.ai-chat-scroll-body');
				if (scrollBody) {
					const text = scrollBody.textContent.trim();
					if (text.length > 50) {
						const messageID = scrollBody.getAttribute("data-message-id") || scrollBody.id || "";
						const textLength = Math.min(text.length, textLimit);
						return {
							messageID,
							textLength,
							text: messageID !== previousDOMMessageID || textLength !== previousDOMTextLength ? text.slice(0, textLimit) : "",
						};
					}
				}
				return {messageID: "", textLength: 0, text: ""};
			} catch (e) { return {messageID: "", textLength: 0, text: ""}; }
		})();

		return JSON.stringify({
			onebox_info: project(pick("oneboxInfo", "oneBoxInfo")),
			dqa_instant_elements: project(pick("dqaInstantElements", "dqaElements")),
			active: Boolean(pick("aiWendianActive", "wendianActive", "aiActive")),
			search_round_id: cloneComplete(pick("currentSearchRoundId", "searchRoundId")),
			user_message_id: cloneComplete(pick("currentUserMessageId", "userMessageId")),
			dom_ai_message_id: domAI.messageID,
			dom_ai_text_length: domAI.textLength,
			dom_ai_text: domAI.text,
		});
	}`, previousDOMMessageID, previousDOMTextLength, aiResponseTextLimit)
	if err != nil {
		return aiStateProbe{}, fmt.Errorf("提取搜索页 AI 状态失败: %w", err)
	}
	if result == nil || result.Value.Str() == "" {
		return aiStateProbe{}, nil
	}

	var probe aiStateProbe
	if err := json.Unmarshal([]byte(result.Value.Str()), &probe); err != nil {
		return aiStateProbe{}, fmt.Errorf("解析搜索页 AI 状态失败: %w", err)
	}
	return probe, nil
}

func normalizeAIResponse(probe aiStateProbe) (*AIChatReply, bool) {
	dqa := decodeAIStateValue(probe.DQAInstantElements)
	onebox := decodeAIStateValue(probe.OneboxInfo)
	content := firstNonEmpty(probe.DomAIText, extractAIText(dqa), extractAIText(onebox))
	hasMore := aiStateHasMore(dqa) || aiStateHasMore(onebox)
	if content == "" {
		return nil, probe.Active || hasMore
	}
	return &AIChatReply{Content: content, HasMore: hasMore}, hasMore
}

func decodeAIStateValue(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

func extractAIText(value any) string {
	parts := extractAITextParts(value)
	seen := make(map[string]bool, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		result = append(result, part)
	}
	return strings.Join(result, "\n")
}

func extractAITextParts(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var parts []string
		for _, item := range typed {
			parts = append(parts, extractAITextParts(item)...)
		}
		return parts
	case map[string]any:
		textKeys := []string{"content", "answer", "summary", "text", "markdown", "desc"}
		if parts := extractAIMapKeys(typed, textKeys); hasAIText(parts) {
			return parts
		}
		containerKeys := []string{
			"elements", "items", "list", "children", "data", "result",
			"response", "message", "onebox", "dqa",
		}
		if parts := extractAIMapKeys(typed, containerKeys); hasAIText(parts) {
			return parts
		}

		keys := make([]string, 0, len(typed))
		for key := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "content") ||
				strings.Contains(lower, "answer") ||
				strings.Contains(lower, "summary") ||
				strings.Contains(lower, "element") ||
				strings.Contains(lower, "message") {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		return extractAIMapKeys(typed, keys)
	default:
		return nil
	}
}

func hasAIText(parts []string) bool {
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			return true
		}
	}
	return false
}

func extractAIMapKeys(value map[string]any, keys []string) []string {
	var parts []string
	for _, wanted := range keys {
		for key, nested := range value {
			if strings.EqualFold(key, wanted) {
				parts = append(parts, extractAITextParts(nested)...)
				break
			}
		}
	}
	return parts
}

func aiStateHasMore(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if aiStateHasMore(item) {
				return true
			}
		}
	case map[string]any:
		for key, nested := range typed {
			normalizedKey := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			switch normalizedKey {
			case "hasmore", "isgenerating", "generating", "loading", "streaming":
				if flag, ok := nested.(bool); ok && flag {
					return true
				}
			case "isend", "finished", "completed", "done":
				if flag, ok := nested.(bool); ok && !flag {
					return true
				}
			case "status":
				if status, ok := nested.(string); ok {
					switch strings.ToLower(status) {
					case "pending", "generating", "streaming", "loading":
						return true
					}
				}
			}
			if aiStateHasMore(nested) {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func mergeFeedsByID(primary, fallback []Feed) []Feed {
	byID := make(map[string]Feed, len(fallback))
	for _, feed := range fallback {
		if feed.ID != "" {
			byID[feed.ID] = feed
		}
	}
	result := make([]Feed, 0, len(primary)+len(fallback))
	seen := make(map[string]bool, len(primary))
	for _, feed := range primary {
		if other, ok := byID[feed.ID]; ok {
			fillMissingFeedFields(&feed, other)
		}
		result = append(result, feed)
		if feed.ID != "" {
			seen[feed.ID] = true
		}
	}
	for _, feed := range fallback {
		if feed.ID == "" || seen[feed.ID] {
			continue
		}
		result = append(result, feed)
		seen[feed.ID] = true
	}
	return result
}

func fillMissingFeedFields(dst *Feed, src Feed) {
	if dst.ID == "" {
		dst.ID = src.ID
	}
	if dst.XsecToken == "" {
		dst.XsecToken = src.XsecToken
	}
	if dst.ModelType == "" {
		dst.ModelType = src.ModelType
	}
	if dst.NoteCard.Type == "" {
		dst.NoteCard.Type = src.NoteCard.Type
	}
	if dst.NoteCard.DisplayTitle == "" {
		dst.NoteCard.DisplayTitle = src.NoteCard.DisplayTitle
	}
	if dst.NoteCard.User.UserID == "" {
		dst.NoteCard.User.UserID = src.NoteCard.User.UserID
	}
	if dst.NoteCard.User.Nickname == "" {
		dst.NoteCard.User.Nickname = src.NoteCard.User.Nickname
	}
	if dst.NoteCard.User.NickName == "" {
		dst.NoteCard.User.NickName = src.NoteCard.User.NickName
	}
	if dst.NoteCard.User.Avatar == "" {
		dst.NoteCard.User.Avatar = src.NoteCard.User.Avatar
	}
	if dst.NoteCard.User.XsecToken == "" {
		dst.NoteCard.User.XsecToken = src.NoteCard.User.XsecToken
	}
	if dst.NoteCard.InteractInfo.LikedCount == "" {
		dst.NoteCard.InteractInfo.LikedCount = src.NoteCard.InteractInfo.LikedCount
	}
	if dst.NoteCard.InteractInfo.SharedCount == "" {
		dst.NoteCard.InteractInfo.SharedCount = src.NoteCard.InteractInfo.SharedCount
	}
	if dst.NoteCard.InteractInfo.CommentCount == "" {
		dst.NoteCard.InteractInfo.CommentCount = src.NoteCard.InteractInfo.CommentCount
	}
	if dst.NoteCard.InteractInfo.CollectedCount == "" {
		dst.NoteCard.InteractInfo.CollectedCount = src.NoteCard.InteractInfo.CollectedCount
	}
	dst.NoteCard.InteractInfo.Liked = dst.NoteCard.InteractInfo.Liked || src.NoteCard.InteractInfo.Liked
	dst.NoteCard.InteractInfo.Collected = dst.NoteCard.InteractInfo.Collected || src.NoteCard.InteractInfo.Collected
	if dst.NoteCard.Cover.Width == 0 {
		dst.NoteCard.Cover.Width = src.NoteCard.Cover.Width
	}
	if dst.NoteCard.Cover.Height == 0 {
		dst.NoteCard.Cover.Height = src.NoteCard.Cover.Height
	}
	if dst.NoteCard.Cover.URL == "" {
		dst.NoteCard.Cover.URL = src.NoteCard.Cover.URL
	}
	if dst.NoteCard.Cover.FileID == "" {
		dst.NoteCard.Cover.FileID = src.NoteCard.Cover.FileID
	}
	if dst.NoteCard.Cover.URLPre == "" {
		dst.NoteCard.Cover.URLPre = src.NoteCard.Cover.URLPre
	}
	if dst.NoteCard.Cover.URLDefault == "" {
		dst.NoteCard.Cover.URLDefault = src.NoteCard.Cover.URLDefault
	}
	if len(dst.NoteCard.Cover.InfoList) == 0 {
		dst.NoteCard.Cover.InfoList = src.NoteCard.Cover.InfoList
	}
	if dst.NoteCard.Video == nil {
		dst.NoteCard.Video = src.NoteCard.Video
	}
}

type searchPageDecision struct {
	NavigateExplore bool
	SearchSelector  string
}

func decideSearchPage(pageURL string) searchPageDecision {
	if isSearchResultPage(pageURL) {
		return searchPageDecision{
			NavigateExplore: false,
			SearchSelector:  SelectorSearchInput,
		}
	}
	if isExplorePage(pageURL) {
		return searchPageDecision{
			NavigateExplore: false,
			SearchSelector:  SelectorSearchInput,
		}
	}
	return searchPageDecision{
		NavigateExplore: true,
		SearchSelector:  SelectorSearchInput,
	}
}

func isExplorePage(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" && parsed.Host == "www.xiaohongshu.com" && parsed.Path == "/explore"
}

func isSearchResultPage(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" && parsed.Host == "www.xiaohongshu.com" && (parsed.Path == "/search_result" || parsed.Path == "/search_result_ai") && parsed.Fragment == ""
}

// prepareSearchPage 供 searchByUI 和测试同时使用
func prepareSearchPage(infoFn func() string, navigateFn func(string) error) (string, error) {
	pageURL := infoFn()
	decision := decideSearchPage(pageURL)
	if decision.NavigateExplore {
		if err := navigateFn("https://www.xiaohongshu.com/explore"); err != nil {
			return "", fmt.Errorf("导航探索页失败: %w", err)
		}
	}
	return decision.SearchSelector, nil
}

func makeSearchURL(keyword string) string {

	values := url.Values{}
	values.Set("keyword", keyword)
	values.Set("source", "web_explore_feed")

	// From https://www.xiaohongshu.com/explore, the current search button routes to
	// /search_result_ai while keeping source=web_explore_feed.
	return fmt.Sprintf("https://www.xiaohongshu.com/search_result_ai?%s", values.Encode())
}

// isCurrentSearchPage 检查页面 URL 是否已在指定关键词搜索结果上
func isCurrentSearchPage(page *hrod.Page, keyword string) bool {
	info, err := page.Rod.Info()
	if err != nil || info == nil {
		return false
	}
	u, err := url.Parse(info.URL)
	if err != nil {
		return false
	}
	q := u.Query()
	return q.Get("keyword") == keyword
}
