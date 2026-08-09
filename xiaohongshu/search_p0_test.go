package xiaohongshu

import (
	"fmt"
	"strings"
	"testing"
)

func TestSearchFallbackDoesNotSwallowFatal(t *testing.T) {
	navigations := 0
	err := waitForSearchResultsWithURLFallback("keyword", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			return fmt.Errorf("probe: %w", ErrFatalRendererError)
		},
		pageErr: func() error { return nil },
		navigate: func(string) error {
			navigations++
			return nil
		},
	})
	if !IsFatalRendererError(err) || navigations != 0 {
		t.Fatalf("fatal 不得进入 URL fallback: navigations=%d err=%v", navigations, err)
	}
}

func TestSearchFallbackSkipsNavigateWhenAlreadyOnSearchPage(t *testing.T) {
	waits := 0
	err := waitForSearchResultsWithURLFallback("三亚旅游", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			waits++
			return fmt.Errorf("probe 超时")
		},
		pageErr: func() error { return nil },
		navigate: func(string) error {
			return errAlreadyOnSearchPage
		},
	})
	if err == nil {
		t.Fatal("已在搜索页不重复导航时应返回原等待错误")
	}
	if !strings.Contains(err.Error(), "已在搜索页不重复导航") {
		t.Fatalf("错误应说明已在搜索页: %v", err)
	}
	if waits != 1 {
		t.Fatalf("已在搜索页时应只等待一次: %d", waits)
	}
}

func TestSearchFallbackNavigatesWhenNotOnSearchPage(t *testing.T) {
	navigations := 0
	waits := 0
	err := waitForSearchResultsWithURLFallback("三亚旅游", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			waits++
			if waits == 1 {
				return fmt.Errorf("probe 超时")
			}
			return nil
		},
		pageErr: func() error { return nil },
		navigate: func(string) error {
			navigations++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("兜底导航后等待成功不应报错: %v", err)
	}
	if navigations != 1 || waits != 2 {
		t.Fatalf("未在搜索页时应导航一次并等待两次: navigations=%d waits=%d", navigations, waits)
	}
}

func TestIsSearchResultPage(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{name: "search_result", url: "https://www.xiaohongshu.com/search_result?keyword=abc", want: true},
		{name: "search_result_ai", url: "https://www.xiaohongshu.com/search_result_ai?keyword=abc", want: true},
		{name: "无 fragment", url: "https://www.xiaohongshu.com/search_result#anchor", want: false},
		{name: "explore", url: "https://www.xiaohongshu.com/explore", want: false},
		{name: "伪域名", url: "https://evil.com/search_result?keyword=abc", want: false},
		{name: "相似路径", url: "https://www.xiaohongshu.com/search_results_extra", want: false},
		{name: "非 https", url: "http://www.xiaohongshu.com/search_result?keyword=abc", want: false},
		{name: "非法 URL", url: "not-a-url", want: false},
	}
	for _, tc := range cases {
		if got := isSearchResultPage(tc.url); got != tc.want {
			t.Errorf("%s: isSearchResultPage(%q) = %v, want %v", tc.name, tc.url, got, tc.want)
		}
	}
}
