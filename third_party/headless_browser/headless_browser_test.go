package headless_browser

import "testing"

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
