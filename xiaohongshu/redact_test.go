package xiaohongshu

import (
	"strings"
	"testing"
)

func TestRedactSensitiveURL(t *testing.T) {
	raw := "https://edith.xiaohongshu.com/api/sns/web/v1/feed?keyword=kimi&xsec_token=secret-xsec&xsecToken=secret-camel&access_token=secret-access&a1=secret-cookie"

	redacted := redactSensitiveURL(raw)

	for _, leaked := range []string{"secret-xsec", "secret-camel", "secret-access", "secret-cookie"} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("redacted URL leaked %q: %s", leaked, redacted)
		}
	}
	if !strings.Contains(redacted, "keyword=kimi") {
		t.Fatalf("redacted URL should keep non-sensitive query params: %s", redacted)
	}
}

func TestSummarizeNetworkBodyRedactsSensitiveValues(t *testing.T) {
	body := `{
		"xsec_token":"secret-xsec",
		"web_session":"secret-session",
		"authorization":"Bearer secret-auth",
		"nested":{"token":"secret-token"},
		"title":"visible"
	}`

	summary := summarizeNetworkBody(body)

	for _, leaked := range []string{"secret-xsec", "secret-session", "secret-auth", "secret-token"} {
		if strings.Contains(summary, leaked) {
			t.Fatalf("network summary leaked %q: %s", leaked, summary)
		}
	}
	if !strings.Contains(summary, "visible") {
		t.Fatalf("network summary should keep non-sensitive content: %s", summary)
	}
}
