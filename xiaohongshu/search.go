package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	rodinput "github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type SearchResult struct {
	Search struct {
		Feeds FeedsValue `json:"feeds"`
	} `json:"search"`
}

const (
	searchInputWaitTimeout         = 45 * time.Second
	searchResultsWaitTimeout       = 30 * time.Second
	searchFilterRefreshWaitTimeout = 20 * time.Second
)

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// internalFilterOption 内部使用的筛选选项(基于索引)
type internalFilterOption struct {
	FiltersIndex int    // 筛选组索引
	TagsIndex    int    // 标签索引
	Text         string // 标签文本描述
}

// 预定义的筛选选项映射表（内部使用）
var filterOptionsMap = map[int][]internalFilterOption{
	1: { // 排序依据
		{FiltersIndex: 1, TagsIndex: 1, Text: "综合"},
		{FiltersIndex: 1, TagsIndex: 2, Text: "最新"},
		{FiltersIndex: 1, TagsIndex: 3, Text: "最多点赞"},
		{FiltersIndex: 1, TagsIndex: 4, Text: "最多评论"},
		{FiltersIndex: 1, TagsIndex: 5, Text: "最多收藏"},
	},
	2: { // 笔记类型
		{FiltersIndex: 2, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 2, TagsIndex: 2, Text: "视频"},
		{FiltersIndex: 2, TagsIndex: 3, Text: "图文"},
	},
	3: { // 发布时间
		{FiltersIndex: 3, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 3, TagsIndex: 2, Text: "一天内"},
		{FiltersIndex: 3, TagsIndex: 3, Text: "一周内"},
		{FiltersIndex: 3, TagsIndex: 4, Text: "半年内"},
	},
	4: { // 搜索范围
		{FiltersIndex: 4, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 4, TagsIndex: 2, Text: "已看过"},
		{FiltersIndex: 4, TagsIndex: 3, Text: "未看过"},
		{FiltersIndex: 4, TagsIndex: 4, Text: "已关注"},
	},
	5: { // 位置距离
		{FiltersIndex: 5, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 5, TagsIndex: 2, Text: "同城"},
		{FiltersIndex: 5, TagsIndex: 3, Text: "附近"},
	},
}

