package xiaohongshu

import (
	"net/url"
	"strings"
	"testing"
)

func TestRedactSensitiveURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"xsec_token query", "https://www.xiaohongshu.com/explore?id=123&xsec_token=abc123&xsec_source=pc_feed"},
		{"xsecToken camel", "https://www.xiaohongshu.com/explore?id=123&xsecToken=abc123"},
		{"access_token query", "https://api.example.com/v1/data?access_token=secret-token-xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSensitiveURL(tc.raw)
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("脱敏结果无法解析: %q (%v)", got, err)
			}
			// path 保留
			if !strings.HasPrefix(parsed.Path, "/explore") && !strings.HasPrefix(parsed.Path, "/v1/data") {
				t.Fatalf("path 未保留: %q", got)
			}
			// 敏感 query 值被替换为 ***
			q := parsed.Query()
			for _, key := range []string{"xsec_token", "xsecToken", "access_token"} {
				if v := q.Get(key); v != "" && v != "***" {
					t.Fatalf("敏感参数 %s 未脱敏: %q", key, got)
				}
			}
			// 原 token 零残留
			for _, secret := range []string{"abc123", "secret-token-xyz"} {
				if strings.Contains(got, secret) {
					t.Fatalf("sensitive token leaked in %q", got)
				}
			}
		})
	}
}

func TestRedactSensitiveText(t *testing.T) {
	raw := `{"xsec_token":"leak1","xsecToken":"leak2","access_token":"leak3"}`
	got := redactSensitiveText(raw)
	for _, secret := range []string{"leak1", "leak2", "leak3"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sensitive token leaked in %q", got)
		}
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("no redaction marker in %q", got)
	}
}
