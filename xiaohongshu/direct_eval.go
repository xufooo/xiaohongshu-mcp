package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

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
	result, err := (proto.RuntimeEvaluate{
		Expression:   expression,
		ReturnByValue: true,
		AwaitPromise:  true,
	}).Call(page.Rod.Context(ctx))
	if err != nil {
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
