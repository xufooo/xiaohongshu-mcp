package xiaohongshu

import "testing"

func TestLoginPageStateRequiresExplicitAuthenticationSignals(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "selector-only equivalent has no state", raw: `{}`, want: false},
		{name: "authenticated user", raw: `{"authenticated":true,"guest":false,"userId":"u1"}`, want: true},
		{name: "guest", raw: `{"authenticated":true,"guest":true,"userId":"u1"}`, want: false},
		{name: "missing user id", raw: `{"authenticated":true,"guest":false,"userId":""}`, want: false},
		{name: "authentication not explicit", raw: `{"guest":false,"userId":"u1"}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := parseLoginPageState(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := state.authenticated(); got != tc.want {
				t.Fatalf("authenticated() = %v, want %v", got, tc.want)
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
