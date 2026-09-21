package xiaohongshu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoBareVisibleHelperInJS 防止页面内 JS 再引用已收敛掉的 visible() 帮助函数。
// 这类问题 Go 编译和普通单测都发现不了，只会在真机上以 ReferenceError 出现
// （实测踩过：.filter(visible) 与 .find(visible) 两种写法各漏一处）。
func TestNoBareVisibleHelperInJS(t *testing.T) {
	// 只匹配「调用或把函数当值传」的形态，避免误伤 Go 局部变量（visible, err := …）。
	forbidden := []string{
		"(visible)",
		"visible(el",
		"visible(navSearchInput",
		"visible(searchInput",
		"!visible(",
		"&& visible(",
		"|| visible(",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob 失败: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "js_visible.go" {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", file, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "func ") {
				continue
			}
			for _, bad := range forbidden {
				if strings.Contains(line, bad) {
					t.Fatalf("%s:%d 仍引用已收敛的 visible 帮助函数（命中 %q）；"+
						"应改用 visibleInCSS/visibleWithSize/visibleOnScreen：%s",
						file, i+1, bad, trimmed)
				}
			}
		}
	}
}
