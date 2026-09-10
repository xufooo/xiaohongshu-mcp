package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

const commentPagingWindowObjectID = "comment-paging-window"

type commentPagingCDPClient struct {
	responses             [][]byte
	errs                  []error
	runtimeResponses      [][]byte
	runtimeErrs           []error
	targetResponses       [][]byte
	targetErrs            []error
	runtimeCalls          int
	runtimeProbeCalls     int
	runtimeExpressions    []string
	callFunctionCalls           int
	callFunctionDeclarations    []string
	callFunctionObjectIDs       []proto.RuntimeRemoteObjectID
	callFunctionArguments       [][]*proto.RuntimeCallArgument
	targetCalls           int
	eventCh               chan *cdp.Event
	eventOnce             sync.Once
	closeOnce             sync.Once
	afterFunctionCall     func(int)
}

func (c *commentPagingCDPClient) Event() <-chan *cdp.Event {
	c.eventOnce.Do(func() {
		c.eventCh = make(chan *cdp.Event)
	})
	return c.eventCh
}

func (c *commentPagingCDPClient) Close() {
	c.eventOnce.Do(func() {
		c.eventCh = make(chan *cdp.Event)
	})
	c.closeOnce.Do(func() {
		close(c.eventCh)
	})
}

func (c *commentPagingCDPClient) Call(ctx context.Context, _ string, method string, params interface{}) ([]byte, error) {
	if method == "Target.getTargetInfo" {
		index := c.targetCalls
		c.targetCalls++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index < len(c.targetErrs) && c.targetErrs[index] != nil {
			return nil, c.targetErrs[index]
		}
		if index < len(c.targetResponses) {
			return c.targetResponses[index], nil
		}
		return []byte(`{}`), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Target.setDiscoverTargets":
		return []byte(`{}`), nil
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"session-1"}`), nil
	case "Runtime.evaluate":
		c.runtimeCalls++
		var request struct {
			Expression string `json:"expression"`
		}
		if raw, marshalErr := json.Marshal(params); marshalErr == nil {
			_ = json.Unmarshal(raw, &request)
		}
		c.runtimeExpressions = append(c.runtimeExpressions, request.Expression)
		if request.Expression == "window" {
			return []byte(`{"result":{"type":"object","objectId":"comment-paging-window"}}`), nil
		}
		index := c.runtimeProbeCalls
		c.runtimeProbeCalls++
		if index < len(c.runtimeErrs) && c.runtimeErrs[index] != nil {
			return nil, c.runtimeErrs[index]
		}
		if index < len(c.runtimeResponses) {
			return c.runtimeResponses[index], nil
		}
	case "Runtime.callFunctionOn":
		index := c.callFunctionCalls
		c.callFunctionCalls++
		if request, ok := params.(proto.RuntimeCallFunctionOn); ok {
			c.callFunctionDeclarations = append(c.callFunctionDeclarations, request.FunctionDeclaration)
			c.callFunctionObjectIDs = append(c.callFunctionObjectIDs, request.ObjectID)
			c.callFunctionArguments = append(c.callFunctionArguments, request.Arguments)
		}
		if index < len(c.errs) && c.errs[index] != nil {
			return nil, c.errs[index]
		}
		if index < len(c.responses) {
			response := c.responses[index]
			if c.afterFunctionCall != nil {
				c.afterFunctionCall(index)
			}
			return response, nil
		}
	}
	return []byte(`{}`), nil
}

func newCommentPagingPage(t *testing.T, client *commentPagingCDPClient) *hrod.Page {
	t.Helper()
	browserCtx, cancelBrowser := context.WithCancel(context.Background())
	browser := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(client)
	if err := browser.Connect(); err != nil {
		t.Fatalf("初始化 rod browser: %v", err)
	}
	browserEvents := browser.Event()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-browserEvents:
		case <-time.After(time.Second):
			t.Errorf("rod browser 事件 goroutine 未退出")
		}
		cancelBrowser()
	})
	page, err := browser.PageFromTarget("target-1")
	if err != nil {
		t.Fatalf("初始化 rod page: %v", err)
	}
	client.runtimeCalls = 0
	client.runtimeProbeCalls = 0
	client.runtimeExpressions = nil
	client.callFunctionCalls = 0
	client.callFunctionDeclarations = nil
	client.callFunctionObjectIDs = nil
	client.callFunctionArguments = nil
	client.targetCalls = 0
	return &hrod.Page{Rod: page}
}

const realCommentPagingHTML = `<!doctype html>
<div class="comments-container"><span class="total">共 7 条评论</span></div>
<div class="parent-comment" data-id="parent-1">
  <div class="comment-item" id="parent-1">
    <div class="content">parent content</div>
    <div class="author-wrapper"><span class="name">parent user</span></div>
    <div class="interactions"><span class="like">1</span></div>
  </div>
  <div class="reply-container"><div class="list-container">
    <div class="comment-item" id="reply-1">
      <div class="content">reply 1</div>
      <div class="author-wrapper"><span class="name">reply user 1</span></div>
      <div class="interactions"><span class="like">1</span></div>
    </div>
    <div class="comment-item" id="reply-1"><div class="content">duplicate reply</div></div>
    <div class="comment-item"><div class="content">missing stable id</div></div>
    <div class="comment-item" id="reply-empty"><div class="content"></div></div>
    <div class="comment-item" id="reply-seen"><div class="content">already returned</div></div>
    <div class="comment-item" id="reply-2">
      <div class="content">reply 2</div>
      <div class="author-wrapper"><span class="name">reply user 2</span></div>
      <div class="interactions"><span class="like">2</span></div>
    </div>
    <div class="comment-item" id="reply-3">
      <div class="content">reply 3</div>
      <div class="author-wrapper"><span class="name">reply user 3</span></div>
      <div class="interactions"><span class="like">3</span></div>
    </div>
  </div></div>
</div>
<div class="end-container">THE END</div>
<script>
window.authorLikeReads = 0;
for (const element of document.querySelectorAll(".author-wrapper .name, .interactions .like")) {
  const value = element.textContent;
  Object.defineProperty(element, "innerText", {
    configurable: true,
    get() {
      window.authorLikeReads++;
      return value;
    },
  });
}
</script>`

func newRealCommentPagingPage(t *testing.T) *hrod.Page {
	t.Helper()
	bin, hasBrowser := launcher.LookPath()
	if !hasBrowser {
		t.Skip("未找到 Chrome/Chromium，跳过真实分页 JS fixture")
	}
	browserLauncher := launcher.New().Bin(bin).Headless(true).NoSandbox(true)
	controlURL, err := browserLauncher.Launch()
	if err != nil {
		t.Fatalf("启动真实测试浏览器: %v", err)
	}
	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		browserLauncher.Kill()
		t.Fatalf("连接真实测试浏览器: %v", err)
	}
	t.Cleanup(func() {
		_ = browser.Close()
		browserLauncher.Kill()
	})
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		t.Fatalf("创建真实测试页面: %v", err)
	}
	if err := page.SetDocumentContent(realCommentPagingHTML); err != nil {
		t.Fatalf("设置真实 DOM fixture: %v", err)
	}
	return &hrod.Page{Rod: page}
}

func commentPageRuntimeResponse(t *testing.T, snapshot commentPageDOMSnapshot) []byte {
	t.Helper()
	value, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("序列化分页响应: %v", err)
	}
	envelope := struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}{}
	envelope.Result.Type = "string"
	envelope.Result.Value = string(value)
	response, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("序列化 Runtime 响应: %v", err)
	}
	return response
}

func commentPageTargetInfoResponse(rawURL string) []byte {
	response, _ := json.Marshal(map[string]interface{}{
		"targetInfo": map[string]string{
			"targetId": "target-1",
			"url":      rawURL,
		},
	})
	return response
}

func TestParseFeedIDFromPageURL(t *testing.T) {
	tests := []struct {
		name, input, want string
		ok                 bool
	}{
		{"explore query fragment encoded ID", "https://www.xiaohongshu.com/explore/%36a7a944f0000000024026c8d?x=1#comments", "6a7a944f0000000024026c8d", true},
		{"discovery item", "https://www.xiaohongshu.com/discovery/item/5f4d8e7b00000000010001a2#comments", "5f4d8e7b00000000010001a2", true},
		{"not detail", "https://www.xiaohongshu.com/explore", "", false},
		{"invalid escape", "https://www.xiaohongshu.com/explore/%zz", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseFeedIDFromPageURL(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseFeedIDFromPageURL() = %q, %v; 期望 %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func runRecoverCommentBatchSession(t *testing.T, ctx context.Context, client *commentPagingCDPClient, expectedFeedID string) error {
	t.Helper()
	page := newCommentPagingPage(t, client)
	return (&BrowseSession{page: page}).recoverCommentBatchSession(ctx, page, nil, expectedFeedID)
}

func assertCommentPagingCalls(t *testing.T, client *commentPagingCDPClient, targetCalls int) {
	t.Helper()
	if client.targetCalls != targetCalls || client.runtimeCalls != 0 {
		t.Fatalf("CDP 调用次数错误: target=%d runtime=%d, 期望 target=%d runtime=0", client.targetCalls, client.runtimeCalls, targetCalls)
	}
}

func assertCommentPagingMainCalls(t *testing.T, client *commentPagingCDPClient, wantCalls int, feedID string) {
	t.Helper()
	if client.callFunctionCalls != wantCalls || len(client.callFunctionDeclarations) != wantCalls ||
		len(client.callFunctionObjectIDs) != wantCalls || len(client.callFunctionArguments) != wantCalls {
		t.Fatalf("Runtime.callFunctionOn 调用契约错误: calls=%d declarations=%d objectIDs=%d arguments=%d want=%d",
			client.callFunctionCalls, len(client.callFunctionDeclarations), len(client.callFunctionObjectIDs), len(client.callFunctionArguments), wantCalls)
	}
	for i := 0; i < wantCalls; i++ {
		declaration := client.callFunctionDeclarations[i]
		if !strings.HasPrefix(declaration, "function() { return (") ||
			!strings.Contains(declaration, "(feedID) => {") ||
			!strings.Contains(declaration, "extractComments(feedID)") ||
			!strings.Contains(declaration, ").apply(this, arguments) }") {
			t.Fatalf("分页主调用 FunctionDeclaration 错误: %q", declaration)
		}
		if client.callFunctionObjectIDs[i] != commentPagingWindowObjectID {
			t.Fatalf("分页主调用 objectId 错误: got=%q want=%q", client.callFunctionObjectIDs[i], commentPagingWindowObjectID)
		}
		args := client.callFunctionArguments[i]
		if len(args) != 1 || args[0] == nil || args[0].ObjectID != "" {
			t.Fatalf("分页主调用应使用一个结构化值参数: %+v", args)
		}
		value, err := json.Marshal(args[0].Value)
		if err != nil {
			t.Fatalf("读取分页主调用实参失败: %v", err)
		}
		var gotFeedID string
		if err := json.Unmarshal(value, &gotFeedID); err != nil || gotFeedID != feedID {
			t.Fatalf("分页主调用实参错误: raw=%s got=%q want=%q", value, gotFeedID, feedID)
		}
	}
}

func TestRecoverCommentBatchSession(t *testing.T) {
	tests := []struct {
		name, wantErr, wantDetail string
		targetResponses          [][]byte
		targetErrs               []error
		targetCalls              int
	}{
		{"success", "", "", [][]byte{commentPageTargetInfoResponse("https://www.xiaohongshu.com/explore/feed-1?x=1#comments")}, nil, 1},
		{"first failure then success", "", "", [][]byte{commentPageTargetInfoResponse("https://www.xiaohongshu.com/explore"), commentPageTargetInfoResponse("https://www.xiaohongshu.com/discovery/item/feed-1?x=1#comments")}, nil, 2},
		{"two failures", "无法确认 feed", "second target failure", nil, []error{errors.New("first target failure"), errors.New("second target failure")}, 2},
		{"feed mismatch", "不匹配", "", [][]byte{commentPageTargetInfoResponse("https://www.xiaohongshu.com/explore/other-feed")}, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &commentPagingCDPClient{targetResponses: tt.targetResponses, targetErrs: tt.targetErrs}
			err := runRecoverCommentBatchSession(t, context.Background(), client, "feed-1")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("恢复确认不应失败: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("恢复确认错误契约错误: %v", err)
			}
			if tt.wantDetail != "" && (err == nil || !strings.Contains(err.Error(), tt.wantDetail)) {
				t.Fatalf("恢复确认错误详情错误: %v", err)
			}
			assertCommentPagingCalls(t, client, tt.targetCalls)
		})
	}
}

func TestRecoverCommentBatchSessionPropagatesCallerContext(t *testing.T) {
	canceledCtx, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	deadlineCtx, cancelDeadline := context.WithTimeout(context.Background(), 0)
	defer cancelDeadline()

	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "canceled", ctx: canceledCtx, want: context.Canceled},
		{name: "deadline", ctx: deadlineCtx, want: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &commentPagingCDPClient{targetResponses: [][]byte{commentPageTargetInfoResponse("https://www.xiaohongshu.com/explore/feed-1")}}
			err := runRecoverCommentBatchSession(t, tt.ctx, client, "feed-1")
			if !errors.Is(err, tt.want) {
				t.Fatalf("caller context 错误未原样传播: got=%v want=%v", err, tt.want)
			}
			assertCommentPagingCalls(t, client, 1)
		})
	}
}

func TestExtractCommentsPagePreservesErrors(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	terminationErr := context.DeadlineExceeded
	probeErr := errors.New("probe failed")
	tests := []struct {
		name               string
		ctx                context.Context
		errs               []error
		runtimeErrs        []error
		runtimeResponses   [][]byte
		wantErr            error
		wantContains       string
		wantFatal          bool
		wantRuntimeCalls   int
		wantFunctionCalls  int
		wantProbe          bool
		probeErrorHidden   bool
	}{
		{
			name:             "deadline sentinel probes once (fake CDP)",
			errs:             []error{terminationErr},
			runtimeResponses: [][]byte{[]byte(`{"result":{"type":"string","value":"full-scan"}}`)},
			wantErr:          terminationErr,
			wantRuntimeCalls: 2,
			wantFunctionCalls: 1,
			wantProbe:        true,
		},
		{
			name:             "ordinary error does not probe",
			errs:             []error{errors.New("local eval timeout")},
			wantContains:     "local eval timeout",
			wantRuntimeCalls: 1,
			wantFunctionCalls: 1,
		},
		{
			name:             "fatal renderer does not probe",
			errs:             []error{fmt.Errorf("%w: renderer closed", ErrFatalRendererError)},
			wantFatal:        true,
			wantRuntimeCalls: 1,
			wantFunctionCalls: 1,
		},
		{
			name:             "caller canceled does not probe",
			ctx:              canceledCtx,
			wantErr:          context.Canceled,
			wantRuntimeCalls: 0,
			wantFunctionCalls: 0,
		},
		{
			name:             "probe failure preserves original error",
			errs:             []error{terminationErr},
			runtimeErrs:      []error{probeErr},
			wantErr:          terminationErr,
			wantRuntimeCalls: 2,
			wantFunctionCalls: 1,
			wantProbe:        true,
			probeErrorHidden: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &commentPagingCDPClient{
				errs:             tt.errs,
				runtimeErrs:      tt.runtimeErrs,
				runtimeResponses: tt.runtimeResponses,
			}
			page := newCommentPagingPage(t, client)
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			_, err := extractCommentsPageWithProgressFromDOM(ctx, page, "feed-1")
			if err == nil {
				t.Fatal("期望分页 Eval 返回错误")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("原始错误未保留: got=%v want=%v", err, tt.wantErr)
			}
			if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("错误应包含 %q: %v", tt.wantContains, err)
			}
			if tt.wantFatal && !IsFatalRendererError(err) {
				t.Fatalf("fatal renderer 错误未原样保留: %v", err)
			}
			if tt.probeErrorHidden && strings.Contains(err.Error(), probeErr.Error()) {
				t.Fatalf("probe 错误覆盖了原始错误: %v", err)
			}
			if client.runtimeCalls != tt.wantRuntimeCalls {
				t.Fatalf("Runtime.evaluate 调用次数错误: got=%d want=%d", client.runtimeCalls, tt.wantRuntimeCalls)
			}
			assertCommentPagingMainCalls(t, client, tt.wantFunctionCalls, "feed-1")
			if len(client.runtimeExpressions) != tt.wantRuntimeCalls {
				t.Fatalf("Runtime.evaluate expression 记录数量错误: got=%d want=%d expressions=%+v", len(client.runtimeExpressions), tt.wantRuntimeCalls, client.runtimeExpressions)
			}
			if tt.wantRuntimeCalls > 0 && client.runtimeExpressions[0] != "window" {
				t.Fatalf("Runtime.evaluate 首次调用应获取 window context: %+v", client.runtimeExpressions)
			}
			if tt.wantProbe {
				if len(client.runtimeExpressions) != 2 || !strings.Contains(client.runtimeExpressions[1], "__xhsCommentPaginationPhase") {
					t.Fatalf("应恰好执行一次阶段 probe: %+v", client.runtimeExpressions)
				}
			} else if len(client.runtimeExpressions) > 1 {
				t.Fatalf("非 termination 路径不应执行 probe: %+v", client.runtimeExpressions)
			}
		})
	}
}

func TestLoadCommentsBatchReturnsPartialBatchAfterLocalEvalTimeout(t *testing.T) {
	client := &commentPagingCDPClient{
		responses: [][]byte{commentPageRuntimeResponse(t, commentPageDOMSnapshot{
			Comments: []Comment{{ID: "comment-1", NoteID: "feed-1", Content: "内容"}},
			Progress: commentProgress{Total: 1, AtEnd: true},
		})},
		errs: []error{nil, context.DeadlineExceeded},
	}
	page := newCommentPagingPage(t, client)
	input := &CommentCursor{FeedID: "feed-1", Round: 1}

	comments, next, hasMore, err := loadCommentsBatch(context.Background(), page, CommentLoadConfig{ScrollSpeed: "fast"}, input, 20)
	if err != nil {
		t.Fatalf("已有结果后局部 Eval 超时不应失败: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != "comment-1" || !hasMore {
		t.Fatalf("局部超时后的部分结果错误: comments=%+v hasMore=%v", comments, hasMore)
	}
	if next == nil || len(next.ReturnedIDs) != 1 || next.ReturnedIDs[0] != "comment-1" {
		t.Fatalf("cursor 未严格增长: %+v", next)
	}
	if len(input.ReturnedIDs) != 0 {
		t.Fatalf("输入 cursor 不应被修改: %+v", input)
	}
}

func TestLoadCommentsBatchDoesNotConsumeCursorOnEvalFailure(t *testing.T) {
	wantErr := errors.New("renderer eval failed")
	client := &commentPagingCDPClient{errs: []error{wantErr}}
	page := newCommentPagingPage(t, client)
	input := &CommentCursor{FeedID: "feed-1", Round: 1, ReturnedIDs: []string{"old-comment"}}

	comments, next, hasMore, err := loadCommentsBatch(context.Background(), page, CommentLoadConfig{}, input, 20)
	if !errors.Is(err, wantErr) || comments != nil || next != nil || hasMore {
		t.Fatalf("Eval 失败传播或返回值错误: comments=%+v next=%+v hasMore=%v err=%v", comments, next, hasMore, err)
	}
	if len(input.ReturnedIDs) != 1 || input.ReturnedIDs[0] != "old-comment" {
		t.Fatalf("失败时不得消费输入 cursor: %+v", input)
	}
}

func TestLoadCommentsBatchPropagatesCallerContextDeadline(t *testing.T) {
	client := &commentPagingCDPClient{}
	page := newCommentPagingPage(t, client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := &CommentCursor{FeedID: "feed-1", Round: 1}

	_, _, _, err := loadCommentsBatch(ctx, page, CommentLoadConfig{}, input, 20)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller context 错误被吞掉: %v", err)
	}
}

func TestLoadCommentsBatchDedupesStableIDsAndCapsAtMaxItems(t *testing.T) {
	comments := []Comment{
		{ID: "old-comment", Content: "old"},
		{ID: "comment-1", Content: "one"},
		{ID: "comment-1", Content: "duplicate"},
		{ID: "", Content: "missing id"},
		{ID: "empty-content", Content: ""},
	}
	for i := 2; i <= 51; i++ {
		comments = append(comments, Comment{ID: fmt.Sprintf("comment-%d", i), Content: fmt.Sprintf("content-%d", i)})
	}
	client := &commentPagingCDPClient{
		responses: [][]byte{commentPageRuntimeResponse(t, commentPageDOMSnapshot{
			Comments:    comments,
			Progress:    commentProgress{Total: 100, AtEnd: true},
		})},
	}
	page := newCommentPagingPage(t, client)
	input := &CommentCursor{FeedID: "feed-1", Round: 1, ReturnedIDs: []string{"old-comment", "old-comment", "idx_legacy", ""}}

	got, next, hasMore, err := loadCommentsBatch(context.Background(), page, CommentLoadConfig{ScrollSpeed: "fast"}, input, 50)
	if err != nil {
		t.Fatalf("分页加载不应失败: %v", err)
	}
	if !hasMore {
		t.Fatal("达到批次上限且仍有评论时应返回 hasMore=true")
	}
	if len(got) != 50 {
		t.Fatalf("批次数量错误: got=%d want=50", len(got))
	}
	seen := make(map[string]struct{}, len(got))
	for _, comment := range got {
		if comment.ID == "" || comment.Content == "" {
			t.Fatalf("批次包含无效评论: %+v", comment)
		}
		if _, ok := seen[comment.ID]; ok {
			t.Fatalf("批次包含重复稳定 ID: %q", comment.ID)
		}
		seen[comment.ID] = struct{}{}
	}
	if next == nil || len(next.ReturnedIDs) != 51 || next.ReturnedIDs[0] != "old-comment" {
		t.Fatalf("下一 cursor 错误: %+v", next)
	}
	for i := 0; i < 50; i++ {
		want := fmt.Sprintf("comment-%d", i+1)
		if next.ReturnedIDs[i+1] != want {
			t.Fatalf("下一 cursor 顺序错误: index=%d got=%q want=%q", i+1, next.ReturnedIDs[i+1], want)
		}
	}
	if len(input.ReturnedIDs) != 4 || input.ReturnedIDs[1] != "old-comment" || input.ReturnedIDs[2] != "idx_legacy" {
		t.Fatalf("输入 cursor 不应被修改: %+v", input)
	}
}

func TestLoadCommentsBatchCapsInGoAndContinuesOverflow(t *testing.T) {
	snapshot := commentPageDOMSnapshot{
		Comments: []Comment{
			{
				ID:              "parent-1",
				Content:         "parent",
				SubCommentCount: "3",
				SubComments: []Comment{
					{ID: "reply-1", Content: "reply 1"},
					{ID: "reply-2", Content: "reply 2"},
					{ID: "reply-3", Content: "reply 3"},
				},
			},
		},
		Progress: commentProgress{Total: 4, AtEnd: true},
	}
	response := commentPageRuntimeResponse(t, snapshot)
	client := &commentPagingCDPClient{responses: [][]byte{response, response, response}}
	page := newCommentPagingPage(t, client)
	input := &CommentCursor{FeedID: "feed-1", Round: 1}

	got, next, hasMore, err := loadCommentsBatch(context.Background(), page, CommentLoadConfig{ScrollSpeed: "fast"}, input, 2)
	if err != nil {
		t.Fatalf("分页加载不应失败: %v", err)
	}
	if len(got) != 2 || !hasMore || got[0].ID != "parent-1" || got[0].SubCommentCount != "3" || got[1].ID != "reply-1" {
		t.Fatalf("Go 侧 limit 或父子顺序错误: comments=%+v hasMore=%v", got, hasMore)
	}
	if next == nil || len(next.ReturnedIDs) != 2 || next.ReturnedIDs[0] != "parent-1" || next.ReturnedIDs[1] != "reply-1" {
		t.Fatalf("溢出评论不应写入本轮 cursor: %+v", next)
	}
	if len(input.ReturnedIDs) != 0 {
		t.Fatalf("输入 cursor 不应被修改: %+v", input)
	}

	got, next, hasMore, err = loadCommentsBatch(context.Background(), page, CommentLoadConfig{ScrollSpeed: "fast"}, next, 2)
	if err != nil {
		t.Fatalf("续页加载不应失败: %v", err)
	}
	if len(got) != 2 || hasMore || got[0].ID != "reply-2" || got[1].ID != "reply-3" {
		t.Fatalf("下一 cursor 未取得 overflow: comments=%+v hasMore=%v", got, hasMore)
	}
	if next == nil || len(next.ReturnedIDs) != 4 || next.ReturnedIDs[2] != "reply-2" || next.ReturnedIDs[3] != "reply-3" {
		t.Fatalf("续页 cursor 错误: %+v", next)
	}
}

func TestLoadCommentsBatchLimitZeroUsesSameSnapshotForProgress(t *testing.T) {
	first := commentPageDOMSnapshot{
		Comments: []Comment{
			{ID: "comment-1", Content: "one"},
			{ID: "comment-2", Content: "two"},
		},
		Progress: commentProgress{Total: 3, AtEnd: true},
	}
	second := commentPageDOMSnapshot{
		Comments: []Comment{
			{ID: "comment-1", Content: "one"},
			{ID: "comment-2", Content: "two"},
			{ID: "comment-3", Content: "three"},
		},
		Progress: commentProgress{Total: 3, AtEnd: true},
	}
	client := &commentPagingCDPClient{
		responses: [][]byte{
			commentPageRuntimeResponse(t, first),
			commentPageRuntimeResponse(t, second),
		},
	}
	page := newCommentPagingPage(t, client)
	input := &CommentCursor{FeedID: "feed-1", Round: 1}

	got, next, hasMore, err := loadCommentsBatch(context.Background(), page, CommentLoadConfig{ScrollSpeed: "fast"}, input, 2)
	if err != nil {
		t.Fatalf("limit=0 收尾探测不应失败: %v", err)
	}
	if len(got) != 2 || got[0].ID != "comment-1" || got[1].ID != "comment-2" || !hasMore {
		t.Fatalf("limit=0 完整快照进度判定错误: comments=%+v hasMore=%v", got, hasMore)
	}
	if next == nil || len(next.ReturnedIDs) != 2 || next.ReturnedIDs[0] != "comment-1" || next.ReturnedIDs[1] != "comment-2" {
		t.Fatalf("limit=0 不应把 overflow 写入 cursor: %+v", next)
	}
	if client.runtimeCalls != 1 {
		t.Fatalf("limit=0 不应增加独立 progress Eval: runtimeCalls=%d", client.runtimeCalls)
	}
	assertCommentPagingMainCalls(t, client, 2, "feed-1")
}

func TestExtractCommentsPageExecutesPaginationJS(t *testing.T) {
	page := newRealCommentPagingPage(t)
	if _, err := page.Rod.Eval(`() => {
		const phaseMarker = "__xhsCommentPaginationPhase";
		let current = "";
		let completed = false;
		let probed = false;
		Object.defineProperty(window, phaseMarker, {
			configurable: true,
			get() {
				probed = true;
				return current;
			},
			set(value) {
				current = value;
				if (value === "completed") completed = true;
			},
		});
		window.__xhsCommentPaginationPhaseState = () => [completed, current, probed].join("|");
	}`); err != nil {
		t.Fatalf("安装分页阶段测试 accessor: %v", err)
	}
	readAuthorLikeReads := func() string {
		result, err := page.Rod.Eval(`() => String(window.authorLikeReads)`)
		if err != nil || result == nil {
			t.Fatalf("读取 author/like 访问计数: %v", err)
		}
		return result.Value.Str()
	}

	snapshot, err := extractCommentsPageWithProgressFromDOM(context.Background(), page, "feed-1")
	if err != nil {
		t.Fatalf("真实分页 JS 执行失败: %v", err)
	}
	phaseStateResult, err := page.Rod.Eval(`() => window.__xhsCommentPaginationPhaseState()`)
	if err != nil || phaseStateResult == nil {
		t.Fatalf("读取分页阶段测试状态: %v", err)
	}
	if got := phaseStateResult.Value.Str(); got != "true||false" {
		t.Fatalf("成功路径阶段 marker 状态错误: %s", got)
	}
	if len(snapshot.Comments) != 1 || snapshot.Comments[0].ID != "parent-1" {
		t.Fatalf("完整父评论快照错误: %+v", snapshot.Comments)
	}
	parent := snapshot.Comments[0]
	if parent.SubCommentCount != "6" || len(parent.SubComments) != 6 || snapshot.Progress.Total != 7 {
		t.Fatalf("完整子评论快照或 progress 错误: %+v", snapshot)
	}
	wantIDs := []string{"reply-1", "reply-1", "", "reply-seen", "reply-2", "reply-3"}
	for i, want := range wantIDs {
		if parent.SubComments[i].ID != want {
			t.Fatalf("子评论快照顺序错误: index=%d got=%q want=%q", i, parent.SubComments[i].ID, want)
		}
	}
	if got := readAuthorLikeReads(); got != "8" {
		t.Fatalf("完整快照未物化全部作者/点赞: reads=%s", got)
	}
}

func TestCommentPagingOutcomeCarriesProgressToCompletion(t *testing.T) {
	loaderConfig := CommentLoadConfig{ScrollSpeed: "fast"}
	completionConfig := CommentLoadConfig{ClickMoreReplies: true, MaxRepliesThreshold: 0, ScrollSpeed: "fast"}

	t.Run("terminal progress is reused without another DOM eval", func(t *testing.T) {
		client := &commentPagingCDPClient{
			responses: [][]byte{
				commentPageRuntimeResponse(t, commentPageDOMSnapshot{
					Comments: []Comment{{ID: "comment-1", NoteID: "feed-1", Content: "内容"}},
					Progress: commentProgress{Total: 1, AtEnd: true},
				}),
				commentPageRuntimeResponse(t, commentPageDOMSnapshot{
					Progress: commentProgress{Total: 1, AtEnd: true},
				}),
			},
		}
		page := newCommentPagingPage(t, client)
		input := &CommentCursor{FeedID: "feed-1", Round: 1}

		outcome, err := runDetailCommentsBatch(context.Background(), func(loadCtx context.Context) (commentBatchOutcome, error) {
			return loadCommentsBatchOutcome(loadCtx, page, loaderConfig, input, 20)
		})
		if err != nil {
			t.Fatalf("终批 loader 不应失败: %v", err)
		}
		if len(outcome.comments) != 1 || outcome.hasMore || outcome.progress.Total != 1 || !outcome.progress.AtEnd || outcome.progress.NoComments {
			t.Fatalf("终批 outcome 错误: %+v", outcome)
		}

		session := &BrowseSession{
			page:              page,
			currentFeedID:     "feed-1",
			opened:             true,
			openedNoteContent: OpenedNoteContent{NoteID: "feed-1"},
		}
		runtimeCallsBeforeCompletion := client.runtimeCalls
		response, next, hasMore, err := session.completeDetailCommentsBatch(
			context.Background(), page, &evalTimeoutCounter{}, "feed-1", input, 20, completionConfig, outcome,
		)
		if err != nil {
			t.Fatalf("终批 completion 不应失败: %v", err)
		}
		if response == nil || next == nil || hasMore || response.Comments.HasMore || !response.Comments.Complete ||
			response.Comments.IncompleteReason != "" ||
			response.Comments.TotalItems != 1 || response.Comments.SeenCount != 1 {
			t.Fatalf("终批响应错误: response=%+v next=%+v hasMore=%v", response, next, hasMore)
		}
		if client.runtimeCalls != runtimeCallsBeforeCompletion {
			t.Fatalf("completion 不应重复执行 progress DOM Eval: before=%d after=%d", runtimeCallsBeforeCompletion, client.runtimeCalls)
		}
	})

	t.Run("context cancellation after successful collect keeps progress", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := &commentPagingCDPClient{
			responses: [][]byte{commentPageRuntimeResponse(t, commentPageDOMSnapshot{
				Comments: []Comment{{ID: "comment-1", NoteID: "feed-1", Content: "内容"}},
				Progress: commentProgress{Total: 1, AtEnd: true},
			})},
			afterFunctionCall: func(index int) {
				if index == 0 {
					cancel()
				}
			},
		}
		page := newCommentPagingPage(t, client)
		outcome, err := loadCommentsBatchOutcome(ctx, page, loaderConfig, &CommentCursor{FeedID: "feed-1", Round: 1}, 20)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("成功 collect 后的 context 错误未保留: %v", err)
		}
		if outcome.progress.Total != 1 || !outcome.progress.AtEnd || outcome.progress.NoComments {
			t.Fatalf("成功 collect 后 progress 丢失: %+v", outcome.progress)
		}
		if client.runtimeCalls != 1 {
			t.Fatalf("取消后不应再次执行 progress DOM Eval: %d", client.runtimeCalls)
		}
	})
}
