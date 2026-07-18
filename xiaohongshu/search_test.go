package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
)

func TestSearch(t *testing.T) {

	t.Skip("SKIP: 测试发布")

	b, err := browser.NewBrowser(context.Background(), false)
	if err != nil {
		t.Skipf("browser unavailable: %v", err)
	}
	defer b.Close()

	page := b.NewPage()
	defer func() {
		_ = page.Close()
	}()

	action := NewSearchAction(page)

	feeds, err := action.Search(context.Background(), "Kimi")
	require.NoError(t, err)
	require.NotEmpty(t, feeds, "feeds should not be empty")

	fmt.Printf("成功获取到 %d 个 Feed\n", len(feeds))

	for _, feed := range feeds {
		fmt.Printf("Feed ID: %s\n", feed.ID)
		fmt.Printf("Feed Title: %s\n", feed.NoteCard.DisplayTitle)
	}
}

func TestSearchWithFilters(t *testing.T) {

	//t.Skip("SKIP: 测试筛选功能")

	b, err := browser.NewBrowser(context.Background(), false)
	if err != nil {
		t.Skipf("browser unavailable: %v", err)
	}
	defer b.Close()

	page := b.NewPage()
	defer func() {
		_ = page.Close()
	}()

	action := NewSearchAction(page)

	// 使用新的 FilterOption 结构
	filter := FilterOption{
		NoteType:    "图文",
		PublishTime: "一天内",
	}

	feeds, err := action.Search(context.Background(), "dn432", filter)
	require.NoError(t, err)
	require.NotEmpty(t, feeds, "feeds should not be empty")

	fmt.Printf("成功获取到 %d 个筛选后的 Feed\n", len(feeds))

	for _, feed := range feeds {
		fmt.Printf("Feed ID: %s\n", feed.ID)
		fmt.Printf("Feed Title: %s\n", feed.NoteCard.DisplayTitle)
	}
}

func TestFilterValidation(t *testing.T) {
	// 测试有效的筛选选项转换
	validFilter := FilterOption{
		NoteType:    "图文",
		PublishTime: "一天内",
	}
	internalFilters, err := convertToInternalFilters(validFilter)
	require.NoError(t, err)
	require.Len(t, internalFilters, 2)

	// 验证转换后的内部筛选选项
	for _, filter := range internalFilters {
		err := validateInternalFilterOption(filter)
		require.NoError(t, err)
	}

	// 测试无效的筛选值
	invalidFilter := FilterOption{
		NoteType: "不存在的类型",
	}
	_, err = convertToInternalFilters(invalidFilter)
	require.Error(t, err)
	require.Contains(t, err.Error(), "未找到文本")

	// 测试所有有效的筛选选项
	allFilters := FilterOption{
		SortBy:      "最新",
		NoteType:    "视频",
		PublishTime: "一周内",
		SearchScope: "已关注",
		Location:    "同城",
	}
	internalFilters, err = convertToInternalFilters(allFilters)
	require.NoError(t, err)
	require.Len(t, internalFilters, 5)
}

