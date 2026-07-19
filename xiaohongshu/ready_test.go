package xiaohongshu

import (
	"os"
	"strings"
	"testing"
)

func TestIsXHSReadyHomeSearch(t *testing.T) {
	tests := []struct {
		name             string
		probe            xhsReadyProbe
		allowURLFallback bool
		want             bool
	}{
		{
			name: "non-explore URL not ready",
			probe: xhsReadyProbe{
				URL:                   "https://www.xiaohongshu.com/search",
				HomeFeedCount:         5,
				FeedCardCount:         10,
				SearchInputInFeedsReady: true,
			},
			want: false,
		},
		{
			name: "home not ready",
			probe: xhsReadyProbe{
				URL:                   "https://www.xiaohongshu.com/explore",
				HomeFeedCount:         0,
				FeedCardCount:         0,
				SearchInputInFeedsReady: true,
			},
			want: false,
		},
		{
			name: "input not ready",
			probe: xhsReadyProbe{
				URL:                   "https://www.xiaohongshu.com/explore",
				HomeFeedCount:         5,
				FeedCardCount:         10,
				SearchInputInFeedsReady: false,
			},
			want: false,
		},
		{
			name: "all conditions met",
			probe: xhsReadyProbe{
				URL:                   "https://www.xiaohongshu.com/explore",
				HomeFeedCount:         5,
				FeedCardCount:         10,
				SearchInputInFeedsReady: true,
			},
			want: true,
		},
		{
			name: "all conditions met with feed cards only",
			probe: xhsReadyProbe{
				URL:                   "https://www.xiaohongshu.com/explore",
				HomeFeedCount:         0,
				FeedCardCount:         3,
				SearchInputInFeedsReady: true,
			},
			want: true,
		},
		{
			name: "allowURLFallback does not bypass input condition",
			probe: xhsReadyProbe{
				URL:                   "https://www.xiaohongshu.com/explore",
				HomeFeedCount:         5,
				FeedCardCount:         10,
				SearchInputInFeedsReady: false,
				AppCount:              1,
			},
			allowURLFallback: true,
			want:             false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isXHSReady(tc.probe, XHSReadyHomeSearch, "", tc.allowURLFallback)
			if got != tc.want {
				t.Fatalf("isXHSReady(kind=home_search) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHomeSearchProbeRetainsReadOnlyInteractionChecks(t *testing.T) {
	source, err := os.ReadFile("ready.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)
	for _, want := range []string{
		"document.querySelector(searchInputInFeedsSelector)",
		"!el || !el.isConnected",
		"!visible(el)",
		"el.disabled || el.readOnly",
		"r.top >= window.innerHeight || r.bottom <= 0",
		"r.left >= window.innerWidth || r.right <= 0",
		"const cx = r.left + r.width / 2",
		"const cy = r.top + r.height / 2",
		"document.elementFromPoint(cx, cy)",
		"el === hit || el.contains(hit)",
		"SelectorSearchInputInFeeds",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("HomeSearch probe missing %q", want)
		}
	}
}