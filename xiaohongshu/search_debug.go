package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const debugSearchProbeSampleLimit = 320

type debugSearchErrorKind string

const (
	debugSearchErrNone            debugSearchErrorKind = "none"
	debugSearchErrEvalTimeout     debugSearchErrorKind = "eval_timeout"
	debugSearchErrFatalRenderer   debugSearchErrorKind = "fatal_renderer"
	debugSearchErrContextDeadline debugSearchErrorKind = "context_deadline"
	debugSearchErrContextCanceled debugSearchErrorKind = "context_canceled"
	debugSearchErrRendererClosed  debugSearchErrorKind = "renderer_closed"
	debugSearchErrOther           debugSearchErrorKind = "other"
)

func classifyDebugSearchError(err error) debugSearchErrorKind {
	if err == nil {
		return debugSearchErrNone
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return debugSearchErrContextDeadline
	}
	if errors.Is(err, context.Canceled) {
		return debugSearchErrContextCanceled
	}
	if IsFatalRendererError(err) {
		return debugSearchErrFatalRenderer
	}
	if isEvalTimeout(err) {
		return debugSearchErrEvalTimeout
	}
	if isConfirmedRendererDead(err) {
		return debugSearchErrRendererClosed
	}
	return debugSearchErrOther
}

type debugSearchRecorder struct {
	startedAt        time.Time
	TotalMS          int64
	PrecheckMS       int64
	InputMS          int64
	SubmitMS         int64
	WaitMS           int64
	ExtractMS        int64
	InputProbeMs     []int64
	InputProbeCount  int
	InputProbeFailed int
	InputLastError   debugSearchErrorKind
	ResultProbeMs    []int64
	ResultProbeCount int
	ResultProbeFailed int
	ResultLastError  debugSearchErrorKind
	WaitExit         string
	Fallback         bool
	WaitRounds       int
	currentStage     string
	stageStartedAt   time.Time
}

func newDebugSearchRecorder() *debugSearchRecorder {
	return &debugSearchRecorder{
		startedAt:     time.Now(),
		InputProbeMs:  make([]int64, 0, 16),
		ResultProbeMs: make([]int64, 0, 16),
	}
}

func (r *debugSearchRecorder) beginStage(stage string) {
	if r.currentStage == stage {
		return
	}
	if r.currentStage != "" {
		r.endStage()
	}
	r.currentStage = stage
	r.stageStartedAt = time.Now()
}

func (r *debugSearchRecorder) endStage() {
	if r.currentStage == "" {
		return
	}
	ms := time.Since(r.stageStartedAt).Milliseconds()
	switch r.currentStage {
	case "precheck":
		r.PrecheckMS += ms
	case "input":
		r.InputMS += ms
	case "submit":
		r.SubmitMS += ms
	case "wait":
		r.WaitMS += ms
	case "extract":
		r.ExtractMS += ms
	}
	r.currentStage = ""
}

func (r *debugSearchRecorder) finish() {
	r.endStage()
	r.TotalMS = time.Since(r.startedAt).Milliseconds()
}

func (r *debugSearchRecorder) recordInputProbe(ms int64, err error) {
	r.InputProbeCount++
	if err != nil {
		r.InputProbeFailed++
		r.InputLastError = classifyDebugSearchError(err)
	} else {
		r.InputLastError = debugSearchErrNone
	}
	if len(r.InputProbeMs) < debugSearchProbeSampleLimit {
		r.InputProbeMs = append(r.InputProbeMs, ms)
	}
}

func (r *debugSearchRecorder) recordResultProbe(ms int64, err error) {
	r.ResultProbeCount++
	if err != nil {
		r.ResultProbeFailed++
		r.ResultLastError = classifyDebugSearchError(err)
	} else {
		r.ResultLastError = debugSearchErrNone
	}
	if len(r.ResultProbeMs) < debugSearchProbeSampleLimit {
		r.ResultProbeMs = append(r.ResultProbeMs, ms)
	}
}