func TestSearchResultsReady(t *testing.T) {
	tests := []struct {
		name     string
		probe    searchResultsKeywordProbe
		baseline searchResultsBaseline
		ready    bool
	}{
		{
			name: "state keyword and feeds ready",
			probe: searchResultsKeywordProbe{
				HasStateKeyword: true,
				KeywordMatched:  true,
				HasStateFeeds:   true,
			},
			ready: true,
		},
		{
			name: "state keyword and stale state signature is not ready",
			probe: searchResultsKeywordProbe{
				HasStateKeyword: true,
				KeywordMatched:  true,
				HasStateFeeds:   true,
				StateSignature:  "old-state",
			},
			baseline: searchResultsBaseline{
				StateSignature: "old-state",
			},
			ready: false,
		},
		{
			name: "url keyword and dom ready without state keyword",
			probe: searchResultsKeywordProbe{
				URLKeywordMatched: true,
				HasVisibleCards:   true,
			},
			ready: true,
		},
		{
			name: "url keyword and stale dom signature is not ready",
			probe: searchResultsKeywordProbe{
				URLKeywordMatched: true,
				HasVisibleCards:   true,
				DOMSignature:      "old-cards",
			},
			baseline: searchResultsBaseline{
				DOMSignature: "old-cards",
			},
			ready: false,
		},
		{
			name: "stale state keyword but url and dom ready",
			probe: searchResultsKeywordProbe{
				HasStateKeyword:   true,
				KeywordMatched:    false,
				URLKeywordMatched: true,
				HasVisibleCards:   true,
			},
			ready: true,
		},
		{
			name: "visible cards without current url keyword are not enough",
			probe: searchResultsKeywordProbe{
				HasStateKeyword:   true,
				KeywordMatched:    false,
				URLKeywordMatched: false,
				HasVisibleCards:   true,
			},
			ready: false,
		},
		{
			name: "explore feed cards without keyword evidence are not ready",
			probe: searchResultsKeywordProbe{
				HasVisibleCards: true,
			},
			ready: false,
		},
		{
			name: "state feeds without any keyword match are not enough",
			probe: searchResultsKeywordProbe{
				HasStateFeeds: true,
			},
			ready: false,
		},
		{
			name: "matched url without cards is not ready",
			probe: searchResultsKeywordProbe{
				URLKeywordMatched: true,
				HasVisibleCards:   false,
			},
			ready: false,
		},
		{
			name: "matched input on search page with refreshed cards is ready",
			probe: searchResultsKeywordProbe{
				InputMatched:    true,
				OnSearchPage:    true,
				HasVisibleCards: true,
				DOMSignature:    "new-cards",
			},
			baseline: searchResultsBaseline{
				DOMSignature: "old-cards",
			},
			ready: true,
		},
		{
			name: "matched input on search page with stale cards is not ready",
			probe: searchResultsKeywordProbe{
				InputMatched:    true,
				OnSearchPage:    true,
				HasVisibleCards: true,
				DOMSignature:    "old-cards",
				StateSignature:  "old-state",
			},
			baseline: searchResultsBaseline{
				DOMSignature:   "old-cards",
				StateSignature: "old-state",
			},
			ready: false,
		},
		{
			name: "matched input off search page is not ready",
			probe: searchResultsKeywordProbe{
				InputMatched:    true,
				HasVisibleCards: true,
			},
			ready: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.ready, searchResultsReady(tt.probe, tt.baseline))
		})
	}
}

func TestWaitForSearchResultsWithURLFallbackSucceeds(t *testing.T) {
	initialErr := errors.New("ui results not ready")
	baseline := searchResultsBaseline{
		StateSignature: "old-state",
		DOMSignature:   "old-dom",
	}
	var waits []searchResultsBaseline
	var navURL string

	err := waitForSearchResultsWithURLFallback("Kimi", baseline, searchResultsFallbackHooks{
		wait: func(got searchResultsBaseline) error {
			waits = append(waits, got)
			if len(waits) == 1 {
				return initialErr
			}
			return nil
		},
		pageErr: func() error { return nil },
		navigate: func(url string) error {
			navURL = url
			return nil
		},
	})

	require.NoError(t, err)
	require.Equal(t, []searchResultsBaseline{baseline, {}}, waits)
	require.Equal(t, makeSearchURL("Kimi"), navURL)
}

func TestWaitForSearchResultsWithURLFallbackSkipsFallbackWhenInitialWaitSucceeds(t *testing.T) {
	var navigateCalled bool

	err := waitForSearchResultsWithURLFallback("Kimi", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error { return nil },
		pageErr: func() error {
			t.Fatal("pageErr should not be called when initial wait succeeds")
			return nil
		},
		navigate: func(string) error {
			navigateCalled = true
			return nil
		},
	})

	require.NoError(t, err)
	require.False(t, navigateCalled)
}

func TestWaitForSearchResultsWithURLFallbackSkipsFallbackWhenContextDone(t *testing.T) {
	initialErr := errors.New("ui results not ready")
	var waitCount int
	var navigateCalled bool

	err := waitForSearchResultsWithURLFallback("Kimi", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			waitCount++
			return initialErr
		},
		pageErr: func() error { return context.Canceled },
		navigate: func(string) error {
			navigateCalled = true
			return nil
		},
	})

	require.ErrorIs(t, err, initialErr)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, waitCount)
	require.False(t, navigateCalled)
}

func TestWaitForSearchResultsWithURLFallbackReportsNavigationFailure(t *testing.T) {
	initialErr := errors.New("ui results not ready")
	navErr := errors.New("navigation failed")

	err := waitForSearchResultsWithURLFallback("Kimi", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait:     func(searchResultsBaseline) error { return initialErr },
		pageErr:  func() error { return nil },
		navigate: func(string) error { return navErr },
	})

	require.ErrorIs(t, err, initialErr)
	require.ErrorIs(t, err, navErr)
}

