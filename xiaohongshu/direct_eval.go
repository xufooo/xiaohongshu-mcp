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

func evalJSDirect(ctx context.Context, page *hrod.Page, fn string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
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
		return nil, &rod.EvalError{result.ExceptionDetails}
	}
	if result.Result == nil {
		return nil, fmt.Errorf("Runtime.evaluate result is nil")
	}
	return result.Result, nil
}
