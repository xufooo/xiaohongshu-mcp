package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strings"
	"time"

	rodinput "github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"

	"github.com/go-rod/rod"
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
	searchTabClickTimeout          = 15 * time.Second
	searchTagClickTimeout          = 15 * time.Second
	aiResponseWaitTimeout          = 3 * time.Second
	aiResponsePollInterval         = 500 * time.Millisecond


)

// FilterOption 筛选选项结构体
type FilterOption struct {
	Tab         string `json:"tab,omitempty" jsonschema:"频道: 全部|图文|视频|用户,默认为'全部'"`
	Tag         string `json:"tag,omitempty" jsonschema:"动态标签,传「综合」清除筛选"`
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
	page  *hrod.Page
	state *ActionStateStore
}

func NewSearchAction(page *hrod.Page) *SearchAction {
	return &SearchAction{page: page}
}

func NewSearchActionWithState(page *hrod.Page, state *ActionStateStore) *SearchAction {
	return &SearchAction{page: page, state: state}
}

func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) (SearchPageResult, error) {
	var previousAIState *aiStateProbe
	pageBeforeSearch := s.page.Context(ctx)
	if !isCurrentSearchPage(pageBeforeSearch, keyword) || len(filters) > 0 {
		previousAIState = &aiStateProbe{}
		if reply, err := probeAIResponseStateWithTimeout(pageBeforeSearch, time.Second); err == nil && reply != nil {
			previousAIState.Active = true
			previousAIState.DomAIText = reply.Content
		}
	}

	page, result, err := s.searchFeeds(ctx, keyword, filters...)
	if err != nil {
		return SearchPageResult{}, err
	}

	aiChat, err := readAIResponseFromState(page, previousAIState)
	if err != nil {
		logrus.WithError(err).Warn("读取搜索页 AI 回复失败")
	} else {
		result.AIChat = aiChat
	}
	return result, nil
}

func (s *SearchAction) searchFeeds(ctx context.Context, keyword string, filters ...FilterOption) (*hrod.Page, SearchPageResult, error) {
	page := s.page.Context(ctx)

	// 导航前校验筛选值，及时报错
	var filter FilterOption
	var pfs []pendingFilter
	for _, f := range filters {
		filter = f
		collected, err := collectFilters(f)
		if err != nil {
			return nil, SearchPageResult{}, err
		}
		pfs = append(pfs, collected...)
	}

	// 检查当前页面是否已在该关键词搜索结果上
	if !isCurrentSearchPage(page, keyword) {
		if err := s.searchByUI(page, keyword); err != nil {
			return nil, SearchPageResult{}, err
		}
	} else if filter.Tab != "" || filter.Tag != "" || len(pfs) > 0 {
		// 复用同关键词页面时，有筛选条件则需要刷新以重置 Tab/Tag 状态
		searchURL := makeSearchURL(keyword)
		if err := page.Navigate(searchURL); err != nil {
			return nil, SearchPageResult{}, fmt.Errorf("刷新搜索页重置状态失败: %w", err)
		}
		if err := waitForSearchResults(page, keyword, searchResultsBaseline{}); err != nil {
			return nil, SearchPageResult{}, err
		}
	}

	result, err := s.collectResults(page, keyword, filter, pfs)
	if err != nil {
		return nil, SearchPageResult{}, err
	}
	return page, result, nil
}