func TestWaitForSearchResultsWithURLFallbackReportsFallbackWaitFailure(t *testing.T) {
	initialErr := errors.New("ui results not ready")
	fallbackErr := errors.New("fallback results not ready")
	var waitCount int

	err := waitForSearchResultsWithURLFallback("Kimi", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			waitCount++
			if waitCount == 1 {
				return initialErr
			}
			return fallbackErr
		},
		pageErr:  func() error { return nil },
		navigate: func(string) error { return nil },
	})

	require.ErrorIs(t, err, initialErr)
	require.ErrorIs(t, err, fallbackErr)
	require.Equal(t, 2, waitCount)
}

func TestSearchResultWatchdogUsesFeedCards(t *testing.T) {
	require.Equal(t, SelectorFeedCard, SearchResultSpec.Selector)
	require.True(t, SearchResultSpec.VisibleOnly)
}

func TestSearchInputSelectorPriorityStructure(t *testing.T) {
	require.Equal(t, `#search-input-in-feeds`, SelectorSearchInputInFeeds)
	require.Equal(t, `#search-input`, SelectorSearchInputInSearchResult)
	require.Equal(t,
		SelectorSearchInputInFeeds+`, `+SelectorSearchInputInSearchResult,
		SelectorSearchInput,
	)
	require.NotContains(t, SelectorSearchInput, `input.search-input`)

	uiSource, err := os.ReadFile("ui_selectors.go")
	require.NoError(t, err)
	require.NotContains(t, string(uiSource), "SelectorCompatibleSearchInput")
	require.NotContains(t, string(uiSource), "SelectorSearchInputFallback")

	searchSource, err := os.ReadFile("search.go")
	require.NoError(t, err)
	script := string(searchSource)
	start := strings.Index(script, "func probeSearchInput(")
	end := strings.Index(script, "func formatSearchInputProbe(")
	require.NotEqual(t, -1, start, "probeSearchInput source marker missing")
	require.Greater(t, end, start, "probeSearchInput source boundary missing")
	probeSource := script[start:end]

	require.Contains(t, probeSource, "searchSelector")
	require.NotContains(t, probeSource, "fallbackSelector")
	require.NotContains(t, probeSource, "compatibleSelector")
	require.Contains(t, probeSource, "candidates.find(")
	require.Contains(t, probeSource, "rect.width > 1")
	require.Contains(t, probeSource, "rect.height > 1")
	require.Contains(t, probeSource, "`, searchSelector)")

	waitStart := strings.Index(script, "func waitForSearchInput(")
	waitEnd := strings.Index(script, "func probeSearchInput(")
	require.NotEqual(t, -1, waitStart, "waitForSearchInput source marker missing")
	require.Greater(t, waitEnd, waitStart, "waitForSearchInput source boundary missing")
	waitSource := script[waitStart:waitEnd]
	require.Contains(t, waitSource, "if searchSelector == SelectorSearchInputInFeeds")
	require.Contains(t, waitSource, "selector = SelectorSearchInputInFeeds")
	require.Contains(t, waitSource, "page.Element(selector)")
}

