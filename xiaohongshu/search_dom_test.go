package xiaohongshu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

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
