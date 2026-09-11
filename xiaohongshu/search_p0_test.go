package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

func TestSearchFallbackDoesNotSwallowFatal(t *testing.T) {
	navigations := 0
	err := waitForSearchResultsWithURLFallback("keyword", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			return fmt.Errorf("probe: %w", ErrFatalRendererError)
		},
		pageErr: func() error { return nil },
		navigate: func(string) error {
			navigations++
			return nil
		},
	})
	if !IsFatalRendererError(err) || navigations != 0 {
		t.Fatalf("fatal 不得进入 URL fallback: navigations=%d err=%v", navigations, err)
	}
}

func TestSearchFallbackSkipsNavigateWhenAlreadyOnSearchPage(t *testing.T) {
	waits := 0
	err := waitForSearchResultsWithURLFallback("三亚旅游", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			waits++
			return fmt.Errorf("probe 超时")
		},
		pageErr: func() error { return nil },
		navigate: func(string) error {
			return errAlreadyOnSearchPage
		},
	})
	if err == nil {
		t.Fatal("已在搜索页不重复导航时应返回原等待错误")
	}
	if !strings.Contains(err.Error(), "已在搜索页不重复导航") {
		t.Fatalf("错误应说明已在搜索页: %v", err)
	}
	if waits != 1 {
		t.Fatalf("已在搜索页时应只等待一次: %d", waits)
	}
}

func TestSearchFallbackNavigatesWhenNotOnSearchPage(t *testing.T) {
	navigations := 0
	waits := 0
	err := waitForSearchResultsWithURLFallback("三亚旅游", searchResultsBaseline{}, searchResultsFallbackHooks{
		wait: func(searchResultsBaseline) error {
			waits++
			if waits == 1 {
				return fmt.Errorf("probe 超时")
			}
			return nil
		},
		pageErr: func() error { return nil },
		navigate: func(string) error {
			navigations++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("兜底导航后等待成功不应报错: %v", err)
	}
	if navigations != 1 || waits != 2 {
		t.Fatalf("未在搜索页时应导航一次并等待两次: navigations=%d waits=%d", navigations, waits)
	}
}

func TestSelectorSearchInputCoversTextarea(t *testing.T) {
	if !strings.Contains(SelectorSearchInput, "#search-input-ai") {
		t.Fatalf("SelectorSearchInput must include #search-input-ai, got: %s", SelectorSearchInput)
	}
	if strings.Contains(SelectorSearchInput, "textarea.search-input-ai") {
		t.Fatalf("SelectorSearchInput must not include wrong class selector textarea.search-input-ai, got: %s", SelectorSearchInput)
	}
}

func TestIsSearchResultPage(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{name: "search_result", url: "https://www.xiaohongshu.com/search_result?keyword=abc", want: true},
		{name: "search_result_ai", url: "https://www.xiaohongshu.com/search_result_ai?keyword=abc", want: true},
		{name: "无 fragment", url: "https://www.xiaohongshu.com/search_result#anchor", want: false},
		{name: "explore", url: "https://www.xiaohongshu.com/explore", want: false},
		{name: "explore detail", url: "https://www.xiaohongshu.com/explore/abc123", want: false},
		{name: "伪域名", url: "https://evil.com/search_result?keyword=abc", want: false},
		{name: "相似路径", url: "https://www.xiaohongshu.com/search_results_extra", want: false},
		{name: "非 https", url: "http://www.xiaohongshu.com/search_result?keyword=abc", want: false},
		{name: "非法 URL", url: "not-a-url", want: false},
	}
	for _, tc := range cases {
		if got := isSearchResultPage(tc.url); got != tc.want {
			t.Errorf("%s: isSearchResultPage(%q) = %v, want %v", tc.name, tc.url, got, tc.want)
		}
	}
}

func TestFilterAppliedRequiresActiveOptionOnSearchRoute(t *testing.T) {
	pf := pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}
	tests := []struct {
		name  string
		probe filterAppliedProbe
		want  bool
	}{
		{
			name:  "面板关闭时目标组不存在",
			probe: filterAppliedProbe{Route: "https://www.xiaohongshu.com/search_result", GroupFound: false, ActiveText: "最多评论"},
			want:  false,
		},
		{
			name:  "目标未 active",
			probe: filterAppliedProbe{Route: "https://www.xiaohongshu.com/search_result", GroupFound: true, ActiveText: "综合"},
			want:  false,
		},
		{
			name:  "目标 active 且处于搜索路由",
			probe: filterAppliedProbe{Route: "https://www.xiaohongshu.com/search_result_ai", GroupFound: true, ActiveText: "最多评论"},
			want:  true,
		},
		{
			name:  "目标 active 但处于 explore 路由",
			probe: filterAppliedProbe{Route: "https://www.xiaohongshu.com/explore/abc123", GroupFound: true, ActiveText: "最多评论"},
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := filterApplied(tc.probe, pf); got != tc.want {
				t.Fatalf("filterApplied(%+v, %+v) = %v, want %v", tc.probe, pf, got, tc.want)
			}
		})
	}
}

