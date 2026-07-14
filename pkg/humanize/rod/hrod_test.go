package hrod

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestInteractableSnapshotEvidenceString(t *testing.T) {
	// 非 obscured 无证据
	for _, reason := range []string{"detached", "not_visible"} {
		s := interactableSnapshot{Reason: reason}
		if got := s.evidenceString(); got != "" {
			t.Errorf("reason=%q evidenceString() = %q, want empty", reason, got)
		}
	}

	// obscured JSON 含所有要求字段且 contains:false
	s := interactableSnapshot{
		Reason:  "obscured",
		TargetTag: "INPUT", TargetID: "search", TargetClass: "search-input",
		TargetPlaceholder: "搜索...",
		TargetCX: 100, TargetCY: 200,
		Left: 50, Top: 150, Width: 300, Height: 40,
		HitTag: "DIV", HitID: "overlay", HitClass: "modal-backdrop",
		HitText:   "loading",
		HitLeft: 0, HitTop: 0, HitWidth: 800, HitHeight: 600,
		TargetContainsHit: false,
		URL:   "https://example.com/search",
		Title: "Search Page",
	}
	got := s.evidenceString()
	if got == "" {
		t.Fatal("obscured evidenceString() returned empty")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("evidence is not valid JSON: %v\nraw: %s", err, got)
	}
	required := []string{"placeholder", "targetTag", "targetId", "targetClass",
		"targetCx", "targetCy",
		"targetLeft", "targetTop", "targetWidth", "targetHeight",
		"hitTag", "hitId", "hitClass", "hitText",
		"hitLeft", "hitTop", "hitWidth", "hitHeight",
		"contains", "url", "title"}
	for _, k := range required {
		if _, ok := parsed[k]; !ok {
			t.Errorf("evidence missing required field %q", k)
		}
	}
	// contains 必须是 false，不因 omitempty 丢失
	c, _ := parsed["contains"].(bool)
	if c {
		t.Error("contains should be false for obscured snapshot")
	}
	// 字节硬上限
	if len(got) > maxEvidenceBytes {
		t.Errorf("evidence exceeds %d bytes: %d", maxEvidenceBytes, len(got))
	}

	// Go 层 URL 防御：query/fragment/userinfo 在输出中被剥离
	s = interactableSnapshot{
		Reason:  "obscured",
		TargetTag: "INPUT",
		TargetClass:       strings.Repeat("a", 200),
		TargetPlaceholder: strings.Repeat("x", 100),
		TargetCX: 100, TargetCY: 200,
		Left: 50, Top: 150, Width: 300, Height: 40,
		HitTag: "DIV",
		HitText:   "line1\nline2\nline3   " + strings.Repeat("x", 100),
		TargetContainsHit: false,
		URL:   "https://example.com/path?secret=leaked#frag",
		Title: strings.Repeat("y", 200),
	}
	got = s.evidenceString()
	if got == "" {
		t.Fatal("obscured evidenceString() returned empty for edge cases")
	}
	parsed = nil
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("evidence is not valid JSON: %v\nraw: %s", err, got)
	}
	// URL 只保留 origin+pathname，query/fragment/userinfo 全部剥离
	if u, ok := parsed["url"].(string); !ok || u != "https://example.com/path" {
		t.Errorf("url = %q, want %q", u, "https://example.com/path")
	}
	// 长字段截断
	if ph, ok := parsed["placeholder"].(string); !ok || len(ph) > 40 {
		t.Errorf("placeholder too long or missing: %q (len=%d)", ph, len(ph))
	}
	if tl, ok := parsed["title"].(string); !ok || len(tl) > 120 {
		t.Errorf("title too long or missing: %q (len=%d)", tl, len(tl))
	}
	if ht, ok := parsed["hitText"].(string); !ok || len(ht) > 60 {
		t.Errorf("hitText too long or missing: %q (len=%d)", ht, len(ht))
	}

	// 全部字符串不含控制字符（含 tab/LF/CR）
	if strings.ContainsAny(got, "\t\n\r") {
		t.Errorf("evidence JSON contains control characters tab/LF/CR")
	}
	for _, k := range required {
		v, ok := parsed[k].(string)
		if !ok {
			continue
		}
		for _, r := range v {
			if isDiscardRune(r) {
				t.Errorf("field %q contains discarded char U+%04X", k, r)
			}
		}
	}
}

func TestTruncateStripsControlChars(t *testing.T) {
	input := "a\x00b\x01c\td\ne\rf\x7Fg h\x80i\u061Cj\u200Bk\u2028l\u2066m\uFEFFn"
	want := "abcdefg hijklmn"
	got := truncate(input, 100)
	if got != want {
		t.Errorf("truncate(%q, 100) = %q, want %q", input, got, want)
	}
}
}

