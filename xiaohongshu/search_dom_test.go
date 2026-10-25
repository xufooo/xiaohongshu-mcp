package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestOmittedFiltersDoNotInventDefaultSelections(t *testing.T) {
	filtered := []FilterOption{{SortBy: "最新", NoteType: "视频", Location: "同城"}}
	baseline := effectiveSearchFilter(nil)
	want := FilterOption{}
	if baseline != want || effectiveSearchFilter(filtered) == baseline {
		t.Fatalf("omission retained filters: baseline=%+v", baseline)
	}
	if filterKeyFromFilters(nil) == filterKeyFromFilters([]FilterOption{{SortBy: "综合", NoteType: "不限"}}) {
		t.Fatal("omission asserted selected defaults")
	}
	cursor := &FeedCursor{Kind: FeedPageSearch, Keyword: "same", FilterKey: filterKeyFromFilters(filtered)}
	if err := cursor.Validate(FeedPageSearch, "same", filterKeyFromFilters(nil)); err == nil {
		t.Fatal("filtered cursor accepted for omitted filters")
	}
	pfs, err := collectFilters(baseline)
	if err != nil || len(pfs) != 0 {
		t.Fatalf("omission introduced filter clicks: %v %v", pfs, err)
	}
}

func TestSearchFiltersFailClosedBeforePageAccess(t *testing.T) {
	for _, filter := range []FilterOption{{SortBy: "最新"}, {SortBy: "综合"}, {NoteType: "不限"}} {
		feeds, err := NewSearchAction(nil).SearchFeedsOnly(context.Background(), "same", filter)
		if !errors.Is(err, errSearchFiltersUnconfirmed) || feeds != nil {
			t.Fatalf("filter was certified without an apply contract: feeds=%v err=%v", feeds, err)
		}
		result, err := NewSearchAction(nil).Search(context.Background(), "same", filter)
		if !errors.Is(err, errSearchFiltersUnconfirmed) || result.Feeds != nil {
			t.Fatalf("AI search accessed page before rejecting filters: result=%v err=%v", result, err)
		}
	}
}

func TestFilterArrayReplacementDoesNotConfirmAppliedConditions(t *testing.T) {
	page := navigateFixturePage(t)
	ctx := page.Rod.GetContext()
	counter := &evalTimeoutCounter{}
	if _, err := evalJS(ctx, counter, page, `() => { window.__INITIAL_STATE__ = {search: {feeds: [{id: 'same'}]}}; }`); err != nil {
		t.Fatal(err)
	}
	if _, err := evalJS(ctx, counter, page, `() => { window.__INITIAL_STATE__.search.feeds = [{id: 'same'}]; }`); err != nil {
		t.Fatal(err)
	}
	feeds, err := NewSearchAction(page).collectResults(ctx, page, counter, []pendingFilter{{GroupLabel: "排序依据", OptionText: "最新"}})
	if !errors.Is(err, errSearchFiltersUnconfirmed) || feeds != nil {
		t.Fatalf("replacement certified unrelated results: feeds=%v err=%v", feeds, err)
	}
}

// 执行真实搜索探针，验证隐藏卡片、顺序和第六张后的短路。
func TestSearchDOMSignatureVisibleCardLimit(t *testing.T) {
	page := navigateFixturePage(t)
	for _, tc := range []struct {
		name    string
		visible []bool
	}{
		{name: "empty"},
		{name: "hidden", visible: []bool{false, false}},
		{name: "short", visible: []bool{false, true, false, true}},
		{name: "six", visible: []bool{true, true, true, true, true, true}},
		{name: "interleaved", visible: []bool{false, true, false, true, true, false, true, true, true, true, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var html strings.Builder
			var expected []string
			for i, visible := range tc.visible {
				style := "display:none"
				if visible {
					style = "width:100px;height:20px"
					if len(expected) < 6 {
						expected = append(expected, fmt.Sprintf("n%d|title%d|https://www.xiaohongshu.com/explore/n%d", i, i, i))
					}
				}
				fmt.Fprintf(&html, `<section class="note-item" data-note-id="n%d" style="%s"><a href="https://www.xiaohongshu.com/explore/n%d"><span class="title">title%d</span></a></section>`, i, style, i, i)
			}
			if err := page.Rod.SetDocumentContent(html.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := page.Rod.Eval(`() => {
				window.cardRectReads = 0;
				for (const card of document.querySelectorAll("section.note-item")) {
					const original = card.getBoundingClientRect.bind(card);
					card.getBoundingClientRect = () => { window.cardRectReads++; return original(); };
				}
			}`); err != nil {
				t.Fatal(err)
			}
			probe, err := probeSearchResultsKeyword(page.Rod.GetContext(), page, &evalTimeoutCounter{}, "")
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if len(expected) > 0 {
				encoded, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				want = string(encoded)
			}
			if probe.DOMSignature != want || probe.HasVisibleCards != (len(expected) > 0) {
				t.Fatalf("signature=%q visible=%t; want signature=%q", probe.DOMSignature, probe.HasVisibleCards, want)
			}
			reads, err := page.Rod.Eval(`() => window.cardRectReads`)
			if err != nil {
				t.Fatal(err)
			}
			if got := reads.Value.Int(); got != len(expected) {
				t.Fatalf("尺寸读取次数=%d，期望=%d", got, len(expected))
			}
		})
	}
}
