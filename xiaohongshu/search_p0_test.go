package xiaohongshu

import (
	"fmt"
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
