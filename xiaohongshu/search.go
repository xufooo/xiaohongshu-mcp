package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	rodinput "github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type SearchResult struct {
	Search struct {
		Feeds FeedsValue `json:"feeds"`
	} `json:"search"`
}

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
	page.MustNavigate(searchURL).MustWaitLoad()
	return s.collectResults(page, filters...)
}

func (s *SearchAction) searchByUI(page *hrod.Page, keyword string) error {
	page.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	input, err := page.Timeout(15 * time.Second).Element(SelectorSearchInput)
	if err != nil {
		return fmt.Errorf("未找到搜索框: %w", err)
	}
	if err := input.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击搜索框失败: %w", err)
	}
	page.MustEval(`(selector) => {
		const el = document.querySelector(selector);
		if (!el) return;
		el.focus();
		el.value = "";
		el.dispatchEvent(new Event("input", { bubbles: true }));
	}`, SelectorSearchInput)
	if err := page.SleepRandom(300*time.Millisecond, 1200*time.Millisecond); err != nil {
		return err
	}
	if err := input.Input(keyword); err != nil {
		return fmt.Errorf("输入关键词失败: %w", err)
	}
	if err := page.SleepRandom(500*time.Millisecond, 2*time.Second); err != nil {
		return err
	}
	if err := page.Actor().Keyboard.Press(rodinput.Enter); err != nil {
		return fmt.Errorf("提交搜索失败: %w", err)
	}

	deadline := time.Now().Add(12 * time.Second).UnixMilli()
	page.MustWait(`(deadline) => {
		const feeds = window.__INITIAL_STATE__?.search?.feeds;
		const data = feeds?.value !== undefined ? feeds.value : feeds?._value;
		const hasStateFeeds = Array.isArray(data) && data.length > 0;
		const hasVisibleCards = document.querySelectorAll("section.note-item, .note-item, .feeds-container section, .note-list section").length > 0;
		return hasStateFeeds || hasVisibleCards || Date.now() >= deadline;
	}`, deadline)
	return nil
}

func (s *SearchAction) collectResults(page *hrod.Page, filters ...FilterOption) ([]Feed, error) {
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

		// 悬停在筛选按钮上
		filterButton := page.MustElement(`div.filter`)
		filterButton.MustHover()

		// 等待筛选面板出现
		page.MustWait(`() => document.querySelector('div.filter-panel') !== null`)

		// 使用 JavaScript 注入方式筛选（比 Go-rod 跨进程 DOM 遍历更稳定）
		for _, filter := range allInternalFilters {
			result := page.MustEval(`(filtersIndex, text) => {
				const panel = document.querySelector('div.filter-panel');
				if (!panel) {
					return '筛选面板不存在';
				}
				const groups = Array.from(panel.querySelectorAll('div.filters'));
				const group = groups[filtersIndex - 1];
				if (!group) {
					return '筛选组不存在';
				}
				const tags = Array.from(group.querySelectorAll('div.tags'));
				const option = tags.find((tag) => {
					if (tag.getAttribute('aria-hidden') === 'true') {
						return false;
					}
					return tag.innerText.trim() === text;
				});
				if (!option) {
					return '筛选标签不存在';
				}
				option.click();
				return '';
			}`, filter.FiltersIndex, filter.Text).String()
			if result != "" {
				return nil, fmt.Errorf("应用筛选失败: %s: %s", result, filter.Text)
			}
		}

		// 记录关闭筛选面板前的 DOM 卡片和 state 长度，避免直接返回旧的搜索结果。
		previousDOMCardCount := page.MustEval(`(selector) => document.querySelectorAll(selector).length`, SelectorFeedCard).Int()
		previousFeedsJSONLength := page.MustEval(`() => {
			const feeds = window.__INITIAL_STATE__?.search?.feeds;
			const data = feeds?.value !== undefined ? feeds.value : feeds?._value;
			return Array.isArray(data) ? JSON.stringify(data).length : 0;
		}`).Int()

		// 关闭筛选面板触发新的搜索请求
		page.MustEval(`() => {
			document.querySelector('div.filter')?.dispatchEvent(new MouseEvent('mouseleave', {bubbles: true}));
		}`)

		// Wait for the application state instead of page stability. The search
		// page keeps background requests/DOM updates active, so WaitStable may
		// never resolve even though results are ready.
		// 超过 5 秒则返回当前可用结果。
		page.MustWait(`(selector, previousDOMCardCount, previousFeedsJSONLength, deadline) => {
			const currentDOMCardCount = document.querySelectorAll(selector).length;
			const feeds = window.__INITIAL_STATE__?.search?.feeds;
			const data = feeds?.value !== undefined ? feeds.value : feeds?._value;
			return currentDOMCardCount !== previousDOMCardCount ||
				(Array.isArray(data) && JSON.stringify(data).length !== previousFeedsJSONLength) ||
				Date.now() >= deadline;
		}`, SelectorFeedCard, previousDOMCardCount, previousFeedsJSONLength, time.Now().Add(5*time.Second).UnixMilli())
	}

	if feeds, err := ExtractSearchFeedsFromDOM(page); err == nil && len(feeds) > 0 {
		return feeds, nil
	}

	// DOM 提取失败时降级到 __INITIAL_STATE__，兼容页面结构变动或虚拟列表未渲染的情况。
	result := page.MustEval(`() => {
		if (window.__INITIAL_STATE__ &&
		    window.__INITIAL_STATE__.search &&
		    window.__INITIAL_STATE__.search.feeds) {
			const feeds = window.__INITIAL_STATE__.search.feeds;
			const feedsData = feeds.value !== undefined ? feeds.value : feeds._value;
			if (feedsData) {
				return JSON.stringify(feedsData);
			}
		}
		return "";
	}`).String()

	if result == "" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}

	return feeds, nil
}

func makeSearchURL(keyword string) string {

	values := url.Values{}
	values.Set("keyword", keyword)
	values.Set("source", "web_explore_feed")

	// From https://www.xiaohongshu.com/explore, the current search button routes to
	// /search_result_ai while keeping source=web_explore_feed.
	return fmt.Sprintf("https://www.xiaohongshu.com/search_result_ai?%s", values.Encode())
}
