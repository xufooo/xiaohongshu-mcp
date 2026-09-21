package xiaohongshu

import "testing"

// TestOnlyNotesDropsNonNoteEntries 对齐上游 #776 的判据：
// live_v2（直播卡片）与 hot_query（搜索热词）没有 noteCard，属于噪音；
// 视频笔记的 modelType 同样是 note，不能被误伤。
func TestOnlyNotesDropsNonNoteEntries(t *testing.T) {
	feeds := []Feed{
		{ID: "note-image", ModelType: "note", NoteCard: NoteCard{Type: "normal", DisplayTitle: "图文"}},
		{ID: "note-video", ModelType: "note", NoteCard: NoteCard{Type: "video", DisplayTitle: "视频"}},
		{ID: "", ModelType: "live_v2", NoteCard: NoteCard{}},
		{ID: "", ModelType: "hot_query", NoteCard: NoteCard{}},
		{ID: "untitled", ModelType: "note", NoteCard: NoteCard{Type: "normal"}},
	}
	got := onlyNotes(feeds)
	if len(got) != 3 {
		t.Fatalf("应保留 3 条笔记（含无标题笔记与视频笔记），实得 %d: %+v", len(got), got)
	}
	for _, feed := range got {
		if feed.ModelType != modelTypeNote {
			t.Fatalf("过滤后仍有非笔记条目: %+v", feed)
		}
	}
	hasVideo := false
	for _, feed := range got {
		if feed.NoteCard.Type == "video" {
			hasVideo = true
		}
	}
	if !hasVideo {
		t.Fatal("视频笔记被误伤")
	}
}
