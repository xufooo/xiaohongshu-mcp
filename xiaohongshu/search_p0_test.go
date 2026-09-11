package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

type searchP0CDPClient struct {
	eventCh      chan *cdp.Event
	eventOnce    sync.Once
	closeOnce    sync.Once
	panelOpen    bool
	panelProbes  int
	buttonClicks int
	hit          bool
	mouseEvents  []proto.InputDispatchMouseEventType
}

func (c *searchP0CDPClient) Event() <-chan *cdp.Event {
	c.eventOnce.Do(func() {
		c.eventCh = make(chan *cdp.Event)
	})
	return c.eventCh
}

func (c *searchP0CDPClient) Close() {
	c.eventOnce.Do(func() {
		c.eventCh = make(chan *cdp.Event)
	})
	c.closeOnce.Do(func() {
		close(c.eventCh)
	})
}

func (c *searchP0CDPClient) Call(ctx context.Context, _ string, method string, params interface{}) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	switch method {
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"session-1"}`), nil
	case "Runtime.evaluate":
		request, ok := params.(proto.RuntimeEvaluate)
		if !ok {
			raw, _ := json.Marshal(params)
			_ = json.Unmarshal(raw, &request)
		}
		switch {
		case strings.Contains(request.Expression, ".filter-panel"):
			c.panelProbes++
			if c.panelOpen {
				return []byte(`{"result":{"type":"boolean","value":true}}`), nil
			}
			return []byte(`{"result":{"type":"boolean","value":false}}`), nil
		case strings.TrimSpace(request.Expression) == "window":
			return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
		default:
			return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
		}
	case "Runtime.callFunctionOn":
		request, ok := params.(proto.RuntimeCallFunctionOn)
		if !ok {
			raw, _ := json.Marshal(params)
			_ = json.Unmarshal(raw, &request)
		}
		switch {
		case strings.TrimSpace(request.FunctionDeclaration) == "() => window":
			return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
		case strings.Contains(request.FunctionDeclaration, "JSON.stringify([window.innerWidth, window.innerHeight])"):
			return []byte(`{"result":{"type":"string","value":"[800,600]"}}`), nil
		case strings.Contains(request.FunctionDeclaration, "getComputedStyle(this).visibility"):
			return []byte(`{"result":{"type":"string","value":"visible"}}`), nil
		case strings.Contains(request.FunctionDeclaration, "elementFromPoint"):
			if c.hit {
				return []byte(`{"result":{"type":"boolean","value":true}}`), nil
			}
			return []byte(`{"result":{"type":"boolean","value":false}}`), nil
		default:
			return []byte(`{"result":{"type":"object","subtype":"node","objectId":"filter-button"}}`), nil
		}
	case "DOM.getContentQuads":
		return []byte(`{"quads":[[10,20,110,20,110,60,10,60]]}`), nil
	case "Input.dispatchMouseEvent":
		request, ok := params.(proto.InputDispatchMouseEvent)
		if !ok {
			raw, _ := json.Marshal(params)
			_ = json.Unmarshal(raw, &request)
		}
		c.mouseEvents = append(c.mouseEvents, request.Type)
		if request.Type == proto.InputDispatchMouseEventTypeMouseReleased {
			c.buttonClicks++
			c.panelOpen = true
		}
		return []byte(`{}`), nil
	}
	return []byte(`{}`), nil
}

func newSearchP0Element(t *testing.T, client *searchP0CDPClient) (*rod.Page, *rod.Element) {
	t.Helper()
	browserCtx, cancelBrowser := context.WithCancel(context.Background())
	browser := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(client)
	if err := browser.Connect(); err != nil {
		t.Fatalf("初始化 search fake rod browser: %v", err)
	}
	browserEvents := browser.Event()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-browserEvents:
		case <-time.After(time.Second):
			t.Errorf("search fake rod browser 事件 goroutine 未退出")
		}
		cancelBrowser()
	})
	page, err := browser.PageFromTarget("target-1")
	if err != nil {
		t.Fatalf("初始化 search fake rod page: %v", err)
	}
	element, err := page.Element("div.filter")
	if err != nil {
		t.Fatalf("初始化 search fake filter element: %v", err)
	}
	return page, element
}

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

func TestClickDirectDispatchesOneMouseClick(t *testing.T) {
	client := &searchP0CDPClient{hit: true}
	_, element := newSearchP0Element(t, client)

	if err := humanize.ClickDirect(element); err != nil {
		t.Fatalf("ClickDirect 不应失败: %v", err)
	}
	want := []proto.InputDispatchMouseEventType{
		proto.InputDispatchMouseEventTypeMouseMoved,
		proto.InputDispatchMouseEventTypeMousePressed,
		proto.InputDispatchMouseEventTypeMouseReleased,
	}
	if len(client.mouseEvents) != len(want) {
		t.Fatalf("鼠标事件数量错误: got=%v want=%v", client.mouseEvents, want)
	}
	for i := range want {
		if client.mouseEvents[i] != want[i] {
			t.Fatalf("鼠标事件[%d]错误: got=%v want=%v", i, client.mouseEvents[i], want[i])
		}
	}
}

func TestClickDirectStopsBeforePressWhenHitCheckFails(t *testing.T) {
	client := &searchP0CDPClient{hit: false}
	_, element := newSearchP0Element(t, client)

	if err := humanize.ClickDirect(element); err == nil {
		t.Fatal("按下前命中复核失败时应返回错误")
	}
	if len(client.mouseEvents) != 1 || client.mouseEvents[0] != proto.InputDispatchMouseEventTypeMouseMoved {
		t.Fatalf("命中复核失败后只应有一次 mouseMoved: %v", client.mouseEvents)
	}
	for _, event := range client.mouseEvents {
		if event == proto.InputDispatchMouseEventTypeMousePressed {
			t.Fatal("按下前命中复核失败时不得产生 mousePressed")
		}
		if event == proto.InputDispatchMouseEventTypeMouseReleased {
			t.Fatal("按下前命中复核失败时不得产生 mouseReleased")
		}
	}
}

func TestEnsureFilterPanelOpenRecoversClosedPanel(t *testing.T) {
	client := &searchP0CDPClient{hit: true}
	rawPage, rawButton := newSearchP0Element(t, client)
	button := hrod.NewElement(rawButton, humanize.New(rawPage, humanize.Config{}))
	page := &hrod.Page{Rod: rawPage}

	stage, err := ensureFilterPanelOpen(context.Background(), &evalTimeoutCounter{}, page, button)
	if err != nil {
		t.Fatalf("已筛选状态恢复面板不应失败: stage=%s err=%v", stage, err)
	}
	if stage != "" {
		t.Fatalf("成功路径不应返回失败 stage: %q", stage)
	}
	if client.panelProbes != 1 {
		t.Fatalf("首次恢复应只探针一次: %d", client.panelProbes)
	}
	if client.buttonClicks != 1 {
		t.Fatalf("面板不存在时应点击按钮一次: %d", client.buttonClicks)
	}

	stage, err = ensureFilterPanelOpen(context.Background(), &evalTimeoutCounter{}, page, button)
	if err != nil {
		t.Fatalf("面板已存在时不应失败: stage=%s err=%v", stage, err)
	}
	if client.panelProbes != 2 {
		t.Fatalf("每次调用应各做一次面板探针: %d", client.panelProbes)
	}
	if client.buttonClicks != 1 {
		t.Fatalf("面板已存在时不应再次点击按钮: %d", client.buttonClicks)
	}
}
