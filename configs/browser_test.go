package configs

import (
	"testing"
	"time"
)

// TestLowResourceProfileExplicit 显式设置 XHS_LOW_RESOURCE 时必须压过架构默认值。
func TestLowResourceProfileExplicit(t *testing.T) {
	t.Setenv("XHS_LOW_RESOURCE", "1")
	if !LowResourceProfile() {
		t.Fatal("XHS_LOW_RESOURCE=1 应启用低资源档")
	}
	for _, off := range []string{"0", "false", "off", "no"} {
		t.Setenv("XHS_LOW_RESOURCE", off)
		if LowResourceProfile() {
			t.Fatalf("XHS_LOW_RESOURCE=%s 应关闭低资源档", off)
		}
	}
}

// TestBrowserJSHeapMB 堆上限：显式值优先，非法值回落，低资源档默认 192。
func TestBrowserJSHeapMB(t *testing.T) {
	t.Setenv("XHS_BROWSER_JS_HEAP_MB", "128")
	if got := BrowserJSHeapMB(); got != 128 {
		t.Fatalf("显式堆上限应为 128，got %d", got)
	}

	t.Setenv("XHS_BROWSER_JS_HEAP_MB", "0")
	t.Setenv("XHS_LOW_RESOURCE", "1")
	if got := BrowserJSHeapMB(); got != 192 {
		t.Fatalf("低资源档默认堆上限应为 192，got %d", got)
	}

	t.Setenv("XHS_BROWSER_JS_HEAP_MB", "not-a-number")
	if got := BrowserJSHeapMB(); got != 192 {
		t.Fatalf("非法值应回落 192，got %d", got)
	}

	t.Setenv("XHS_LOW_RESOURCE", "0")
	if got := BrowserJSHeapMB(); got != 256 {
		t.Fatalf("非低资源档默认堆上限应为 256，got %d", got)
	}
}

// TestBrowserRendererLimit renderer 进程上限默认 2，可显式覆盖。
func TestBrowserRendererLimit(t *testing.T) {
	t.Setenv("XHS_BROWSER_RENDERER_LIMIT", "")
	if got := BrowserRendererLimit(); got != 2 {
		t.Fatalf("默认 renderer 上限应为 2，got %d", got)
	}
	t.Setenv("XHS_BROWSER_RENDERER_LIMIT", "1")
	if got := BrowserRendererLimit(); got != 1 {
		t.Fatalf("显式 renderer 上限应为 1，got %d", got)
	}
	t.Setenv("XHS_BROWSER_RENDERER_LIMIT", "-3")
	if got := BrowserRendererLimit(); got != 2 {
		t.Fatalf("非法值应回落 2，got %d", got)
	}
}

// TestBrowserBlockedURLPatterns 显式列表优先，"-" 表示不拦截，低资源档给默认媒体列表。
func TestBrowserBlockedURLPatterns(t *testing.T) {
	t.Setenv("XHS_BROWSER_BLOCK_URLS", "*.mp4*, *.m3u8*")
	got := BrowserBlockedURLPatterns()
	if len(got) != 2 || got[0] != "*.mp4*" || got[1] != "*.m3u8*" {
		t.Fatalf("显式模式解析错误: %v", got)
	}

	t.Setenv("XHS_BROWSER_BLOCK_URLS", "-")
	t.Setenv("XHS_LOW_RESOURCE", "1")
	if got := BrowserBlockedURLPatterns(); got != nil {
		t.Fatalf("\"-\" 应表示不拦截，got %v", got)
	}

	t.Setenv("XHS_BROWSER_BLOCK_URLS", "")
	t.Setenv("XHS_LOW_RESOURCE", "1")
	if got := BrowserBlockedURLPatterns(); len(got) == 0 {
		t.Fatal("低资源档空值应按未设置处理，返回默认媒体拦截列表")
	}

	t.Setenv("XHS_LOW_RESOURCE", "0")
	if got := BrowserBlockedURLPatterns(); got != nil {
		t.Fatalf("非低资源档不应默认拦截，got %v", got)
	}
}

// TestIdentityCheckInterval 采集间隔：默认 10m，"0" 表示每次都采集，非法值回落。
func TestIdentityCheckInterval(t *testing.T) {
	t.Setenv("XHS_IDENTITY_CHECK_INTERVAL", "")
	if got := IdentityCheckInterval(); got != 10*time.Minute {
		t.Fatalf("默认间隔应为 10m，got %s", got)
	}
	t.Setenv("XHS_IDENTITY_CHECK_INTERVAL", "0")
	if got := IdentityCheckInterval(); got != 0 {
		t.Fatalf("0 应表示每次都采集，got %s", got)
	}
	t.Setenv("XHS_IDENTITY_CHECK_INTERVAL", "90s")
	if got := IdentityCheckInterval(); got != 90*time.Second {
		t.Fatalf("显式间隔解析错误，got %s", got)
	}
	t.Setenv("XHS_IDENTITY_CHECK_INTERVAL", "bogus")
	if got := IdentityCheckInterval(); got != 10*time.Minute {
		t.Fatalf("非法间隔应回落 10m，got %s", got)
	}
	t.Setenv("XHS_IDENTITY_CHECK_INTERVAL", "-5m")
	if got := IdentityCheckInterval(); got != 10*time.Minute {
		t.Fatalf("负间隔应回落 10m，got %s", got)
	}
}
