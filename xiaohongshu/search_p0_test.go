package xiaohongshu

import (
	"context"
	"encoding/json"
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

// TestRiskKeywordsSingleSource 风控关键词只有一份来源：
// Go 判定、页面内探针、写失败探针都从 riskKeywordGroups 生成。
func TestRiskKeywordsSingleSource(t *testing.T) {
	if got := RiskKindFromText("请拖动滑块完成验证"); got != RiskSliderChallenge {
		t.Fatalf("滑块文本应判为滑块风险, got %v", got)
	}
	if got := RiskKindFromText("操作频繁，请稍后再试"); got != RiskAccessAnomaly {
		t.Fatalf("频繁文本应判为访问异常, got %v", got)
	}
	list := riskKeywordsJSList()
	if !strings.HasPrefix(list, "[") || !strings.Contains(list, "验证码") {
		t.Fatalf("关键词 JSON 非法: %s", list)
	}
	// 生成到 JS 里的必须是数组字面量，不能带模板反引号（否则 .find 会炸）。
	probe := xhsProbeRiskJS()
	if !strings.Contains(probe, "const riskKeywords = "+list+";") {
		t.Fatalf("页面探针应内联同源关键词数组: %s", probe)
	}
	if strings.Contains(probe, "`"+list+"`") {
		t.Fatal("关键词不得被包成模板字符串")
	}
	comment := commentSubmissionStateJS()
	if !strings.Contains(comment, "const errorKeywords = "+writeFailureKeywords("评论")+";") {
		t.Fatalf("评论探针关键词异常: %s", comment)
	}
	reply := replySubmitStateJS()
	if !strings.Contains(reply, "const keywords = "+writeFailureKeywords("回复")+";") {
		t.Fatalf("回复探针关键词异常: %s", reply)
	}
	for _, action := range []string{"评论", "回复"} {
		js := writeFailureKeywords(action)
		for _, want := range []string{"操作频繁", action + "失败", "禁止" + action} {
			if !strings.Contains(js, want) {
				t.Fatalf("%s 关键词应含 %q: %s", action, want, js)
			}
		}
	}
}

// 我们自己能选择的可见性文案不能当风控词：
// 「仅自己可见」曾在 permission_denied 组里，导致发布私有笔记必然被判 permission_denied。
func TestRiskRulesExcludeVisibilityLabels(t *testing.T) {
	if got := RiskKindFromText("仅自己可见"); got != RiskNone {
		t.Fatalf("「仅自己可见」是正常可见性设置，不应判为风控, got %v", got)
	}
	if got := RiskKindFromText("发布设置：公开可见 / 仅自己可见 / 仅互关好友可见"); got != RiskNone {
		t.Fatalf("可见性选项文案不应判为风控, got %v", got)
	}
	if strings.Contains(riskKeywordsJSList(), "仅自己可见") || strings.Contains(riskRulesJSList(), "仅自己可见") {
		t.Fatal("两份风险清单里都不应出现「仅自己可见」")
	}
	// 真·无权限仍然要判出来。
	if got := RiskKindFromText("无权限访问该笔记"); got != RiskPermissionDenied {
		t.Fatalf("无权限应判 permission_denied, got %v", got)
	}
	if got := RiskKindFromText("该笔记已被删除"); got != RiskNoteNotFound {
		t.Fatalf("已删除应判 note_not_found, got %v", got)
	}
}

// 页面探针的规则数组必须来自唯一来源（riskRuleGroups），不能自己内联一份。
func TestRiskRulesSingleSource(t *testing.T) {
	rules := riskRulesJSList()
	for _, want := range []string{"login_expired", "slider_challenge", "captcha", "access_anomaly", "note_not_found", "permission_denied"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("规则数组缺少 %s: %s", want, rules)
		}
	}
	if !strings.Contains(classifyRiskJS, rules) {
		t.Fatal("页面探针的规则数组必须由 riskRulesJSList 内联生成")
	}
	if !strings.Contains(riskKeywordsJSList(), "验证码") {
		t.Fatal("文本探针关键词也应来自同一张表")
	}
}

// AI 总结的取值判据：conversation（页面在用的 AI 会话）优先，且"完成"才算就绪。
// 背景：原实现只读 dqa/onebox 且只等 3s，而实测 AI 答案要 ~13s 才生成完 → 永远读不到。
func TestNormalizeAIResponsePrefersConversation(t *testing.T) {
	// 会话活跃但还没有正文：不算就绪，且应继续等（pending=true）
	probe := aiStateProbe{ConversationActive: true}
	if reply, pending := normalizeAIResponse(probe); reply != nil || !pending {
		t.Fatalf("会话活跃但无正文: reply=%v pending=%v", reply, pending)
	}

	// 流式中（有正文但未完成）：返回正文并标记 pending
	probe = aiStateProbe{ConversationActive: true, ConversationText: "露营要注意…"}
	if reply, pending := normalizeAIResponse(probe); reply == nil || !pending || reply.Content != "露营要注意…" {
		t.Fatalf("流式中: reply=%v pending=%v", reply, pending)
	}

	// 完成（round.isComplete && aiMessage.isFinished）：就绪
	probe = aiStateProbe{
		ConversationActive:   true,
		ConversationText:     "露营要注意…",
		ConversationComplete: true,
		ConversationFinished: true,
	}
	reply, pending := normalizeAIResponse(probe)
	if reply == nil || pending {
		t.Fatalf("完成态应就绪: reply=%v pending=%v", reply, pending)
	}
	if reply.Content != "露营要注意…" || reply.HasMore {
		t.Fatalf("完成态内容/HasMore 不对: %+v", reply)
	}

	// 没有 AI 会话（普通搜索页）：不影响既有 dqa/onebox 路径
	if reply, pending := normalizeAIResponse(aiStateProbe{}); reply != nil || pending {
		t.Fatalf("无 AI 会话: reply=%v pending=%v", reply, pending)
	}
}
