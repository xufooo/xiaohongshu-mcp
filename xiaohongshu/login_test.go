package xiaohongshu

import "testing"

// loginPageState 只用于展示字段（用户名/ID），不参与登录判定 —— 判定是 loginReadySelector 这个 DOM。
func TestParseLoginPageState(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantUser string
		wantName string
	}{
		// 实测已登录快照
		{name: "已登录", raw: `{"guest":false,"userId":"6523ebde000000002b00267b","nickname":"一画一话"}`, wantUser: "6523ebde000000002b00267b", wantName: "一画一话"},
		// 实测未登录快照
		{name: "未登录", raw: `{"guest":true,"userId":"6ab1e4070000000013023403","nickname":""}`, wantUser: "6ab1e4070000000013023403", wantName: ""},
		{name: "空状态", raw: `{}`, wantUser: "", wantName: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := parseLoginPageState(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if state.UserID != tc.wantUser || state.Nickname != tc.wantName {
				t.Fatalf("解析结果 = (%q, %q), want (%q, %q)", state.UserID, state.Nickname, tc.wantUser, tc.wantName)
			}
		})
	}
}

func TestParseLoginPageStateRejectsMissingEvalValue(t *testing.T) {
	for _, raw := range []string{"", "null"} {
		if _, err := parseLoginPageState(raw); err == nil {
			t.Fatalf("raw %q 应视为状态读取失败", raw)
		}
	}
}
