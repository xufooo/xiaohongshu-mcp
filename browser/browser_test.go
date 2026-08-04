package browser

import (
	"testing"

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