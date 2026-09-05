package browser

import (
	"strings"
	"testing"
	"time"

	"github.com/xpzouying/headless_browser"
)

func TestCloakFingerprintEnabled(t *testing.T) {
	cases := []struct {
		cloak   bool
		ua      string
		enabled bool
	}{
		{true, "", true},              // Cloak + 无 UA：启用 fingerprint
		{true, "Mozilla/5.0 (X11)", false}, // Cloak + 显式 UA：跳过 fingerprint
		{false, "", false},             // 普通 Chrome：不启用
	}
	for _, c := range cases {
		if got := cloakFingerprintEnabled(c.cloak, c.ua); got != c.enabled {
			t.Errorf("cloakFingerprintEnabled(%v, %q) = %v, want %v", c.cloak, c.ua, got, c.enabled)
		}
	}
}

func TestCloakFingerprintOptions(t *testing.T) {
	opts := cloakFingerprintOptions(42, "zh-CN")
	if len(opts) != 4 {
		t.Fatalf("cloakFingerprintOptions 应返回 4 个选项, got %d", len(opts))
	}
	cfg := &headless_browser.Config{}
	for _, o := range opts {
		o(cfg)
	}
	if !cfg.Fingerprint {
		t.Error("应启用 fingerprint")
	}
	if cfg.FingerprintSeed != 42 {
		t.Errorf("FingerprintSeed = %d, want 42", cfg.FingerprintSeed)
	}
	if cfg.Language != "zh-CN" {
		t.Errorf("Language = %q, want zh-CN", cfg.Language)
	}
	if cfg.ExtraFlags["fingerprint-brand"] != "Chrome" {
		t.Errorf("ExtraFlags 应含 fingerprint-brand=Chrome, got %v", cfg.ExtraFlags)
	}
}
func TestMaskProxyCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"无认证", "http://proxy.example.com:8080", "http://proxy.example.com:8080"},
		{"仅用户名", "http://user123@proxy.example.com:8080", "http://***@proxy.example.com:8080"},
		{"用户名+密码", "http://user123:pass456@proxy.example.com:8080", "http://***:***@proxy.example.com:8080"},
		{"userinfo 转义 @", "http://user%40x:p%40ss@proxy.example.com:8080", "http://***:***@proxy.example.com:8080"},
		{"scheme-relative 带认证", "//user123:pass456@proxy.example.com:8080", "//***:***@proxy.example.com:8080"},
		{"scheme-relative 仅用户名", "//user123@proxy.example.com:8080", "//***@proxy.example.com:8080"},
		{"路径含 @ 不干扰边界", "http://user123:pass456@proxy.example.com:8080/path@x", "http://***:***@proxy.example.com:8080/path@x"},
		{"无 scheme 带 userinfo fail-closed", "user123:pass456@proxy.example.com:8080", "[invalid-proxy]"},
		{"畸形 URL", "http://[bad url", "[invalid-proxy]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskProxyCredentials(tc.in)
			if got != tc.want {
				t.Fatalf("maskProxyCredentials(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
			for _, secret := range []string{"user123", "pass456"} {
				if strings.Contains(got, secret) {
					t.Fatalf("凭据泄露: %q contains %q", got, secret)
				}
			}
		})
	}
}

func TestIdleCloseDelay(t *testing.T) {
	cases := []struct {
		name         string
		owner        string
		configured   time.Duration
		sessionGrace time.Duration
		want         time.Duration
	}{
		{"普通 owner 保留配置", "browser_operation", 5 * time.Minute, time.Minute, 5 * time.Minute},
		{"session owner 上限 grace", "session:test-id", 5 * time.Minute, time.Minute, time.Minute},
		{"session owner 尊重更短配置", "session:test-id", 30 * time.Second, time.Minute, 30 * time.Second},
		{"session owner 保持零禁用", "session:test-id", 0, time.Minute, 0},
		{"session owner 保持负值禁用", "session:test-id", -1 * time.Second, time.Minute, -1 * time.Second},
		{"无冒号前缀不算 session owner", "session", 5 * time.Minute, time.Minute, 5 * time.Minute},
		{"sessionGrace 零时跟随配置", "session:test-id", 5 * time.Minute, 0, 5 * time.Minute},
		{"sessionGrace 为负时跟随配置", "session:test-id", 5 * time.Minute, -1 * time.Second, 5 * time.Minute},
		{"sessionGrace 大于配置时用配置", "session:test-id", 30 * time.Second, 10 * time.Minute, 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idleCloseDelay(tc.owner, tc.configured, tc.sessionGrace); got != tc.want {
				t.Fatalf("idleCloseDelay(%q, %v, %v) = %v, 期望 %v", tc.owner, tc.configured, tc.sessionGrace, got, tc.want)
			}
		})
	}
}
