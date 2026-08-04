package headless_browser

import (
	"reflect"
	"testing"

	"github.com/go-rod/rod/lib/launcher"
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