func TestWaitFilterAppliedTimeoutWrapsApplyFailure(t *testing.T) {
	client := &currentPageURLCDPClient{response: runtimeEvaluateStringResponse(t, `{"route":"https://www.xiaohongshu.com/search_result","group_found":true,"active_text":"综合"}`)}
	session := newCurrentPageURLSession(t, client)
	page := session.page.Context(context.Background())

	err := waitFilterApplied(context.Background(), page, nil, pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}, 50*time.Millisecond)
	if !errors.Is(err, errFilterApplyFailed) {
		t.Fatalf("未 active 超时应包装为 errFilterApplyFailed: %v", err)
	}
	if client.method != "Runtime.evaluate" {
		t.Fatalf("应先读取未 active probe: method=%q", client.method)
	}
}

func TestWaitFilterAppliedPreservesFatalRendererError(t *testing.T) {
	fatalProbeErr := fmt.Errorf("probe: %w", ErrFatalRendererError)
	if !IsFatalRendererError(fatalProbeErr) {
		t.Fatal("测试前提错误：fatal probe 错误应包含 ErrFatalRendererError")
	}
	client := &currentPageURLCDPClient{
		response: runtimeEvaluateStringResponse(t, `{"route":"https://www.xiaohongshu.com/search_result","group_found":true,"active_text":"综合"}`),
	}
	session := newCurrentPageURLSession(t, client)
	client.err = fatalProbeErr
	client.forceErr = true
	page := session.page.Context(context.Background())

	err := waitFilterApplied(context.Background(), page, nil, pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}, 50*time.Millisecond)
	if !errors.Is(err, errFilterApplyFailed) {
		t.Fatalf("fatal probe 错误应包装为 errFilterApplyFailed: %v", err)
	}
	if !IsFatalRendererError(err) {
		t.Fatalf("fatal probe 错误链不应被吞掉: %v", err)
	}
	if client.method != "Runtime.evaluate" {
		t.Fatalf("应由 fatal probe 读取路径触发: method=%q", client.method)
	}
}

func TestWaitFilterAppliedPrioritizesMisdirectedNavigation(t *testing.T) {
	client := &currentPageURLCDPClient{response: runtimeEvaluateStringResponse(t, `{"route":"https://www.xiaohongshu.com/explore/abc123","group_found":true,"active_text":"最多评论"}`)}
	session := newCurrentPageURLSession(t, client)
	page := session.page.Context(context.Background())

	err := waitFilterApplied(context.Background(), page, nil, pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}, 50*time.Millisecond)
	if !errors.Is(err, errFilterMisdirectedNavigation) {
		t.Fatalf("非搜索路由应优先返回 errFilterMisdirectedNavigation: %v", err)
	}
	if errors.Is(err, errFilterApplyFailed) {
		t.Fatalf("非搜索路由不应降级为 errFilterApplyFailed: %v", err)
	}
}

type filterApplyCDPClient struct {
	probeResponses     [][]byte
	probeIndex         int
	callFunctionCalls  int
	mouseMoves         int
	failCallFunction   bool
	failErr            error
	eventCh            chan *cdp.Event
	eventOnce          sync.Once
	closeOnce          sync.Once
}

