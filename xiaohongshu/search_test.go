package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
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
	require.Contains(t, err.Error(), navErr.Error())
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
	require.Contains(t, err.Error(), fallbackErr.Error())
	require.Equal(t, 2, waitCount)
}

func TestSearchResultWatchdogUsesFeedCards(t *testing.T) {
	require.Equal(t, SelectorFeedCard, SearchResultSpec.Selector)
	require.True(t, SearchResultSpec.VisibleOnly)
}
