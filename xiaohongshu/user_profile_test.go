package xiaohongshu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadUserProfileFixture 读取真实结构脱敏 fixture。
func loadUserProfileFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "user_profile_state_note.json"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	return string(data)
}

// TestParseUserProfileStateActiveNoteOnly 真实 fixture 只返回 active note 分区。
func TestParseUserProfileStateActiveNoteOnly(t *testing.T) {
	resp, err := parseUserProfileState(loadUserProfileFixture(t))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(resp.Feeds) != 2 {
		t.Fatalf("应只返回 note 分区 2 条, 实际 %d", len(resp.Feeds))
	}
	if resp.Feeds[0].ID != "note-placeholder-1" || resp.Feeds[1].ID != "note-placeholder-2" {
		t.Fatalf("返回的 feed 不是 note 分区: %+v", resp.Feeds)
	}
	if resp.UserBasicInfo.Nickname != "用户A" {
		t.Fatalf("basicInfo 未解析: %+v", resp.UserBasicInfo)
	}
	if len(resp.Interactions) != 2 {
		t.Fatalf("interactions 未解析: %+v", resp.Interactions)
	}
}

// TestParseUserProfileStateFavLikedNotMixed fav/liked 占位 ID 不得混入。
func TestParseUserProfileStateFavLikedNotMixed(t *testing.T) {
	resp, err := parseUserProfileState(loadUserProfileFixture(t))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, f := range resp.Feeds {
		if strings.Contains(f.ID, "fav-") || strings.Contains(f.ID, "liked-") {
			t.Fatalf("收藏/点赞分区混入: %s", f.ID)
		}
	}
}

// TestParseUserProfileStateUnsupportedQuery query 与默认 note 不符时失败。
func TestParseUserProfileStateUnsupportedQuery(t *testing.T) {
	raw := strings.Replace(loadUserProfileFixture(t), `"query": "note"`, `"query": "fav"`, 1)
	if _, err := parseUserProfileState(raw); err == nil {
		t.Fatal("query=fav 应失败")
	}
}

// TestParseUserProfileStateActiveTabWrapper activeTab 的 value/_value wrapper 不静默降级。
func TestParseUserProfileStateActiveTabWrapper(t *testing.T) {
	base := loadUserProfileFixture(t)
	// 构造合法 wrapper：{"activeTab": {"value": {"index":0,"query":"fav"}}}
	valueTab := strings.Replace(base, `"activeTab": {`, `"activeTab": {"value": {`, 1)
	valueTab = strings.Replace(valueTab, `"query": "note"`, `"query": "fav"}`, 1)
	if _, err := parseUserProfileState(valueTab); err == nil || !strings.Contains(err.Error(), "unsupported profile tab") {
		t.Fatalf("activeTab value wrapper query=fav 应报 unsupported profile tab, 实际 %v", err)
	}
	underTab := strings.Replace(base, `"activeTab": {`, `"activeTab": {"_value": {`, 1)
	underTab = strings.Replace(underTab, `"query": "note"`, `"query": "fav"}`, 1)
	if _, err := parseUserProfileState(underTab); err == nil || !strings.Contains(err.Error(), "unsupported profile tab") {
		t.Fatalf("activeTab _value wrapper query=fav 应报 unsupported profile tab, 实际 %v", err)
	}
}

// TestParseUserProfileStateNilUserPageData userPageData 为 null 时 fail-closed（不 panic）。
func TestParseUserProfileStateNilUserPageData(t *testing.T) {
	raw := `{"userPageData":null,"notes":[[{"id":"n1","noteCard":{}}]],"activeTab":{"index":0,"query":"note"}}`
	if _, err := parseUserProfileState(raw); err == nil {
		t.Fatal("userPageData=null 应失败")
	}
	raw2 := `{"userPageData":{"value":null},"notes":[[{"id":"n1","noteCard":{}}]],"activeTab":{"index":0,"query":"note"}}`
	if _, err := parseUserProfileState(raw2); err == nil {
		t.Fatal("userPageData.value=null 应失败")
	}
}

// TestParseUserProfileStateIndexOutOfRange activeTab.index 越界失败。
func TestParseUserProfileStateIndexOutOfRange(t *testing.T) {
	raw := `{"userPageData":{"basicInfo":{"nickname":"x"},"interactions":[]},"notes":[[{"id":"n1","noteCard":{}}],[],[]],"activeTab":{"index":9,"query":"note"}}`
	if _, err := parseUserProfileState(raw); err == nil {
		t.Fatal("index 越界应失败")
	}
}

// TestParseUserProfileStateMissingUserPageData userPageData 缺失失败。
func TestParseUserProfileStateMissingUserPageData(t *testing.T) {
	raw := `{"notes":[[{"id":"n1","noteCard":{}}]],"index":0,"query":"note"}`
	if _, err := parseUserProfileState(raw); err == nil {
		t.Fatal("userPageData 缺失应失败")
	}
}

// TestParseUserProfileStateMissingNotes notes 缺失失败。
func TestParseUserProfileStateMissingNotes(t *testing.T) {
	raw := `{"userPageData":{"basicInfo":{"nickname":"x"}},"index":0,"query":"note"}`
	if _, err := parseUserProfileState(raw); err == nil {
		t.Fatal("notes 缺失应失败")
	}
}

// TestParseUserProfileStateValueAndUnderValueNotes notes 的 value/_value 双 wrapper 均能解析。
func TestParseUserProfileStateValueAndUnderValueNotes(t *testing.T) {
	// value wrapper 形态（JSON.stringify 经 toJSON 展开为 value）
	valueWrapped := `{"userPageData":{"value":{"basicInfo":{"nickname":"x"},"interactions":[]}},"notes":{"value":[[{"id":"v1","noteCard":{}}],[],[]]},"activeTab":{"index":0,"query":"note"}}`
	resp, err := parseUserProfileState(valueWrapped)
	if err != nil {
		t.Fatalf("value wrapper 解析失败: %v", err)
	}
	if len(resp.Feeds) != 1 || resp.Feeds[0].ID != "v1" {
		t.Fatalf("value wrapper 返回错误: %+v", resp.Feeds)
	}

	// _value wrapper 形态（Vue ref 内部字段）
	underWrapped := `{"userPageData":{"_value":{"basicInfo":{"nickname":"x"},"interactions":[]}},"notes":{"_value":[[{"id":"u1","noteCard":{}}],[],[]]},"activeTab":{"index":0,"query":"note"}}`
	resp2, err := parseUserProfileState(underWrapped)
	if err != nil {
		t.Fatalf("_value wrapper 解析失败: %v", err)
	}
	if len(resp2.Feeds) != 1 || resp2.Feeds[0].ID != "u1" {
		t.Fatalf("_value wrapper 返回错误: %+v", resp2.Feeds)
	}
}
