package hrod

import (
	"context"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/humanize"
)

// newTestElement 构造一个不连接浏览器的测试元素：裸 *rod.Element + 独立 actor。
func newTestElement(t *testing.T) (*Element, *humanize.Actor) {
	t.Helper()
	actor := humanize.New(&rod.Page{}, humanize.Config{})
	return NewElement(&rod.Element{}, actor), actor
}

// TestElementContextIsolation 验证 clone 只保存自己的 ctx：
// 短超时 clone 到期或 longCtx 取消都不影响 sibling/base clone 的 Sleep。
func TestElementContextIsolation(t *testing.T) {
	el, _ := newTestElement(t)

	longCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	longEl := el.Context(longCtx)

	// 短超时 clone 从 longCtx 派生
	shortEl := longEl.Timeout(30 * time.Millisecond)
	start := time.Now()
	if err := shortEl.Sleep(2 * time.Second); err == nil {
		t.Fatal("短超时 clone Sleep 应返回 deadline exceeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("短 clone Sleep 应在 ~30ms 后返回, 实际 %s", elapsed)
	}

	// 短 clone 到期后，long clone 仍使用 longCtx（未取消），不返回 deadline exceeded
	if err := longEl.Sleep(5 * time.Millisecond); err != nil {
		t.Fatalf("long clone Sleep 不应受短 clone 影响: %v", err)
	}
	// base clone 沿用 background ctx，也不受影响
	if err := el.Sleep(5 * time.Millisecond); err != nil {
		t.Fatalf("base clone Sleep 不应受 clone 影响: %v", err)
	}

	// 取消 longCtx 后，long clone 及其派生超时 clone 立即感知
	cancel()
	if err := longEl.Sleep(time.Second); err != context.Canceled {
		t.Fatalf("long clone 应返回 Canceled, 实际 %v", err)
	}
	if err := shortEl.Sleep(time.Second); err != context.Canceled {
		t.Fatalf("派生超时 clone 应随 longCtx 取消返回 Canceled, 实际 %v", err)
	}
	// base clone 不受 longCtx 取消影响
	if err := el.Sleep(5 * time.Millisecond); err != nil {
		t.Fatalf("base clone 不应受 longCtx 取消影响: %v", err)
	}
}

// TestElementSharesActor 验证 clone 共享同一 actor，且 Actor() 返回构造时的 actor。
func TestElementSharesActor(t *testing.T) {
	el, actor := newTestElement(t)
	longCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	longEl := el.Context(longCtx)
	if el.Actor() != longEl.Actor() {
		t.Fatal("clone 应共享同一 actor")
	}
	if el.Actor() != actor {
		t.Fatal("Actor() 应返回构造时的 actor")
	}
}