func TestEvidenceHardLimit(t *testing.T) {
	// 构造极端数据确保证据 JSON 不超过 2048
	s := interactableSnapshot{
		Reason:            "obscured",
		TargetTag:         "INPUT",
		TargetID:          strings.Repeat("x", 100),
		TargetClass:       strings.Repeat("y", 200),
		TargetPlaceholder: strings.Repeat("z", 100),
		TargetCX:          123456.789,
		TargetCY:          987654.321,
		Left:              111111.222,
		Top:               333333.444,
		Width:             555555.666,
		Height:            777777.888,
		HitTag:            "DIV",
		HitID:             strings.Repeat("a", 100),
		HitClass:          strings.Repeat("b", 200),
		HitText:           strings.Repeat("c", 200),
		HitLeft:           111.222,
		HitTop:            333.444,
		HitWidth:          555.666,
		HitHeight:         777.888,
		TargetContainsHit: false,
		URL:               "https://example.com/" + strings.Repeat("x", 300) + "?leak=1",
		Title:             strings.Repeat("title", 100),
	}
	got := s.evidenceString()
	if got == "" {
		t.Fatal("evidenceString() returned empty")
	}
	if len(got) > maxEvidenceBytes {
		t.Errorf("evidence exceeds %d bytes: got %d\nraw: %s", maxEvidenceBytes, len(got), got)
	}
	// 验证 fallback 仍含关键字段
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, got)
	}
	for _, k := range []string{"targetTag", "hitTag", "contains", "targetCx", "targetCy",
		"targetLeft", "targetTop", "targetWidth", "targetHeight",
		"hitLeft", "hitTop", "hitWidth", "hitHeight"} {
		if _, ok := parsed[k]; !ok {
			t.Errorf("fallback evidence missing field %q", k)
		}
	}
	// 不含 _truncated 标记
	if _, ok := parsed["_truncated"]; ok {
		t.Error("fallback evidence should not contain _truncated")
	}
}

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"https://example.com/path", "https://example.com/path"},
		{"https://example.com/path?a=1&b=2", "https://example.com/path"},
		{"https://example.com/path#frag", "https://example.com/path"},
		{"https://example.com/path?a=1#frag", "https://example.com/path"},
		{"https://user:pass@example.com/path", "https://example.com/path"},
		{"http://example.com:8080/path?token=secret", "http://example.com:8080/path"},
	}
	for _, tc := range tests {
		got := sanitizeURL(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestInteractableSnapshotJSONRoundTrip(t *testing.T) {
	s := interactableSnapshot{
		Connected: true, Visible: true, Clickable: false,
		Left: 10, Top: 20, Width: 100, Height: 50,
		Reason: "obscured",
		TargetTag: "INPUT", TargetID: "q", TargetClass: "search",
		TargetCX: 60, TargetCY: 45,
		HitTag: "DIV", HitID: "overlay", HitClass: "modal",
		HitText: "loading", HitLeft: 0, HitTop: 0, HitWidth: 800, HitHeight: 600,
		TargetContainsHit: false,
		URL: "https://example.com", Title: "Test",
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// TargetContainsHit=false 不能因 omitempty 丢失
	if !bytes.Contains(data, []byte(`"targetContainsHit":false`)) {
		t.Errorf("TargetContainsHit=false omitted from JSON: %s", data)
	}

	var got interactableSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.TargetContainsHit {
		t.Error("TargetContainsHit should remain false after round-trip")
	}
	if got.Reason != "obscured" || got.TargetTag != "INPUT" || got.HitTag != "DIV" {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

func TestObscuredErrorMessageLength(t *testing.T) {
	s := interactableSnapshot{
		Reason:            "obscured",
		TargetTag:         "INPUT",
		TargetID:          strings.Repeat("x", 100),
		TargetClass:       strings.Repeat("y", 200),
		TargetPlaceholder: strings.Repeat("z", 100),
		TargetCX:          123456.789,
		TargetCY:          987654.321,
		Left:              111111.222,
		Top:               333333.444,
		Width:             555555.666,
		Height:            777777.888,
		HitTag:            "DIV",
		HitID:             strings.Repeat("a", 100),
		HitClass:          strings.Repeat("b", 200),
		HitText:           strings.Repeat("c", 200),
		HitLeft:           111.222,
		HitTop:            333.444,
		HitWidth:          555.666,
		HitHeight:         777.888,
		TargetContainsHit: false,
		URL:               "https://example.com/" + strings.Repeat("x", 300),
		Title:             strings.Repeat("title", 100),
	}
	evidence := s.evidenceString()
	if evidence == "" {
		t.Fatal("evidenceString() returned empty for obscured")
	}
	errMsg := fmt.Sprintf("元素不可点击: obscured%s", evidence)
	if len(errMsg) > maxErrorBytes {
		t.Errorf("完整 obscured 错误消息 %d bytes > 上限 %d", len(errMsg), maxErrorBytes)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(evidence), &parsed); err != nil {
		t.Fatalf("evidence 不是合法 JSON: %v", err)
	}
}