func (c *filterApplyCDPClient) Event() <-chan *cdp.Event {
	c.eventOnce.Do(func() {
		c.eventCh = make(chan *cdp.Event)
	})
	return c.eventCh
}

func (c *filterApplyCDPClient) Close() {
	c.eventOnce.Do(func() {
		c.eventCh = make(chan *cdp.Event)
	})
	c.closeOnce.Do(func() {
		close(c.eventCh)
	})
}

func (c *filterApplyCDPClient) Call(ctx context.Context, _ string, method string, params interface{}) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Target.setDiscoverTargets", "Target.setAutoAttach":
		return []byte(`{}`), nil
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"session-1"}`), nil
	case "Target.getTargetInfo":
		return []byte(`{"targetInfo":{"targetId":"target-1","url":"https://www.xiaohongshu.com/search_result"}}`), nil
	case "Runtime.evaluate":
		var request struct {
			Expression string `json:"expression"`
		}
		if raw, marshalErr := json.Marshal(params); marshalErr == nil {
			_ = json.Unmarshal(raw, &request)
		}
		if request.Expression == "window" {
			return []byte(`{"result":{"type":"object","objectId":"filter-window"}}`), nil
		}
		index := c.probeIndex
		c.probeIndex++
		if index < len(c.probeResponses) {
			return c.probeResponses[index], nil
		}
		if len(c.probeResponses) > 0 {
			return c.probeResponses[len(c.probeResponses)-1], nil
		}
		return []byte(`{"result":{"type":"string","value":"{\"route\":\"https://www.xiaohongshu.com/search_result\",\"group_found\":false,\"active_text\":\"\"}"}}`), nil
	case "Runtime.callFunctionOn":
		c.callFunctionCalls++
		if c.failCallFunction {
			return nil, c.failErr
		}
		var request struct {
			FunctionDeclaration string `json:"functionDeclaration"`
		}
		if raw, marshalErr := json.Marshal(params); marshalErr == nil {
			_ = json.Unmarshal(raw, &request)
		}
		switch {
		case request.FunctionDeclaration == "() => window":
			return []byte(`{"result":{"type":"object","objectId":"filter-window"}}`), nil
		case strings.Contains(request.FunctionDeclaration, "containsElement") && !strings.Contains(request.FunctionDeclaration, "functions =>"):
			return []byte(`{"result":{"type":"boolean","value":true}}`), nil
		case strings.Contains(request.FunctionDeclaration, "getComputedStyle(this).pointerEvents"):
			return []byte(`{"result":{"type":"boolean","value":false}}`), nil
		case strings.Contains(request.FunctionDeclaration, "window.scrollX"):
			return []byte(`{"result":{"type":"object","value":{"x":0,"y":0}}}`), nil
		case strings.Contains(request.FunctionDeclaration, "function (f") && strings.Contains(request.FunctionDeclaration, "element"):
			return []byte(`{"result":{"type":"object","subtype":"node","objectId":"filter-button"}}`), nil
		default:
			return []byte(`{"result":{"type":"object","objectId":"filter-helper"}}`), nil
		}
	case "DOM.getContentQuads":
		return []byte(`{"quads":[[0,0,10,0,10,10,0,10]]}`), nil
	case "DOM.getNodeForLocation":
		return []byte(`{"backendNodeId":1}`), nil
	case "DOM.resolveNode":
		return []byte(`{"object":{"type":"object","subtype":"node","objectId":"filter-hit"}}`), nil
	case "DOM.describeNode":
		return []byte(`{"node":{"nodeName":"DIV"}}`), nil
	case "Input.dispatchMouseEvent":
		c.mouseMoves++
		return []byte(`{}`), nil
	case "Runtime.releaseObject":
		return []byte(`{}`), nil
	}
	return []byte(`{}`), nil
}

func newFilterApplyPage(t *testing.T, client *filterApplyCDPClient) *hrod.Page {
	t.Helper()
	browserCtx, cancelBrowser := context.WithCancel(context.Background())
	browser := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(client)
	if err := browser.Connect(); err != nil {
		t.Fatalf("初始化筛选 fake browser: %v", err)
	}
	browserEvents := browser.Event()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-browserEvents:
		case <-time.After(time.Second):
			t.Errorf("筛选 fake browser 事件 goroutine 未退出")
		}
		cancelBrowser()
	})
	page, err := browser.PageFromTarget("target-1")
	if err != nil {
		t.Fatalf("初始化筛选 fake page: %v", err)
	}
	wrapped := &hrod.Page{Rod: page}
	actor := humanize.New(page, humanize.Config{})
	actorField := reflect.ValueOf(wrapped).Elem().FieldByName("actor")
	reflect.NewAt(actorField.Type(), unsafe.Pointer(actorField.UnsafeAddr())).Elem().Set(reflect.ValueOf(actor))
	return wrapped
}

func filterProbeResponse(t *testing.T, groupFound bool, activeText string) []byte {
	t.Helper()
	return runtimeEvaluateStringResponse(t, fmt.Sprintf(`{"route":"https://www.xiaohongshu.com/search_result","group_found":%t,"active_text":%q}`, groupFound, activeText))
}

func TestWaitFilterAppliedReopensPanelOnce(t *testing.T) {
	client := &filterApplyCDPClient{probeResponses: [][]byte{
		filterProbeResponse(t, false, ""),
		filterProbeResponse(t, false, ""),
		filterProbeResponse(t, true, "最多评论"),
	}}
	page := newFilterApplyPage(t, client).Context(context.Background())

	err := waitFilterApplied(context.Background(), page, nil, pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}, 1200*time.Millisecond)
	if err != nil {
		t.Fatalf("一次 hover 重开后目标 active 应成功: %v", err)
	}
	if client.mouseMoves != 1 {
		t.Fatalf("筛选面板只应恢复 hover 一次: moves=%d", client.mouseMoves)
	}
}

func TestWaitFilterAppliedAfterReopenStillWrongOptionFails(t *testing.T) {
	client := &filterApplyCDPClient{probeResponses: [][]byte{
		filterProbeResponse(t, false, ""),
		filterProbeResponse(t, true, "综合"),
	}}
	page := newFilterApplyPage(t, client).Context(context.Background())

	err := waitFilterApplied(context.Background(), page, nil, pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}, 700*time.Millisecond)
	if !errors.Is(err, errFilterApplyFailed) {
		t.Fatalf("重开后仍为综合应返回 errFilterApplyFailed: %v", err)
	}
	if client.mouseMoves != 1 {
		t.Fatalf("重开动作只能执行一次: moves=%d", client.mouseMoves)
	}
}

func TestWaitFilterAppliedReopenFailureWrapsApplyError(t *testing.T) {
	reopenErr := errors.New("reopen filter failed")
	client := &filterApplyCDPClient{
		probeResponses:   [][]byte{filterProbeResponse(t, false, "")},
		failCallFunction: true,
		failErr:          reopenErr,
	}
	page := newFilterApplyPage(t, client).Context(context.Background())

	err := waitFilterApplied(context.Background(), page, nil, pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}, time.Second)
	if !errors.Is(err, errFilterApplyFailed) || !errors.Is(err, reopenErr) {
		t.Fatalf("重开失败应同时保留 apply 和底层错误: %v", err)
	}
}

func TestWaitFilterAppliedReopenFailurePreservesFatalRendererError(t *testing.T) {
	fatalReopenErr := fmt.Errorf("reopen renderer: %w", ErrFatalRendererError)
	client := &filterApplyCDPClient{
		probeResponses:   [][]byte{filterProbeResponse(t, false, "")},
		failCallFunction: true,
		failErr:          fatalReopenErr,
	}
	page := newFilterApplyPage(t, client).Context(context.Background())

	err := waitFilterApplied(context.Background(), page, nil, pendingFilter{GroupLabel: "排序依据", OptionText: "最多评论"}, time.Second)
	if !errors.Is(err, errFilterApplyFailed) || !errors.Is(err, ErrFatalRendererError) || !IsFatalRendererError(err) {
		t.Fatalf("重开 fatal 失败应同时保留两个错误链: %v", err)
	}
}
