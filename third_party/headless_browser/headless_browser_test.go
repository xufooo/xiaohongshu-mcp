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
