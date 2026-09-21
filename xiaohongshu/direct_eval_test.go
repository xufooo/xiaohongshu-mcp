package xiaohongshu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

func TestEvalJSDirectSendsRendererTimeout(t *testing.T) {
	client := &currentPageURLCDPClient{response: []byte(`{"result":{"type":"number","value":1}}`)}
	session := newCurrentPageURLSession(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := evalJSDirect(ctx, session.page, "() => 1"); err != nil {
		t.Fatalf("不期望错误: %v", err)
	}
	request, ok := client.params.(proto.RuntimeEvaluate)
	if !ok {
		t.Fatalf("Runtime.evaluate 参数类型 = %T", client.params)
	}
	if request.Timeout <= 0 || request.Timeout > proto.RuntimeTimeDelta(5000) {
		t.Fatalf("Runtime.evaluate timeout = %v, 期望在 (0,5000] 内", request.Timeout)
	}
}

func TestEvalJSDirectKeepsContextError(t *testing.T) {
	want := errors.New("protocol terminated")
	client := &currentPageURLCDPClient{}
	session := newCurrentPageURLSession(t, client)
	client.err = want
	client.forceErr = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	time.Sleep(2 * time.Millisecond)
	defer cancel()

	_, err := evalJSDirect(ctx, session.page, "() => 1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v, 期望 context deadline", err)
	}
}

func TestNewRuntimeEvaluateUsesContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	request := newRuntimeEvaluate(ctx, "() => 1")
	if request.Timeout <= 0 || request.Timeout > proto.RuntimeTimeDelta(5000) {
		t.Fatalf("Runtime.evaluate timeout = %v, 期望在 (0,5000] 内", request.Timeout)
	}
	if request.Expression != "() => 1" || !request.ReturnByValue || !request.AwaitPromise {
		t.Fatalf("Runtime.evaluate 基础参数错误: %#v", request)
	}
}

func TestNewRuntimeEvaluatePreservesNoDeadlineBehavior(t *testing.T) {
	request := newRuntimeEvaluate(context.Background(), "() => 1")
	if request.Timeout != 0 {
		t.Fatalf("无 deadline 时 Runtime.evaluate timeout = %v, 期望为 0", request.Timeout)
	}
}

func TestNewRuntimeEvaluateUsesRemainingDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	request := newRuntimeEvaluate(ctx, "() => 1")
	if request.Timeout <= 0 || request.Timeout > proto.RuntimeTimeDelta(250) {
		t.Fatalf("Runtime.evaluate timeout = %v, 期望在 (0,250] 内", request.Timeout)
	}
}

func TestNewRuntimeEvaluateOmitsExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	request := newRuntimeEvaluate(ctx, "() => 1")
	if request.Timeout != 0 {
		t.Fatalf("过期 deadline 时 Runtime.evaluate timeout = %v, 期望为 0", request.Timeout)
	}
}

// 页内求值必须有界：调用方没给 deadline 时要套默认上限，
// 否则渲染进程卡住会让一次探测无限挂住（实测出现过 297s）。
func TestEvalJSDirectIsAlwaysBounded(t *testing.T) {
	ctx, cancel := withEvalDeadline(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("没有 deadline 的调用必须被套上默认上限")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > defaultEvalTimeout {
		t.Fatalf("默认上限异常: %v", remaining)
	}

	// 调用方给了更紧的 deadline：原样尊重。
	parent, parentCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer parentCancel()
	kept, keptCancel := withEvalDeadline(parent)
	defer keptCancel()
	keptDeadline, _ := kept.Deadline()
	parentDeadline, _ := parent.Deadline()
	if !keptDeadline.Equal(parentDeadline) {
		t.Fatalf("更紧的 deadline 应被尊重: %v → %v", parentDeadline, keptDeadline)
	}

	// 调用方带着整次请求的宽松 deadline（如 5 分钟）：必须被收到默认上限，
	// 否则一次卡住的求值仍能耗光几分钟。
	loose, looseCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer looseCancel()
	tight, tightCancel := withEvalDeadline(loose)
	defer tightCancel()
	tightDeadline, _ := tight.Deadline()
	if remaining := time.Until(tightDeadline); remaining > defaultEvalTimeout {
		t.Fatalf("宽松 deadline 应被收到 %v 以内, 实际 %v", defaultEvalTimeout, remaining)
	}

	// 有 deadline 的请求必须把剩余时间写进 CDP 请求；没有 deadline 时不写（由上面的包装保证有）。
	request := newRuntimeEvaluate(ctx, "1")
	if request.Timeout == 0 {
		t.Fatal("有 deadline 时应设置 Runtime.evaluate 的 Timeout")
	}
}
