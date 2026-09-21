package xiaohongshu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestWaitStatsRegistry 等待观测量：按 kind 累加次数/累计/最大值。
func TestWaitStatsRegistry(t *testing.T) {
	observeWait("test:kind", 120*time.Millisecond)
	observeWait("test:kind", 80*time.Millisecond)
	snapshot := WaitStatsSnapshot()
	stat, ok := snapshot["test:kind"]
	if !ok {
		t.Fatal("应记录 test:kind")
	}
	if stat.Count != 2 || stat.TotalMs != 200 || stat.MaxMs != 120 {
		t.Fatalf("观测值错误: %+v", stat)
	}
	// 快照是拷贝：改它不影响内部状态
	stat.Count = 99
	if WaitStatsSnapshot()["test:kind"].Count != 2 {
		t.Fatal("快照必须是拷贝")
	}
}

// TestPageSignalJSShape 页面内等待信号必须靠 MutationObserver + resolve，不得用轮询定时器空转。
func TestPageSignalJSShape(t *testing.T) {
	for _, want := range []string{"new MutationObserver", "observer.observe", "resolve(reason)", "finished"} {
		if !strings.Contains(xhsPageSignalJS, want) {
			t.Fatalf("等待信号片段缺少 %q", want)
		}
	}
	if strings.Contains(xhsPageSignalJS, "setInterval") {
		t.Fatal("不应使用 setInterval 轮询")
	}
}