// convertToInternalFilters 将 FilterOption 转换为内部的 internalFilterOption 列表
func convertToInternalFilters(filter FilterOption) ([]internalFilterOption, error) {
	var internalFilters []internalFilterOption

	// 处理排序依据
	if filter.SortBy != "" {
		internal, err := findInternalOption(1, filter.SortBy)
		if err != nil {
			return nil, fmt.Errorf("排序依据错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理笔记类型
	if filter.NoteType != "" {
		internal, err := findInternalOption(2, filter.NoteType)
		if err != nil {
			return nil, fmt.Errorf("笔记类型错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理发布时间
	if filter.PublishTime != "" {
		internal, err := findInternalOption(3, filter.PublishTime)
		if err != nil {
			return nil, fmt.Errorf("发布时间错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理搜索范围
	if filter.SearchScope != "" {
		internal, err := findInternalOption(4, filter.SearchScope)
		if err != nil {
			return nil, fmt.Errorf("搜索范围错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理位置距离
	if filter.Location != "" {
		internal, err := findInternalOption(5, filter.Location)
		if err != nil {
			return nil, fmt.Errorf("位置距离错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	return internalFilters, nil
}

// findInternalOption 根据筛选组索引和文本查找内部筛选选项
func findInternalOption(filtersIndex int, text string) (internalFilterOption, error) {
	options, exists := filterOptionsMap[filtersIndex]
	if !exists {
		return internalFilterOption{}, fmt.Errorf("筛选组 %d 不存在", filtersIndex)
	}

	for _, option := range options {
		if option.Text == text {
			return option, nil
		}
	}

	return internalFilterOption{}, fmt.Errorf("在筛选组 %d 中未找到文本 '%s'", filtersIndex, text)
}

// validateInternalFilterOption 验证内部筛选选项是否在有效范围内
func validateInternalFilterOption(filter internalFilterOption) error {
	// 检查筛选组索引是否有效
	if filter.FiltersIndex < 1 || filter.FiltersIndex > 5 {
		return fmt.Errorf("无效的筛选组索引 %d，有效范围为 1-5", filter.FiltersIndex)
	}

	// 检查标签索引是否在对应筛选组的有效范围内
	options, exists := filterOptionsMap[filter.FiltersIndex]
	if !exists {
		return fmt.Errorf("筛选组 %d 不存在", filter.FiltersIndex)
	}

	if filter.TagsIndex < 1 || filter.TagsIndex > len(options) {
		return fmt.Errorf("筛选组 %d 的标签索引 %d 超出范围，有效范围为 1-%d",
			filter.FiltersIndex, filter.TagsIndex, len(options))
	}

	return nil
}

type SearchAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func NewSearchAction(page *hrod.Page) *SearchAction {
	return &SearchAction{page: page}
}

func NewSearchActionWithState(page *hrod.Page, state *ActionStateStore) *SearchAction {
	return &SearchAction{page: page, state: state}
}

func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	page := s.page.Context(ctx)
	if err := s.searchByUI(page, keyword); err != nil {
		return nil, err
	}
	return s.collectResults(page, filters...)
}

func (s *SearchAction) SearchByURLFallback(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	page := s.page.Context(ctx)
	searchURL := makeSearchURL(keyword)
	if err := page.Navigate(searchURL); err != nil {
		return nil, fmt.Errorf("导航搜索页失败: %w", err)
	}
	if err := waitForSearchResults(page, keyword, searchResultsBaseline{}); err != nil {
		return nil, fmt.Errorf("URL兜底等待搜索结果失败: %w", err)
	}

	return s.collectResults(page, filters...)
}

func (s *SearchAction) searchByUI(page *hrod.Page, keyword string) error {
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
	input, err := waitForSearchInput(page, searchInputWaitTimeout, searchSelector)
	if err != nil {
		return fmt.Errorf("未找到搜索框: %w", err)
	}
	if searchSelector == SelectorSearchInputInSearchResult {
		// Search AI：优先使用 probeSearchInput 已标记的可见输入（data-xhs-mcp-search-input="1"），
		// 避免 history.back() 后 bfcache 存在多个同名 textarea 导致裸查命中错误元素。
		if _, err := page.Eval(`(kw) => {
			const input = document.querySelector('[data-xhs-mcp-search-input="1"]') ||
			              document.querySelector('textarea[name="aiSearchTextarea"]');
			if (!input) throw new Error('search input not found');
			input.focus();
			input.select();
			document.execCommand('delete', false);
			const s = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set;
			s.call(input, kw);
			input.dispatchEvent(new Event('input', {bubbles: true}));
			}`, keyword); err != nil {
			return fmt.Errorf("搜索关键词失败: %w", err)
			}
			if err := page.Actor().Keyboard.Press(rodinput.Enter); err != nil {
			return fmt.Errorf("提交搜索失败: %w", err)
			}
	} else {
		// Explore：对测试确认的输入框执行真实点击、输入和回车。
		if err := input.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return fmt.Errorf("点击搜索框失败: %w", err)
		}
		if err := input.Input(keyword); err != nil {
			return fmt.Errorf("输入关键词失败: %w", err)
		}
		if err := page.Actor().Keyboard.Press(rodinput.Enter); err != nil {
			return fmt.Errorf("提交搜索失败: %w", err)
		}
	}

	if err := waitForSearchResults(page, keyword, searchResultsBaseline{}); err != nil {
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

func captureSearchResultsBaseline(page *hrod.Page) (searchResultsBaseline, error) {
	probe, err := probeSearchResultsKeyword(page, "")
	if err != nil {
		return searchResultsBaseline{}, err
	}
	return searchResultsBaseline{
		StateSignature: probe.StateSignature,
		DOMSignature:   probe.DOMSignature,
	}, nil
}

func waitForSearchResults(page *hrod.Page, keyword string, baseline searchResultsBaseline) error {
	deadline := time.Now().Add(searchResultsWaitTimeout)
	var last searchResultsKeywordProbe
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return err
		}

		probe, err := probeSearchResultsKeyword(page, keyword)
		if err != nil {
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

func probeSearchResultsKeyword(page *hrod.Page, keyword string) (searchResultsKeywordProbe, error) {
	obj, err := page.Eval(`(keyword, feedCardSelector, searchInputSelector, markedSearchInputSelector) => {
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
	}`, keyword, SelectorFeedCard, SelectorSearchInput, SelectorMarkedSearchInput)
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
	inputReady := probe.OnSearchPage &&
		probe.InputMatched &&
		probe.HasVisibleCards &&
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

func waitForSearchInput(page *hrod.Page, timeout time.Duration, searchSelector string) (*hrod.Element, error) {
	deadline := time.Now().Add(timeout)
	var last searchInputProbe
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return nil, err
		}

		probe, err := probeSearchInput(page, searchSelector)
		if err != nil {
			lastErr = err
		} else {
			last = probe
			if probe.HasSearchInput && probe.SearchInputVisible {
				// Explore 首页的搜索框有唯一且已验收的 ID。直接按该 selector
				// 取回并点击，避免 probe 的 marker 再次定位到重叠 textarea。
				selector := SelectorMarkedSearchInput
				if searchSelector == SelectorSearchInputInFeeds {
					selector = SelectorSearchInputInFeeds
				}
				input, err := page.Element(selector)
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

func probeSearchInput(page *hrod.Page, searchSelector string) (searchInputProbe, error) {
	obj, err := page.Eval(`(searchSelector) => {
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
		document.querySelectorAll('[data-xhs-mcp-search-input="1"]').forEach((el) => {
			el.removeAttribute("data-xhs-mcp-search-input");
		});
		const candidates = Array.from(document.querySelectorAll(searchSelector));
		const clickHit = (el) => {
			const rect = el.getBoundingClientRect();
			const x = Math.min(Math.max(rect.left + rect.width / 2, 1), window.innerWidth - 1);
			const y = Math.min(Math.max(rect.top + rect.height / 2, 1), window.innerHeight - 1);
			const hit = document.elementFromPoint(x, y);
			return !!hit && (hit === el || el.contains(hit));
		};
		const searchInput = candidates.find((el) => visible(el) && clickHit(el));
		if (searchInput) {
			searchInput.setAttribute("data-xhs-mcp-search-input", "1");
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
	}`, searchSelector)
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

func (s *SearchAction) collectResults(page *hrod.Page, filters ...FilterOption) ([]Feed, error) {
	appliedFilters := false

	// 如果有筛选条件，则应用筛选
	if len(filters) > 0 {
		// 将所有 FilterOption 转换为内部筛选选项
		var allInternalFilters []internalFilterOption
		for _, filter := range filters {
			internalFilters, err := convertToInternalFilters(filter)
			if err != nil {
				return nil, fmt.Errorf("筛选选项转换失败: %w", err)
			}
			allInternalFilters = append(allInternalFilters, internalFilters...)
		}

		// 验证所有内部筛选选项
		for _, filter := range allInternalFilters {
			if err := validateInternalFilterOption(filter); err != nil {
				return nil, fmt.Errorf("筛选选项验证失败: %w", err)
			}
		}

		if len(allInternalFilters) > 0 {
			// 点击筛选按钮打开面板
			filterButton, err := page.Element(".filter.ai-chat-filter")
			if err != nil {
				return nil, fmt.Errorf("未找到筛选按钮: %w", err)
			}
			if err := filterButton.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return nil, fmt.Errorf("打开筛选面板失败: %w", err)
			}
			if err := page.Wait(rod.Eval(`() => document.querySelector('.filter-panel') !== null`)); err != nil {
				return nil, fmt.Errorf("等待筛选面板失败: %w", err)
			}

			for _, filter := range allInternalFilters {
				tags, err := page.Elements(".filter-panel .tags")
				if err != nil {
					return nil, fmt.Errorf("读取筛选标签失败: %w", err)
				}
				var tag *hrod.Element
				for _, candidate := range tags {
					text, textErr := candidate.Text()
					if textErr == nil && strings.TrimSpace(text) == filter.Text {
						tag = candidate
						break
					}
				}
				if tag == nil {
					return nil, fmt.Errorf("未找到筛选标签 %q", filter.Text)
				}
				if err := tag.Click(proto.InputMouseButtonLeft, 1); err != nil {
					return nil, fmt.Errorf("筛选标签 %q 点击失败: %w", filter.Text, err)
				}
			}

			// 记录关闭筛选面板前的 feeds 数据长度
			previousFeedsJSONLengthResult, err := page.Eval(`() => {
			const feeds = window.__INITIAL_STATE__?.search?.feeds;
			const data = feeds?.value !== undefined ? feeds.value : (feeds?._value !== undefined ? feeds._value : feeds?._rawValue);
			return Array.isArray(data) ? JSON.stringify(data).length : 0;
		}`)
			if err != nil {
				return nil, fmt.Errorf("读取筛选前 state 长度失败: %w", err)
			}
			if previousFeedsJSONLengthResult == nil {
				return nil, fmt.Errorf("读取筛选前 state 长度失败: 无返回")
			}
			previousFeedsJSONLength := previousFeedsJSONLengthResult.Value.Int()

			// 点击页面空白关闭面板。
			if _, err := page.Eval(`() => document.body.click()`); err != nil {
				return nil, fmt.Errorf("关闭筛选面板失败: %w", err)
			}
			if err := page.Sleep(2 * time.Second); err != nil {
				return nil, err
			}

			// 等应用状态刷新
			if err := page.Wait(rod.Eval(`(previousFeedsJSONLength, deadline) => {
			const feeds = window.__INITIAL_STATE__?.search?.feeds;
			const data = feeds?.value !== undefined ? feeds.value : (feeds?._value !== undefined ? feeds._value : feeds?._rawValue);
			return (Array.isArray(data) && JSON.stringify(data).length !== previousFeedsJSONLength) ||
				Date.now() >= deadline;
		}`, previousFeedsJSONLength, time.Now().Add(5*time.Second).UnixMilli())); err != nil {
				return nil, fmt.Errorf("等待筛选结果刷新失败: %w", err)
			}
			appliedFilters = true
		}
	}

	if appliedFilters {
		// 筛选后优先读取应用状态，和 fixup 分支保持一致，避免拿到旧的 DOM 卡片。
		if feeds, err := readSearchFeedsFromState(page); err == nil && len(feeds) > 0 {
			return feeds, nil
		}
	}

	if feeds, err := ExtractSearchFeedsFromDOM(page); err == nil && len(feeds) > 0 {
		if hasEmptyXsecToken(feeds) {
			if stateFeeds, stateErr := readSearchFeedsFromState(page); stateErr == nil {
				mergeSearchFeedXsecTokens(feeds, stateFeeds)
			}
		}
		return feeds, nil
	}

	// DOM 提取失败时降级到 __INITIAL_STATE__，兼容页面结构变动或虚拟列表未渲染的情况。
	return readSearchFeedsFromState(page)
}

func readSearchFeedsFromState(page *hrod.Page) ([]Feed, error) {
	result, err := page.Eval(`() => {
		if (window.__INITIAL_STATE__ &&
		    window.__INITIAL_STATE__.search &&
		    window.__INITIAL_STATE__.search.feeds) {
			const feeds = window.__INITIAL_STATE__.search.feeds;
			const feedsData = feeds?.value !== undefined ? feeds.value : (feeds?._value !== undefined ? feeds._value : feeds?._rawValue);
			if (feedsData) {
				return JSON.stringify(feedsData);
			}
		}
		return "";
	}`)
	if err != nil {
		return nil, err
	}

	if result == nil || result.Value.Str() == "" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result.Value.Str()), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}

	return feeds, nil
}

func hasEmptyXsecToken(feeds []Feed) bool {
	for _, feed := range feeds {
		if feed.XsecToken == "" {
			return true
		}
	}
	return false
}

func mergeSearchFeedXsecTokens(feeds, stateFeeds []Feed) {
	tokenByID := make(map[string]string, len(stateFeeds))
	for _, feed := range stateFeeds {
		if feed.ID != "" && feed.XsecToken != "" {
			tokenByID[feed.ID] = feed.XsecToken
		}
	}

	for i := range feeds {
		if feeds[i].XsecToken != "" {
			continue
		}
		if token := tokenByID[feeds[i].ID]; token != "" {
			feeds[i].XsecToken = token
		}
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
			SearchSelector:  SelectorSearchInputInSearchResult,
		}
	}
	if isExplorePage(pageURL) {
		return searchPageDecision{
			NavigateExplore: false,
			SearchSelector:  SelectorSearchInputInFeeds,
		}
	}
	return searchPageDecision{
		NavigateExplore: true,
		SearchSelector:  SelectorSearchInputInFeeds,
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
