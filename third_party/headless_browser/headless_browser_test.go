package headless_browser

import (
	"reflect"
	"testing"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
)

func TestParseLaunchArg(t *testing.T) {
	tests := []struct {
		input    string
		name     string
		value    string
		hasValue bool
		ok       bool
	}{
		{"--lang=zh-CN", "lang", "zh-CN", true, true},
		{"--disable-gpu", "disable-gpu", "", false, true},
		{"lang=zh-CN", "", "", false, false},
		{"--bad flag", "", "", false, false},
	}

	for _, test := range tests {
		name, value, hasValue, ok := parseLaunchArg(test.input)
		if name != test.name || value != test.value || hasValue != test.hasValue || ok != test.ok {
			t.Fatalf("parseLaunchArg(%q) = (%q, %q, %v, %v)", test.input, name, value, hasValue, ok)
		}
	}
}

func TestApplyCloakLauncherProfile(t *testing.T) {
	l := launcher.New()
	if !l.Has("enable-automation") {
		t.Fatal("launcher default should include enable-automation")
	}

	// disable-features 不被 Cloak profile 修改，保持 rod 默认值不变。
	before, ok := l.GetFlags("disable-features")
	if !ok || len(before) == 0 {
		t.Fatal("launcher default should include disable-features")
	}
	before = append([]string(nil), before...)

	applyCloakLauncherProfile(l)

	if l.Has("enable-automation") {
		t.Fatal("cloak launcher profile should remove enable-automation")
	}
	after, ok := l.GetFlags("disable-features")
	if !ok || !reflect.DeepEqual(after, before) {
		t.Fatalf("disable-features = %v, want unchanged %v", after, before)
	}
}

// TestWithStealthAlias 验证 WithStealth 与 WithStealthJS 控制同一字段。
func TestWithStealthAlias(t *testing.T) {
	cfg := newDefaultConfig()
	WithStealthJS(false)(cfg)
	if cfg.StealthJS {
		t.Fatal("WithStealthJS(false) 应设置 StealthJS=false")
	}

	cfg2 := newDefaultConfig()
	WithStealth(false)(cfg2)
	if cfg2.StealthJS {
		t.Fatal("WithStealth(false) 应控制同一字段 StealthJS=false")
	}
}

// TestWithExtraFlagsDefensiveCopy 验证 WithExtraFlags 防御性复制 map。
func TestWithExtraFlagsDefensiveCopy(t *testing.T) {
	flagsMap := map[string]string{"fingerprint-brand": "Chrome"}
	cfg := newDefaultConfig()
	WithExtraFlags(flagsMap)(cfg)
	flagsMap["fingerprint-brand"] = "Mutated"
	if cfg.ExtraFlags["fingerprint-brand"] != "Chrome" {
		t.Fatal("WithExtraFlags 必须复制 map，调用方后续修改不能影响配置")
	}
}

// TestWithExtraArgsDefensiveCopy 验证 WithExtraArgs 防御性复制 slice。
func TestWithExtraArgsDefensiveCopy(t *testing.T) {
	args := []string{"--lang=zh-CN"}
	cfg := newDefaultConfig()
	WithExtraArgs(args)(cfg)
	args[0] = "--mutated"
	if cfg.ExtraArgs[0] != "--lang=zh-CN" {
		t.Fatal("WithExtraArgs 必须复制 slice，调用方后续修改不能影响配置")
	}
}

func TestPrimaryLang(t *testing.T) {
	if primaryLang("zh-CN") != "zh" {
		t.Fatalf("primaryLang(zh-CN) = %q, want zh", primaryLang("zh-CN"))
	}
	if primaryLang("en-US") != "en" {
		t.Fatalf("primaryLang(en-US) = %q, want en", primaryLang("en-US"))
	}
	if primaryLang("en") != "en" {
		t.Fatalf("primaryLang(en) = %q, want en", primaryLang("en"))
	}
}

// TestAutoFingerprintPlatform 验证按运行 OS 返回合法平台值。
func TestAutoFingerprintPlatform(t *testing.T) {
	platform := autoFingerprintPlatform()
	if platform != "windows" && platform != "macos" {
		t.Fatalf("autoFingerprintPlatform() = %q, 应为 windows 或 macos", platform)
	}
}
func TestWithFingerprintSeedConfig(t *testing.T) {
	c := newDefaultConfig()
	WithFingerprintSeed(20260804)(c)
	if c.FingerprintSeed != 20260804 {
		t.Fatalf("FingerprintSeed 未写入 Config: %d", c.FingerprintSeed)
	}
	if c.FingerprintSeed == 0 {
		t.Fatalf("显式 seed 不应为 0（0 表示随机）")
	}
}

