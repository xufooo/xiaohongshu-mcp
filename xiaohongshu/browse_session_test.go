package xiaohongshu

import "testing"

func TestMergeOpenedNoteUserFromSearchResult(t *testing.T) {
	tests := []struct {
		name    string
		content OpenedNoteContent
		feed    Feed
		wantUID string
		wantTok string
	}{
		{
			name:    "两个字段都空则补齐",
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "u1", XsecToken: "t1"}}},
			wantUID: "u1",
			wantTok: "t1",
		},
		{
			name:    "两个字段都非空则不覆盖",
			content: OpenedNoteContent{User: User{UserID: "detail-u", XsecToken: "detail-t"}},
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "search-u", XsecToken: "search-t"}}},
			wantUID: "detail-u",
			wantTok: "detail-t",
		},
		{
			name:    "仅 userId 空则只补 userId",
			content: OpenedNoteContent{User: User{XsecToken: "detail-t"}},
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "search-u", XsecToken: "search-t"}}},
			wantUID: "search-u",
			wantTok: "detail-t",
		},
		{
			name:    "仅 xsecToken 空则只补 token",
			content: OpenedNoteContent{User: User{UserID: "detail-u"}},
			feed:    Feed{NoteCard: NoteCard{User: User{UserID: "search-u", XsecToken: "search-t"}}},
			wantUID: "detail-u",
			wantTok: "search-t",
		},
		{
			name:    "搜索结果字段为空则保持空",
			content: OpenedNoteContent{User: User{UserID: "detail-u"}},
			feed:    Feed{NoteCard: NoteCard{User: User{}}},
			wantUID: "detail-u",
			wantTok: "",
		},
		{
			name:    "搜索结果只提供 token 时 userId 保持空",
			feed:    Feed{NoteCard: NoteCard{User: User{XsecToken: "search-t"}}},
			wantUID: "",
			wantTok: "search-t",
		},
		{
			name:    "搜索结果无 token 时结果保持空而非笔记 token",
			feed:    Feed{XsecToken: "note-token", NoteCard: NoteCard{User: User{UserID: "search-u"}}},
			wantUID: "search-u",
			wantTok: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := tt.content
			mergeOpenedNoteUserFromSearchResult(&content, tt.feed)
			if content.User.UserID != tt.wantUID {
				t.Errorf("UserID = %q, want %q", content.User.UserID, tt.wantUID)
			}
			if content.User.XsecToken != tt.wantTok {
				t.Errorf("XsecToken = %q, want %q", content.User.XsecToken, tt.wantTok)
			}
		})
	}
}

func TestMergeOpenedNoteUserDoesNotUseNoteToken(t *testing.T) {
	content := OpenedNoteContent{User: User{}}
	feed := Feed{XsecToken: "note-token", NoteCard: NoteCard{User: User{UserID: "u", XsecToken: "author-token"}}}
	mergeOpenedNoteUserFromSearchResult(&content, feed)
	if content.User.XsecToken != "author-token" {
		t.Errorf("XsecToken = %q, want author token", content.User.XsecToken)
	}
	if content.User.XsecToken == "note-token" {
		t.Errorf("XsecToken 不得冒充笔记 token")
	}
}

func TestMergeOpenedNoteUserFromSearchResultNilContent(t *testing.T) {
	mergeOpenedNoteUserFromSearchResult(nil, Feed{NoteCard: NoteCard{User: User{UserID: "u", XsecToken: "t"}}})
}