// SearchFeedsOnly 保留仅返回笔记列表的兼容入口。
func (s *SearchAction) SearchFeedsOnly(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	_, result, err := s.searchFeeds(ctx, keyword, filters...)
	if err != nil {
		return nil, err
	}
	return result.Feeds, nil
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

	var filter FilterOption
	var pfs []pendingFilter
	for _, f := range filters {
		filter = f
		collected, err := collectFilters(f)
		if err != nil {
			return nil, err
		}
		pfs = append(pfs, collected...)
	}
	result, err := s.collectResults(page, keyword, filter, pfs)
	if err != nil {
		return nil, err
	}
	return result.Feeds, nil
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

	if err := input.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击搜索框失败: %w", err)
	}
	// Vue 控制的输入框需要先 JS 清空再键入，否则旧词残留
	if _, err := page.Eval(`() => {
		const el = document.activeElement;
		if (el) { el.select(); document.execCommand('delete', false); }
	}`); err != nil {
		return fmt.Errorf("清空搜索框失败: %w", err)
	}
	if err := input.Input(keyword); err != nil {
		return fmt.Errorf("输入关键词失败: %w", err)
	}
	baseline, err := captureSearchResultsBaseline(page)
	if err != nil {
		return fmt.Errorf("捕获搜索结果基线失败: %w", err)
	}

	if err := page.Actor().Keyboard.Press(rodinput.Enter); err != nil {
		return fmt.Errorf("提交搜索失败: %w", err)
	}

	if err := waitForSearchResults(page, keyword, baseline); err != nil {
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

// clickNoWait 等价上游 humanize.ClickNoWait：取元素形状中心→移过去→点击。
// 跳过 WaitInteractable/遮挡检查，用于 hover 浮层内的元素。
func clickNoWait(el *rod.Element) error {
	shape, err := el.Shape()
	if err != nil {
		return err
	}
	if len(shape.Quads) == 0 {
		return fmt.Errorf("元素无可点击区域")
	}
	q := shape.Quads[0]
	// 对角线中点 = 四边形中心
	center := proto.Point{X: (q[0] + q[4]) / 2, Y: (q[1] + q[5]) / 2}

	// 在四边形范围内加随机抖动
	var jitX, jitY float64
	if q.Len() == 4 {
		minX, maxX, minY, maxY := q[0], q[0], q[1], q[1]
		for i := 0; i < q.Len(); i++ {
			if x := q[i*2]; x < minX {
				minX = x
			} else if x > maxX {
				maxX = x
			}
			if y := q[i*2+1]; y < minY {
				minY = y
			} else if y > maxY {
				maxY = y
			}
		}
		rx, ry := maxX-minX, maxY-minY
		jitX = (rand.Float64()*2 - 1) * rx * 0.15
		jitY = (rand.Float64()*2 - 1) * ry * 0.15
	}
	target := proto.Point{X: center.X + jitX, Y: center.Y + jitY}

	mouse := el.Page().Mouse
	if err := mouse.MoveTo(target); err != nil {
		return err
	}
	if err := mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	if err := mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	return nil
}

// readFeedIDs 从 __INITIAL_STATE__ 读取当前搜索结果 feed ID 列表
func readFeedIDs(page *hrod.Page) (string, error) {
	result, err := page.Eval(feedIDsJS)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return result.Value.Str(), nil
}

// waitFeedsChanged 轮询等待搜索结果 ID 列表发生变化，超时返回阶段错误
func waitFeedsChanged(page *hrod.Page, before string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return err
		}
		after, err := readFeedIDs(page)
		if err == nil && after != "" && after != before {
			return nil
		}
		if err := page.Sleep(300 * time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("等待 feed 变化超时(%s)", timeout)
}

func waitForFilterRefresh(page *hrod.Page, baseline searchResultsBaseline, keyword string) (stateRefreshed bool, _ error) {
	deadline := time.Now().Add(searchFilterRefreshWaitTimeout)
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return false, err
		}

		probe, err := probeSearchResultsKeyword(page, keyword)
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			if probe.OnSearchPage && probe.InputMatched && probe.HasVisibleCards && searchResultsChanged(probe, baseline) {
				return probe.StateSignature != "" && probe.StateSignature != baseline.StateSignature, nil
			}
		}

		if err := page.Sleep(300 * time.Millisecond); err != nil {
			return false, err
		}
	}

	if lastErr != nil {
		return false, fmt.Errorf("筛选结果未刷新: %w", lastErr)
	}
	return false, fmt.Errorf("筛选结果未刷新: 基线未变化")
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