// TestApplyLowMemoryLauncherProfile 低开销档必须逐项落下 flag，并带上堆上限与进程上限。
func TestApplyLowMemoryLauncherProfile(t *testing.T) {
	l := applyLowMemoryLauncherProfile(launcher.New(), 192, 2)

	for _, name := range []string{
		"disable-extensions",
		"disable-component-update",
		"no-default-browser-check",
		"aggressive-cache-discard",
		"mute-audio",
	} {
		if !l.Has(flags.Flag(name)) {
			t.Fatalf("低开销档缺少 flag %s", name)
		}
	}

	// WebGL 指纹相关 flag 不得出现在默认档：--disable-software-rasterizer 会让 WebGL 直接消失。
	for _, forbidden := range []string{"disable-gpu", "disable-software-rasterizer"} {
		if l.Has(flags.Flag(forbidden)) {
			t.Fatalf("低开销档不应默认添加 %s（见 docs/pi3b-optimization.md 实测）", forbidden)
		}
	}

	jsFlags, ok := l.GetFlags("js-flags")
	if !ok || len(jsFlags) != 1 || jsFlags[0] != "--max-old-space-size=192" {
		t.Fatalf("js-flags = %v, want [--max-old-space-size=192]", jsFlags)
	}
	limit, ok := l.GetFlags("renderer-process-limit")
	if !ok || len(limit) != 1 || limit[0] != "2" {
		t.Fatalf("renderer-process-limit = %v, want [2]", limit)
	}
}

// TestApplyLowMemoryLauncherProfileDefaults 未给出参数时用默认值（256MB / 2 个 renderer）。
func TestApplyLowMemoryLauncherProfileDefaults(t *testing.T) {
	l := applyLowMemoryLauncherProfile(launcher.New(), 0, 0)

	jsFlags, _ := l.GetFlags("js-flags")
	if len(jsFlags) != 1 || jsFlags[0] != "--max-old-space-size=256" {
		t.Fatalf("默认 js-flags = %v, want [--max-old-space-size=256]", jsFlags)
	}
	limit, _ := l.GetFlags("renderer-process-limit")
	if len(limit) != 1 || limit[0] != "2" {
		t.Fatalf("默认 renderer-process-limit = %v, want [2]", limit)
	}
}

// TestWithLowMemoryProfileConfig 选项必须写入 Config。
func TestWithLowMemoryProfileConfig(t *testing.T) {
	cfg := newDefaultConfig()
	WithLowMemoryProfile(128, 1)(cfg)
	if !cfg.LowMemory || cfg.JSHeapMB != 128 || cfg.RendererLimit != 1 {
		t.Fatalf("WithLowMemoryProfile 未写入 Config: %+v", cfg)
	}
}

// TestWithBlockedURLsDefensiveCopy 拦截列表必须防御性复制，空列表等同不拦截。
func TestWithBlockedURLsDefensiveCopy(t *testing.T) {
	patterns := []string{"*.mp4*"}
	cfg := newDefaultConfig()
	WithBlockedURLs(patterns)(cfg)
	patterns[0] = "*.mutated*"
	if len(cfg.BlockedURLs) != 1 || cfg.BlockedURLs[0] != "*.mp4*" {
		t.Fatalf("WithBlockedURLs 必须复制切片，got %v", cfg.BlockedURLs)
	}

	cfg2 := newDefaultConfig()
	WithBlockedURLs(nil)(cfg2)
	if cfg2.BlockedURLs != nil {
		t.Fatalf("空列表应表示不拦截，got %v", cfg2.BlockedURLs)
	}
}

// 空 cookie 列表必须"什么都不做"。
// 旧实现走 browser.SetCookies(nil)，而 rod 把 nil 实现成 Storage.clearCookies，
// 于是空 cookies.json（只有 seed、还没存过 cookie）会在每次启动清空持久 profile 的登录态。
// 这里故意传 nil browser：只要函数还去碰 browser 就会 panic，被 recover 成 error 暴露出来。
func TestSetBrowserCookiesEmptyListIsNoOp(t *testing.T) {
	if err := setBrowserCookies(nil, nil); err != nil {
		t.Fatalf("空 cookie 列表不应产生任何浏览器调用: %v", err)
	}
}