func (r *debugSearchRecorder) setWaitExit(exit string) {
	r.WaitExit = exit
}

func (r *debugSearchRecorder) marshalSummary() string {
	type summary struct {
		TotalMS           int64                `json:"total_ms"`
		PrecheckMS        int64                `json:"precheck_ms,omitempty"`
		InputMS           int64                `json:"input_ms,omitempty"`
		SubmitMS          int64                `json:"submit_ms,omitempty"`
		WaitMS            int64                `json:"wait_ms,omitempty"`
		ExtractMS         int64                `json:"extract_ms,omitempty"`
		InputProbeMs      []int64              `json:"input_probe_ms,omitempty"`
		InputProbeCount   int                  `json:"input_probe_count"`
		InputProbeFailed  int                  `json:"input_probe_failed"`
		InputLastError    debugSearchErrorKind `json:"input_last_error_kind,omitempty"`
		ResultProbeMs     []int64              `json:"result_probe_ms,omitempty"`
		ResultProbeCount  int                  `json:"result_probe_count"`
		ResultProbeFailed int                  `json:"result_probe_failed"`
		ResultLastError   debugSearchErrorKind `json:"result_last_error_kind,omitempty"`
		WaitExit          string               `json:"wait_exit,omitempty"`
		Fallback          bool                 `json:"fallback"`
		WaitRounds        int                  `json:"wait_rounds"`
	}
	b, err := json.Marshal(summary{
		TotalMS:           r.TotalMS,
		PrecheckMS:        r.PrecheckMS,
		InputMS:           r.InputMS,
		SubmitMS:          r.SubmitMS,
		WaitMS:            r.WaitMS,
		ExtractMS:         r.ExtractMS,
		InputProbeMs:      r.InputProbeMs,
		InputProbeCount:   r.InputProbeCount,
		InputProbeFailed:  r.InputProbeFailed,
		InputLastError:    r.InputLastError,
		ResultProbeMs:     r.ResultProbeMs,
		ResultProbeCount:  r.ResultProbeCount,
		ResultProbeFailed: r.ResultProbeFailed,
		ResultLastError:   r.ResultLastError,
		WaitExit:          r.WaitExit,
		Fallback:          r.Fallback,
		WaitRounds:        r.WaitRounds,
	})
	if err != nil {
		return fmt.Sprintf("debug_search marshal error: %v", err)
	}
	return string(b)
}

func (r *debugSearchRecorder) fillResult(result *SearchPageResult) {
	result.DebugSearchTotalMS = r.TotalMS
	result.DebugSearchPrecheckMS = r.PrecheckMS
	result.DebugSearchInputMS = r.InputMS
	result.DebugSearchSubmitMS = r.SubmitMS
	result.DebugSearchWaitMS = r.WaitMS
	result.DebugSearchExtractMS = r.ExtractMS
	result.DebugSearchInputProbeMs = r.InputProbeMs
	result.DebugSearchInputProbeCount = r.InputProbeCount
	result.DebugSearchInputProbeFailed = r.InputProbeFailed
	result.DebugSearchInputLastErrorKind = string(r.InputLastError)
	result.DebugSearchResultProbeMs = r.ResultProbeMs
	result.DebugSearchResultProbeCount = r.ResultProbeCount
	result.DebugSearchResultProbeFailed = r.ResultProbeFailed
	result.DebugSearchResultLastErrorKind = string(r.ResultLastError)
	result.DebugSearchWaitExit = r.WaitExit
	result.DebugSearchFallback = r.Fallback
	result.DebugSearchWaitRounds = r.WaitRounds
}

type debugSearchContextKey struct{}

func withDebugSearchRecorder(ctx context.Context, r *debugSearchRecorder) context.Context {
	return context.WithValue(ctx, debugSearchContextKey{}, r)
}

func debugSearchFromContext(ctx context.Context) *debugSearchRecorder {
	r, _ := ctx.Value(debugSearchContextKey{}).(*debugSearchRecorder)
	return r
}