func TestDecideSearchPage(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		want   searchPageDecision
	}{
		{
			name: "blank page / info error",
			url:  "",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "explore page",
			url:  "https://www.xiaohongshu.com/explore",
			want: searchPageDecision{
				NavigateExplore: false,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "search result page with query",
			url:  "https://www.xiaohongshu.com/search_result?keyword=test",
			want: searchPageDecision{
				NavigateExplore: false,
				SearchSelector:  SelectorSearchInputInSearchResult,
			},
		},
		{
			name: "search result page no query",
			url:  "https://www.xiaohongshu.com/search_result",
			want: searchPageDecision{
				NavigateExplore: false,
				SearchSelector:  SelectorSearchInputInSearchResult,
			},
		},
		{
			name: "search_result_ai",
			url:  "https://www.xiaohongshu.com/search_result_ai?keyword=test",
			want: searchPageDecision{
				NavigateExplore: false,
				SearchSelector:  SelectorSearchInputInSearchResult,
			},
		},
		{
			name: "path contains search but not exact path",
			url:  "https://www.xiaohongshu.com/search-something",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "unrelated page",
			url:  "https://www.xiaohongshu.com/discovery/item/123",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "non-xhs host",
			url:  "https://example.com/search_result",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "http scheme not treated as search result",
			url:  "http://www.xiaohongshu.com/search_result",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "protocol-relative url not treated as search result",
			url:  "//www.xiaohongshu.com/search_result",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "malformed url",
			url:  "://invalid",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
		{
			name: "search result page with fragment not treated as search result",
			url:  "https://www.xiaohongshu.com/search_result#foo",
			want: searchPageDecision{
				NavigateExplore: true,
				SearchSelector:  SelectorSearchInputInFeeds,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideSearchPage(tt.url)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSearchByUIUsesInfoNotEval(t *testing.T) {
	searchSource, err := os.ReadFile("search.go")
	require.NoError(t, err)
	script := string(searchSource)

	funcStart := strings.Index(script, "func (s *SearchAction) searchByUI(")
	require.NotEqual(t, -1, funcStart, "searchByUI source marker missing")

	funcBody := script[funcStart:]
	nextFunc := strings.Index(funcBody[1:], "\nfunc ")
	require.Greater(t, nextFunc, 0, "searchByUI boundary missing")
	funcBody = funcBody[:nextFunc+1]

	require.NotContains(t, funcBody, `.pathname`, "searchByUI 不得用 DOM Eval 判断搜索结果页")
	require.Contains(t, funcBody, `page.Rod.Info()`, "searchByUI 应使用非阻塞 Info() 获取页面 URL")
	require.Contains(t, funcBody, `prepareSearchPage(`, "searchByUI 应通过 prepareSearchPage 决策")

	// 搜索框输入已统一为 Rod Click + Input + Enter，不再根据页面分支
	require.NotContains(t, funcBody, `SelectorSearchInputInSearchResult {`, "已统一 Rod 输入途径，不应有搜索页/Explore 分支")
	require.Contains(t, funcBody, `.Click(`, "searchByUI 应使用 Rod Click")
	require.Contains(t, funcBody, `.Input(`, "searchByUI 应使用 Rod Input")
	require.Contains(t, funcBody, `page.Actor().Keyboard.Press(rodinput.Enter)`, "搜索必须使用真实 Enter")
	require.NotContains(t, funcBody, `new KeyboardEvent`, "不得伪造 Enter 键盘事件")
}

func TestPrepareSearchPageBehavior(t *testing.T) {
	tests := []struct {
		name         string
		pageURL      string
		wantSelector string
		wantCallLog  []string
	}{
		{
			name:         "search result page",
			pageURL:      "https://www.xiaohongshu.com/search_result?keyword=test",
			wantSelector: SelectorSearchInputInSearchResult,
			wantCallLog:  []string{"Info"},
		},
		{
			name:         "blank page / info error",
			pageURL:      "",
			wantSelector: SelectorSearchInputInFeeds,
			wantCallLog:  []string{"Info", "Navigate"},
		},
		{
			name:         "explore page",
			pageURL:      "https://www.xiaohongshu.com/explore",
			wantSelector: SelectorSearchInputInFeeds,
			wantCallLog:  []string{"Info"},
		},
		{
			name:         "non-xhs host not treated as search result",
			pageURL:      "https://example.com/search_result",
			wantSelector: SelectorSearchInputInFeeds,
			wantCallLog:  []string{"Info", "Navigate"},
		},
		{
			name:         "search_result_ai",
			pageURL:      "https://www.xiaohongshu.com/search_result_ai?keyword=test",
			wantSelector: SelectorSearchInputInSearchResult,
			wantCallLog:  []string{"Info"},
		},
		{
			name:         "search result page with fragment not treated as search result",
			pageURL:      "https://www.xiaohongshu.com/search_result#foo",
			wantSelector: SelectorSearchInputInFeeds,
			wantCallLog:  []string{"Info", "Navigate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var callLog []string
			var navURL string

			infoFn := func() string {
				callLog = append(callLog, "Info")
				return tt.pageURL
			}
			navigateFn := func(url string) error {
				callLog = append(callLog, "Navigate")
				navURL = url
				return nil
			}

			selector, err := prepareSearchPage(infoFn, navigateFn)
			require.NoError(t, err)
			require.Equal(t, tt.wantSelector, selector)
			require.Equal(t, tt.wantCallLog, callLog, "调用顺序必须为 Info → (可选 Navigate) → 等待探测")

			if len(tt.wantCallLog) > 1 && tt.wantCallLog[1] == "Navigate" {
				require.Equal(t, "https://www.xiaohongshu.com/explore", navURL)
			} else {
				require.Empty(t, navURL, "不应导航")
			}
		})
	}
}