func (s *SearchAction) collectResults(page *hrod.Page, keyword string, filter FilterOption, pfs []pendingFilter) (SearchPageResult, error) {
	var result SearchPageResult
	var stateRefreshed bool

	// 旧 NoteType 兼容：NoteType=图文/视频 自动映射到同名 Tab
	noteTypeMappedToTab := false
	if filter.NoteType != "" && filter.Tab == "" {
		switch filter.NoteType {
		case "图文", "视频":
			filter.Tab = filter.NoteType
			noteTypeMappedToTab = true
		}
	}

	// NoteType 已映射为 Tab 时，从面板筛选中移除，避免重复过滤
	if noteTypeMappedToTab {
		filtered := pfs[:0]
		for _, pf := range pfs {
			if pf.GroupLabel != "笔记类型" {
				filtered = append(filtered, pf)
			}
		}
		pfs = filtered
	}

	tab := filter.Tab
	tag := filter.Tag

	// 1. 点 Tab → waitFeedsChanged（先读基线再点 Tab，实际点击了才等变化）
	if tab != "" {
		before, err := readFeedIDs(page)
		if err != nil {
			return result, fmt.Errorf("读取 tab=%s 点击前基线失败: %w", tab, err)
		}

		clicked, err := clickTab(page, tab)
		if err != nil {
			return result, fmt.Errorf("选择频道 tab=%s 失败: %w", tab, err)
		}

		if clicked {
			// "用户" Tab 不走 feed 等待，等用户卡片渲染
			if tab == "用户" {
				if err := page.SleepRandom(1*time.Second, 2*time.Second); err != nil {
					return result, err
				}
			} else {
				if err := waitFeedsChanged(page, before, searchTabClickTimeout); err != nil {
					return result, fmt.Errorf("tab=%s 点击后等待 feed 变化超时: %w", tab, err)
				}
			}
		}
	}

	// 2. 读取 available_tabs 和 available_tags
	result.AvailableTabs = readAvailableTabsFromDOM(page)
	availableTags, err := readAvailableTagsFromDOM(page)
	if err != nil {
		return result, fmt.Errorf("读取可用标签失败: %w", err)
	}

	// 3. 点 Tag → waitFeedsChanged
	if tag != "" {
		if tab == "用户" {
			if err := clickTag(page, tag); err != nil {
				return result, fmt.Errorf("选择标签 tag=%s 失败: %w", tag, err)
			}
			if err := page.SleepRandom(1*time.Second, 2*time.Second); err != nil {
				return result, err
			}
		} else {
			before, err := readFeedIDs(page)
			if err != nil {
				return result, fmt.Errorf("读取 tag=%s 点击前基线失败: %w", tag, err)
			}

			if err := clickTag(page, tag); err != nil {
				return result, fmt.Errorf("选择标签 tag=%s 失败: %w", tag, err)
			}

			if err := waitFeedsChanged(page, before, searchTagClickTimeout); err != nil {
				return result, fmt.Errorf("tag=%s 点击后等待 feed 变化超时: %w", tag, err)
			}
		}
		availableTags, err = readAvailableTagsFromDOM(page)
		if err != nil {
			return result, fmt.Errorf("点击标签后重读可用标签失败: %w", err)
		}
	}

	// 4. 面板筛选 → waitFeedsChanged
	if len(pfs) > 0 {
		stageErr := func(stage string, t0 time.Time, err error, tagName string) error {
			fields := logrus.Fields{
				"stage":      stage,
				"elapsed_ms": time.Since(t0).Milliseconds(),
				"error":      err.Error(),
			}
			if tagName != "" {
				fields["tag"] = tagName
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
			return result, stageErr("filter_button_lookup", t0, err, "")
		}

		t0 = time.Now()
		if err := filterButton.Rod.Hover(); err != nil {
			return result, stageErr("filter_button_hover", t0, err, "")
		}

		t0 = time.Now()
		if err := filterPage.Wait(rod.Eval(`() => document.querySelector('.filter-panel') !== null`)); err != nil {
			return result, stageErr("filter_panel_wait", t0, err, "")
		}

		before, _ := readFeedIDs(page)

		for _, pf := range pfs {
			option, err := findFilterOption(page, pf)
			if err != nil {
				return result, stageErr("filter_option_lookup", time.Now(), err, pf.OptionText)
			}

			if err := filterPage.SleepRandom(200*time.Millisecond, 500*time.Millisecond); err != nil {
				return result, stageErr("filter_option_delay", time.Now(), err, pf.OptionText)
			}

			t0 = time.Now()
			if err := clickNoWait(option.Rod); err != nil {
				return result, stageErr("filter_option_click", t0, err, pf.OptionText)
			}
		}

		if err := waitFeedsChanged(page, before, searchFilterRefreshWaitTimeout); err != nil {
			return result, fmt.Errorf("面板筛选后等待 feed 变化超时: %w", err)
		}
		stateRefreshed = true

		// 面板筛选后重读 available_tags
		availableTags, err = readAvailableTagsFromDOM(page)
		if err != nil {
			return result, fmt.Errorf("筛选后重读可用标签失败: %w", err)
		}
	}

	// 5. 最终提取结果
	result.AvailableTags = availableTags

	if tab == "用户" {
		users, err := extractSearchUsersFromDOM(page)
		if err != nil {
			return result, fmt.Errorf("提取用户数据失败: %w", err)
		}
		result.ResultType = ResultTypeUser
		result.Users = users
		result.Count = len(users)
		return result, nil
	}

	result.ResultType = ResultTypeFeed

	domFeeds, domErr := ExtractSearchFeedsFromDOM(page)
	stateFeeds, stateErr := readSearchFeedsFromState(page)

	if len(pfs) > 0 {
		if stateRefreshed && stateErr == nil && len(stateFeeds) > 0 {
			result.Feeds = mergeFeedsByID(stateFeeds, domFeeds)
			result.Count = len(result.Feeds)
			return result, nil
		}
		if domErr == nil && len(domFeeds) > 0 {
			result.Feeds = domFeeds
			result.Count = len(result.Feeds)
			return result, nil
		}
		if stateRefreshed && stateErr == nil && len(stateFeeds) > 0 {
			result.Feeds = stateFeeds
			result.Count = len(result.Feeds)
			return result, nil
		}
		if domErr != nil {
			return result, domErr
		}
		return result, stateErr
	}

	if domErr == nil && len(domFeeds) > 0 {
		result.Feeds = mergeFeedsByID(domFeeds, stateFeeds)
		result.Count = len(result.Feeds)
		return result, nil
	}
	if stateErr == nil && len(stateFeeds) > 0 {
		result.Feeds = stateFeeds
		result.Count = len(result.Feeds)
		return result, nil
	}
	if domErr != nil {
		return result, domErr
	}
	return result, stateErr
}

// clickTab 通过搜索框 DOM 定位点击 Tab，返回是否实际点击。
func clickTab(page *hrod.Page, tabText string) (bool, error) {
	input, err := page.ElementX("//input[contains(@placeholder, '搜索')]")
	if err != nil {
		return false, fmt.Errorf("未找到搜索框: %w", err)
	}
	searchArea, err := input.Rod.Parent()
	if err != nil {
		return false, fmt.Errorf("获取搜索框父节点失败: %w", err)
	}
	parent, err := searchArea.Parent()
	if err != nil {
		return false, fmt.Errorf("获取搜索区域父节点失败: %w", err)
	}
	children, err := parent.Children()
	if err != nil {
		return false, fmt.Errorf("获取子节点列表失败: %w", err)
	}
	if len(children) < 2 {
		return false, fmt.Errorf("搜索区域子节点不足（需要 >=2，实际 %d）", len(children))
	}
	tabContainer := children[1]
	tabChildren, err := tabContainer.Children()
	if err != nil {
		return false, fmt.Errorf("获取 Tab 容器子节点失败: %w", err)
	}
	for _, tab := range tabChildren {
		text, err := tab.Text()
		if err != nil {
			continue
		}
		if strings.TrimSpace(text) == tabText {
			class, err := tab.Attribute("class")
			if err != nil {
				return false, fmt.Errorf("获取 Tab class 失败: %w", err)
			}
			if class != nil && strings.Contains(*class, "active") {
				return false, nil
			}
			return true, tab.Click(proto.InputMouseButtonLeft, 1)
		}
	}
	return false, fmt.Errorf("未找到 Tab「%s」", tabText)
}

// clickTag 通过搜索框 DOM 定位点击标签按钮。
func clickTag(page *hrod.Page, tagText string) error {
	input, err := page.ElementX("//input[contains(@placeholder, '搜索')]")
	if err != nil {
		return fmt.Errorf("未找到搜索框: %w", err)
	}
	searchArea, err := input.Rod.Parent()
	if err != nil {
		return fmt.Errorf("获取搜索框父节点失败: %w", err)
	}
	parent, err := searchArea.Parent()
	if err != nil {
		return fmt.Errorf("获取搜索区域父节点失败: %w", err)
	}
	children, err := parent.Children()
	if err != nil {
		return fmt.Errorf("获取子节点列表失败: %w", err)
	}
	if len(children) < 4 {
		return fmt.Errorf("搜索区域子节点不足（需要 >=4，实际 %d）", len(children))
	}
	tagContainer := children[3]
	tagButtons, err := tagContainer.Elements("button")
	if err != nil {
		return fmt.Errorf("获取 Tag 按钮列表失败: %w", err)
	}
	for _, tag := range tagButtons {
		text, err := tag.Text()
		if err != nil {
			continue
		}
		if strings.TrimSpace(text) == tagText {
			return tag.Click(proto.InputMouseButtonLeft, 1)
		}
	}
	return fmt.Errorf("未找到标签「%s」", tagText)
}

// readAvailableTagsFromDOM 从搜索区域第 4 子节点的 button 读取标签文本。
func readAvailableTagsFromDOM(page *hrod.Page) ([]string, error) {
	result, err := page.Eval(`() => {
		const input = document.querySelector('input[placeholder*="搜索"]');
		if (!input) throw new Error("未找到搜索输入框");
		const searchArea = input.parentElement.parentElement;
		const tagContainer = searchArea.children[3];
		if (!tagContainer) throw new Error("未找到标签容器");
		return JSON.stringify(
			Array.from(tagContainer.querySelectorAll('button'))
				.map(b => (b.textContent || '').trim())
				.filter(Boolean)
		);
	}`)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("readAvailableTagsFromDOM: JS 无返回")
	}
	var tags []string
	if err := json.Unmarshal([]byte(result.Value.Str()), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// readAvailableTabsFromDOM 从搜索区域第 2 子节点读取可用 Tab 文本列表。
func readAvailableTabsFromDOM(page *hrod.Page) []string {
	result, err := page.Eval(`() => {
		const input = document.querySelector('input[placeholder*="搜索"]');
		if (!input) return JSON.stringify([]);
		const searchArea = input.parentElement.parentElement;
		const tabContainer = searchArea.children[1];
		if (!tabContainer) return JSON.stringify([]);
		return JSON.stringify(
			Array.from(tabContainer.children)
				.map(ch => (ch.textContent || '').trim())
				.filter(Boolean)
		);
	}`)
	if err != nil || result == nil {
		return nil
	}
	var tabs []string
	if err := json.Unmarshal([]byte(result.Value.Str()), &tabs); err != nil {
		return nil
	}
	return tabs
}

// extractSearchUsersFromDOM 从 DOM 提取搜索用户结果。
func extractSearchUsersFromDOM(page *hrod.Page) ([]SearchUserResult, error) {
	result, err := page.Eval(`() => {
		const clean = (v) => (v || '').replace(/\s+/g, ' ').trim();
		try {
			const state = window.__INITIAL_STATE__;
			if (state && state.search && state.search.users) {
				const users = state.search.users;
				const list = users._value || users.value || users;
				if (Array.isArray(list) && list.length > 0) {
					return JSON.stringify(list.map(u => ({
						user_id: u.user_id || u.userId || u.id || '',
						nickname: u.nickname || u.nickName || u.name || '',
						avatar: u.avatar || u.avatar_url || u.headImage || '',
						description: u.desc || u.description || u.introduction || '',
						profile_url: u.profile_url || u.profileUrl || '',
					})).filter(u => u.user_id));
				}
			}
		} catch (e) {}
		return JSON.stringify([]);
	}`)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Value.Str() == "" {
		return nil, fmt.Errorf("JS 无返回")
	}
	var users []SearchUserResult
	if err := json.Unmarshal([]byte(result.Value.Str()), &users); err != nil {
		return nil, fmt.Errorf("解析用户数据失败: %w", err)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("未从 __INITIAL_STATE__ 提取到用户数据")
	}
	return users, nil
}

func collectSearchFeeds(page *hrod.Page, stateFirst bool) ([]Feed, error) {
	domFeeds, domErr := ExtractSearchFeedsFromDOM(page)
	stateFeeds, stateErr := readSearchFeedsFromState(page)
	if stateFirst && stateErr == nil && len(stateFeeds) > 0 {
		return mergeFeedsByID(stateFeeds, domFeeds), nil
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

type aiStateProbe struct {
	OneboxInfo         json.RawMessage `json:"onebox_info"`
	DQAInstantElements json.RawMessage `json:"dqa_instant_elements"`
	Active             bool            `json:"active"`
	SearchRoundID      json.RawMessage `json:"search_round_id"`
	UserMessageID      json.RawMessage `json:"user_message_id"`
	DomAIText          string          `json:"dom_ai_text"`
}

func readAIResponseFromState(page *hrod.Page, previousState *aiStateProbe) (*AIChatReply, error) {
	// 没有前状态（首次搜索）→ 等 3s 让 AI 异步加载完再读
	if previousState == nil {
		reply, _ := probeAIResponseStateWithTimeout(page, aiResponseWaitTimeout)
		return reply, nil
	}

	// 非 AI 激活搜索 → 读一次即可（没有 AI 内容）
	if !previousState.Active {
		reply, err := probeAIResponseStateWithTimeout(page, aiResponseWaitTimeout)
		if err != nil {
			return nil, err
		}
		return reply, nil
	}

	// AI 激活状态 → 有界轮询等待回复
	deadline := time.Now().Add(aiResponseWaitTimeout)
	var lastReply *AIChatReply

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return lastReply, nil
		}
		reply, err := probeAIResponseStateWithTimeout(page, remaining)
		if err != nil {
			if lastReply != nil {
				return lastReply, nil
			}
			return nil, err
		}

		if reply != nil {
			lastReply = reply
		}
		if !reply.HasMore {
			return lastReply, nil
		}
		if !time.Now().Before(deadline) {
			return lastReply, nil
		}
		sleepFor := min(aiResponsePollInterval, time.Until(deadline))
		if err := page.Sleep(sleepFor); err != nil {
			if lastReply != nil {
				return lastReply, nil
			}
			return nil, err
		}
	}
}

func probeAIResponseStateWithTimeout(page *hrod.Page, timeout time.Duration) (*AIChatReply, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		probe, err := probeAIResponseState(page)
		if err == nil {
			reply, _ := normalizeAIResponse(probe)
			if reply != nil && reply.Content != "" {
				return reply, nil
			}
		}
		time.Sleep(aiResponsePollInterval)
	}
	return nil, fmt.Errorf("AI 回复超时 (%v)", timeout)
}

func probeAIResponseState(page *hrod.Page) (aiStateProbe, error) {
	result, err := page.Eval(`() => {
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

		const snapshot = (value) => {
			const json = JSON.stringify(unwrapRef(value), (_key, nested) => unwrapRef(nested));
			return json === undefined ? undefined : JSON.parse(json);
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

		// 尝试从 DOM 提取 AI 回答文本（从 __INITIAL_STATE__ 拿不到时兜底）
		const domAIText = (() => {
			try {
				// 方案A：直接取 AI 回复消息（实测 .ai-message-finished 为最优选择器）
				const aiMsg = document.querySelector('.ai-message-finished, .ai-message');
				if (aiMsg) {
					const text = aiMsg.textContent.trim();
					if (text.length > 50) return text.slice(0, 3000);
				}

				// 方案B：取 AI 聊天滚动区域
				const scrollBody = document.querySelector('.ai-chat-scroll-body');
				if (scrollBody) {
					const text = scrollBody.textContent.trim();
					if (text.length > 50) return text.slice(0, 3000);
				}

				// 方案C：旧策略兜底（兼容未发现的结构）
				const body = document.body;
				if (!body) return "";
				const fullText = (body.innerText || "").trim();
				if (fullText.length < 500 || fullText.includes("登录") && fullText.length < 2000) return "";

				const containers = document.querySelectorAll(
					".search-layout, .feeds-container, .note-list, [class*='feeds'], [class*='search_result'], " +
					"section[class], .ai-chat-section, .ai-chat-inner"
				);
				let bestAI = "";
				for (const c of containers) {
					const txt = (c.textContent || "").trim();
					const hasNoteItems = c.querySelectorAll(
						"section.note-item, .note-item, [class*='note-item'], article, .feed-item"
					).length;
					if (txt.length > 100 && hasNoteItems === 0 && !txt.startsWith("沪ICP")) {
						if (txt.length > bestAI.length) bestAI = txt;
					}
				}
				if (bestAI.length > 120) {
					return bestAI
						.replace(/沪ICP.*?号/g, "")
						.replace(/营业执照|增值电信|违法不良.*|个性化推荐算法.*|广告屏蔽.*|__.*?__=.*/g, "")
						.replace(/\n{3,}/g, "\n").trim().slice(0, 3000);
				}
				return "";
			} catch (e) { return ""; }
		})();

		return JSON.stringify(snapshot({
			onebox_info: pick("oneboxInfo", "oneBoxInfo"),
			dqa_instant_elements: pick("dqaInstantElements", "dqaElements"),
			active: Boolean(pick("aiWendianActive", "wendianActive", "aiActive")),
			search_round_id: pick("currentSearchRoundId", "searchRoundId"),
			user_message_id: pick("currentUserMessageId", "userMessageId"),
			dom_ai_text: domAIText,
		}));
	}`)
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
