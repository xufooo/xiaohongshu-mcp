package configs

import "testing"

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"128MiB", 128 << 20, true},
		{"128MB", 128 << 20, true},
		{"1GiB", 1 << 30, true},
		{"512KiB", 512 << 10, true},
		{"134217728", 134217728, true},
		{"64B", 64, true},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, err := parseByteSize(c.raw)
		if c.ok && err != nil {
			t.Fatalf("parseByteSize(%q) 不应报错: %v", c.raw, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("parseByteSize(%q) 应报错", c.raw)
		}
		if c.ok && got != c.want {
			t.Fatalf("parseByteSize(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}
