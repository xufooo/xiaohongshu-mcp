package xiaohongshu

import (
	"context"
	"errors"
	"testing"
)

func TestDebugSearchRecorderStageAccumulation(t *testing.T) {
	r := newDebugSearchRecorder()
	r.beginStage("precheck")
	r.beginStage("input")
	r.beginStage("input")
	r.beginStage("wait")
	r.beginStage("wait")
	r.beginStage("extract")
	r.finish()
	if r.TotalMS < 0 || r.InputMS < 0 || r.WaitMS < 0 || r.ExtractMS < 0 {
		t.Fatalf("阶段耗时不应为负: %+v", r)
	}
}

func TestDebugSearchRecorderProbeCounts(t *testing.T) {
	r := newDebugSearchRecorder()
	r.recordInputProbe(10, errors.New("eval timeout"))
	r.recordInputProbe(20, nil)
	r.recordResultProbe(30, context.DeadlineExceeded)
	r.recordResultProbe(40, nil)
	if r.InputProbeCount != 2 || r.InputProbeFailed != 1 {
		t.Fatalf("input probe 统计错误: count=%d failed=%d", r.InputProbeCount, r.InputProbeFailed)
	}
	if r.ResultProbeCount != 2 || r.ResultProbeFailed != 1 {
		t.Fatalf("result probe 统计错误: count=%d failed=%d", r.ResultProbeCount, r.ResultProbeFailed)
	}
	if r.InputLastError != debugSearchErrNone {
		t.Fatalf("最后一次成功 probe 后 input last error 应为 none: %s", r.InputLastError)
	}
	if r.ResultLastError != debugSearchErrNone {
		t.Fatalf("最后一次成功 probe 后 result last error 应为 none: %s", r.ResultLastError)
	}
}

func TestDebugSearchErrorClassification(t *testing.T) {
	if got := classifyDebugSearchError(context.DeadlineExceeded); got != debugSearchErrContextDeadline {
		t.Fatalf("context.DeadlineExceeded 应分类为 context_deadline: %s", got)
	}
	if got := classifyDebugSearchError(context.Canceled); got != debugSearchErrContextCanceled {
		t.Fatalf("context.Canceled 应分类为 context_canceled: %s", got)
	}
	if got := classifyDebugSearchError(errors.New("eval timeout")); got != debugSearchErrEvalTimeout {
		t.Fatalf("eval timeout 错误应分类为 eval_timeout: %s", got)
	}
	if got := classifyDebugSearchError(nil); got != debugSearchErrNone {
		t.Fatalf("nil 应分类为 none: %s", got)
	}
}

func TestDebugSearchRecorderSampleLimit(t *testing.T) {
	r := newDebugSearchRecorder()
	for i := 0; i < debugSearchProbeSampleLimit+10; i++ {
		r.recordResultProbe(int64(i), nil)
	}
	if len(r.ResultProbeMs) != debugSearchProbeSampleLimit {
		t.Fatalf("样本数组应截断到上限: got %d want %d", len(r.ResultProbeMs), debugSearchProbeSampleLimit)
	}
	if r.ResultProbeCount != debugSearchProbeSampleLimit+10 {
		t.Fatalf("计数应继续增长: got %d want %d", r.ResultProbeCount, debugSearchProbeSampleLimit+10)
	}
}

func TestDebugSearchRecorderFinishClosesStage(t *testing.T) {
	r := newDebugSearchRecorder()
	r.beginStage("wait")
	r.finish()
	if r.currentStage != "" {
		t.Fatalf("finish 后不应有未结束阶段: %s", r.currentStage)
	}
	if r.WaitMS < 0 {
		t.Fatalf("wait 耗时不应为负: %d", r.WaitMS)
	}
}

func TestDebugSearchContextRoundTrip(t *testing.T) {
	r := newDebugSearchRecorder()
	ctx := withDebugSearchRecorder(context.Background(), r)
	got := debugSearchFromContext(ctx)
	if got != r {
		t.Fatal("context 往返应返回同一 recorder")
	}
	if debugSearchFromContext(context.Background()) != nil {
		t.Fatal("无 recorder 的 context 应返回 nil")
	}
}
