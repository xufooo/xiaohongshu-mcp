package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

func newRuntimeEvaluate(ctx context.Context, expression string) proto.RuntimeEvaluate {
	request := proto.RuntimeEvaluate{
		Expression:    expression,
		ReturnByValue: true,
		AwaitPromise:  true,
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			request.Timeout = proto.RuntimeTimeDelta(float64(remaining) / float64(time.Millisecond))
		}
	}
	return request
}

// defaultEvalTimeout 是页内一次求值的默认上限（调用方自己给了 deadline 就用调用方的）。
//
// 为什么必须有：`Runtime.evaluate` 在渲染进程卡住时会一直不返回——实测出现过**单次就绪探测阻塞 297s**，
// 把整次调用的 5 分钟预算吃光，错误信息还只剩"context deadline exceeded"。
// 这是**传输层的界**，不是"预计页面什么时候好"的预测：正常探测 x86 实测 p90≈274ms、重载 max≈2s，
// Pi 上慢一个量级也远低于它；真到 15s 说明渲染进程已经不正常，应当如实报错而不是无限等。
const defaultEvalTimeout = 15 * time.Second

// withEvalDeadline 保证**每一次页内求值都有界且不超过默认上限**：
// 调用方给了更紧的 deadline 就尊重它，否则（没给、或给得比默认更松）套上默认上限。
// 取 min 而不是"只在没给时兜底"，是因为调用方常带着整次请求的 5 分钟 deadline，
// 只在"没给"时兜底会被它绕过——那样一次卡住的求值仍能耗光几分钟。
func withEvalDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= defaultEvalTimeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultEvalTimeout)
}

func evalJSDirect(ctx context.Context, page *hrod.Page, fn string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	ctx, cancel := withEvalDeadline(ctx)
	defer cancel()

	encoded := make([]string, len(args))
	for i, arg := range args {
		value, err := json.Marshal(arg)
		if err != nil {
			return nil, err
		}
		encoded[i] = string(value)
	}
	expression := fmt.Sprintf("(%s)(%s)", fn, strings.Join(encoded, ", "))
	result, err := newRuntimeEvaluate(ctx, expression).Call(page.Rod.Context(ctx))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("Runtime.evaluate returned nil")
	}
	if result.ExceptionDetails != nil {
		return nil, &rod.EvalError{RuntimeExceptionDetails: result.ExceptionDetails}
	}
	if result.Result == nil {
		return nil, fmt.Errorf("Runtime.evaluate result is nil")
	}
	return result.Result, nil
}
