package xiaohongshu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScrollYIsSingleSource 阅读位置只能由 xhsScrollYJS 里的 scrollY() 读，
// 不允许再内联 window.scrollY（详情页/搜索页窗口不可滚，内联值恒为 0）。
func TestScrollYIsSingleSource(t *testing.T) {
	if !strings.Contains(xhsScrollYJS, "const scrollY = ()") {
		t.Fatal("共享片段必须定义 scrollY()")
	}
	// 两个容器来自 D.15 实测（详情页 .note-scroller、搜索页 .search-layout-wrapper）
	for _, container := range []string{".note-scroller", ".search-layout-wrapper"} {
		if !strings.Contains(xhsScrollYJS, container) {
			t.Fatalf("共享片段应包含实测命中的滚动容器 %s", container)
		}
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob 失败: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "js_scroll.go" {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", file, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, "window.scrollY") {
				t.Fatalf("%s:%d 内联读取 window.scrollY；应改用共享 scrollY()：%s",
					file, i+1, strings.TrimSpace(line))
			}
		}
	}
}
