package xiaohongshu

import "testing"

func TestInferXHSReadyKindFromSessionStateUsesDetailWhenNoteOpened(t *testing.T) {
	got := inferXHSReadyKindFromSessionState("https://www.xiaohongshu.com/search_result_ai?keyword=test", true, "feed-1")
	if got != XHSReadyDetail {
		t.Fatalf("opened note should use detail kind, got %s", got)
	}
}

func TestInferXHSReadyKindFromSessionStateFallsBackToURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   XHSReadyKind
	}{
		{
			name:   "search URL",
			rawURL: "https://www.xiaohongshu.com/search_result_ai?keyword=test",
			want:   XHSReadySearch,
		},
		{
			name:   "detail URL",
			rawURL: "https://www.xiaohongshu.com/explore/feed-1",
			want:   XHSReadyDetail,
		},
		{
			name:   "home URL",
			rawURL: "https://www.xiaohongshu.com/explore",
			want:   XHSReadyHome,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferXHSReadyKindFromSessionState(tt.rawURL, false, "feed-1")
			if got != tt.want {
				t.Fatalf("inferXHSReadyKindFromSessionState() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestInferXHSReadyKindFromSessionStateRequiresFeedIDForOpenedDetail(t *testing.T) {
	got := inferXHSReadyKindFromSessionState("https://www.xiaohongshu.com/search_result_ai?keyword=test", true, "")
	if got != XHSReadySearch {
		t.Fatalf("opened session without feed ID should fall back to URL, got %s", got)
	}
}
