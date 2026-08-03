package xiaohongshu

import (
	"encoding/json"
	"testing"
)

func TestParseNotificationTab(t *testing.T) {
	cases := []struct {
		raw     string
		want    NotificationTab
		wantErr bool
	}{
		{"", TabMentions, false},
		{"mentions", TabMentions, false},
		{"LIKES", TabLikes, false},
		{" connections ", TabConnections, false},
		{"bad", "", true},
	}
	for _, c := range cases {
		got, err := ParseNotificationTab(c.raw)
		if c.wantErr {
			if err == nil {
				t.Fatalf("ParseNotificationTab(%q) 期望报错，实际 nil", c.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseNotificationTab(%q) 意外报错: %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("ParseNotificationTab(%q) = %q, 期望 %q", c.raw, got, c.want)
		}
	}
}

func TestDecodeNotificationCount(t *testing.T) {
	// 真实结构：notificationCount 是对象，含 mentions/likes/connections/unreadCount。
	v, err := decodeNotificationCount(`{"has_state":true,"mentions":2,"likes":3,"connections":1,"unread":6}`)
	if err != nil {
		t.Fatalf("解码未读数失败: %v", err)
	}
	if v.Mentions != 2 || v.Likes != 3 || v.Connections != 1 || v.Unread != 6 {
		t.Errorf("未读数明细错误: %+v", v)
	}
	if _, err := decodeNotificationCount(`{"has_state":false}`); err == nil {
		t.Fatal("state 缺失应报错")
	}
	if _, err := decodeNotificationCount(`{"has_state":true,"count_missing":true}`); err == nil {
		t.Fatal("count 缺失应报错")
	}
}

func TestParseNotificationAfterCount(t *testing.T) {
	if _, err := decodeNotificationCount("bad json"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestParseNotificationPayload(t *testing.T) {
	raw := `{
		"hasMore": true,
		"messageList": [
			{
				"id": "n1", "type": "comment", "title": "评论", "time": 1700000000,
				"userInfo": {"userid": "u1", "nickname": "小明", "xsecToken": "tok1"},
				"commentInfo": {"id": "c1", "content": "你好世界", "liked": true, "illegalInfo": {"illegalStatus": "NORMAL"}},
				"itemInfo": {"id": "n1", "type": "note_info", "content": "标题A", "xsecToken": "xt1", "illegalInfo": {"illegalStatus": "NORMAL"}}
			},
			{
				"id": "n2", "type": "fans", "title": "关注",
				"user": {"userid": "u2", "nickname": "小红"},
				"commentInfo": {"id": "", "content": "", "illegalInfo": {}},
				"itemInfo": {"id": "", "type": "", "illegalInfo": {}}
			},
			{
				"id": "n3", "type": "comment", "title": "已删除",
				"userInfo": {"userid": "u3", "nickname": "张三"},
				"commentInfo": {"id": "c3", "content": "被删", "illegalInfo": {"illegalStatus": "DELETED"}},
				"itemInfo": {"type": "note_info", "id": "x", "illegalInfo": {}}
			}
		]
	}`
	var payload notificationPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("解析真实结构失败: %v", err)
	}
	if len(payload.MessageList) != 3 {
		t.Fatalf("messageList 应解析 3 条，实际 %d", len(payload.MessageList))
	}
	if !payload.HasMore {
		t.Error("hasMore 应为 true")
	}
	r0 := payload.MessageList[0]
	if r0.from().UserID != "u1" || r0.from().Nickname != "小明" {
		t.Errorf("userInfo.userid 解析错误: %+v", r0.from())
	}
	if r0.Comment.ID != "c1" || r0.Comment.Content != "你好世界" || !r0.Comment.Liked {
		t.Errorf("commentInfo 解析错误: %+v", r0.Comment)
	}
	if !r0.visible() {
		t.Error("NORMAL 状态应可见")
	}
	if payload.MessageList[2].visible() {
		t.Error("DELETED 评论应不可见")
	}
}

func TestNotificationVisible(t *testing.T) {
	valid := rawNotification{Comment: rawComment{ID: "c", Content: "x", Illegal: rawIllegal{Status: statusNormal}}}
	if !valid.visible() {
		t.Error("NORMAL 应可见")
	}
	deleted := rawNotification{Comment: rawComment{ID: "c", Content: "x", Illegal: rawIllegal{Status: "DELETED"}}}
	if deleted.visible() {
		t.Error("DELETED 应不可见")
	}
	itemIllegal := rawNotification{Comment: rawComment{ID: "c"}, Item: rawItem{ID: "i", Type: itemTypeNote, Illegal: rawIllegal{Status: "CHANGE"}}}
	if itemIllegal.visible() {
		t.Error("item 非法应不可见")
	}
}

func TestNotificationItemFingerprint(t *testing.T) {
	if notificationItemFingerprint("u1", "  甲 ", " 评论 ") != "u1|甲|评论" {
		t.Errorf("指纹归一化错误: %q", notificationItemFingerprint("u1", "  甲 ", " 评论 "))
	}
}

func TestMatchNotificationDOM(t *testing.T) {
	entry := rawNotification{
		UserInfo: rawUser{UserID: "u1", Nickname: "小明"},
		Comment:  rawComment{ID: "c1", Content: "你好世界"},
	}
	dom := notificationDOMSnapshot{Items: []notificationDOMItem{
		{UserID: "u2", Nickname: "小红", Content: "别的"},
		{UserID: "u1", Nickname: "小明", Content: "你好世界"},
	}}
	if matched := matchNotificationDOM(entry, notificationDOMSnapshot{}); matched != nil {
		t.Errorf("空 DOM 不应匹配，实际 %+v", matched)
	}
	matched := matchNotificationDOM(entry, dom)
	if matched == nil || matched.UserID != "u1" {
		t.Fatalf("唯一匹配应命中 u1, 实际 %+v", matched)
	}
	ambiguous := notificationDOMSnapshot{Items: []notificationDOMItem{
		{UserID: "u1", Nickname: "小明", Content: "你好世界"},
		{UserID: "u1", Nickname: "小明", Content: "你好世界"},
	}}
	if m := matchNotificationDOM(entry, ambiguous); m != nil {
		t.Errorf("歧义应返回 nil, 实际 %+v", m)
	}
}

func TestConvertNotifications(t *testing.T) {
	raw := []rawNotification{
		{
			ID: "n1", Type: "comment", Time: 1700000000,
			UserInfo: rawUser{UserID: "u1", Nickname: "小明", XsecToken: "tok1"},
			Comment:  rawComment{ID: "c1", Content: "赞", Liked: true, Illegal: rawIllegal{Status: statusNormal}},
			Item:     rawItem{ID: "note1", Type: itemTypeNote, Content: "标题A", XsecToken: "xt1"},
		},
		{
			ID: "n2", Type: "comment",
			UserInfo: rawUser{UserID: "u2", Nickname: "小红"},
			Comment:  rawComment{ID: "c2", Content: "删", Illegal: rawIllegal{Status: "DELETED"}},
		},
	}
	dom := notificationDOMSnapshot{Items: []notificationDOMItem{
		{UserID: "u1", Nickname: "小明", Content: "赞", HasLike: true, HasReply: true},
	}}
	items, filtered := convertNotifications(raw, dom, TabMentions, func(entry rawNotification, i int) string {
		return "nr_" + entry.Comment.ID
	})
	if filtered != 1 {
		t.Fatalf("应过滤 1 条不可见, 实际 %d", filtered)
	}
	if len(items) != 1 {
		t.Fatalf("应剩 1 条, 实际 %d", len(items))
	}
	it := items[0]
	if it.CommentID != "c1" || it.CommentText != "赞" || !it.Liked {
		t.Errorf("commentInfo 未落对外结构: %+v", it)
	}
	if it.FeedID != "note1" || it.FeedXsecToken != "xt1" {
		t.Errorf("note_info 未提取 feed: %+v", it)
	}
	if !it.Actionable || it.NotificationRef != "nr_c1" {
		t.Errorf("actionable/ref 错误: %+v", it)
	}
	if it.Time != 1700000000 {
		t.Errorf("time 应为 int64 时间戳: %+v", it)
	}
}

func TestMergeNotificationItems(t *testing.T) {
	existing := []NotificationItem{{ID: "a", NotificationRef: "r1"}, {ID: "b", NotificationRef: "r2"}}
	fresh := []NotificationItem{{ID: "b", NotificationRef: "r2"}, {ID: "c", NotificationRef: "r3"}}
	merged := mergeNotificationItems(existing, fresh)
	if len(merged) != 3 {
		t.Fatalf("应去重累积为 3, 实际 %d", len(merged))
	}
	if merged[2].ID != "c" {
		t.Errorf("新增条目应追加在尾部: %+v", merged)
	}
}