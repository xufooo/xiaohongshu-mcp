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
