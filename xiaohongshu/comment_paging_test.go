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
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

type commentPagingCDPClient struct {
	responses    [][]byte
	errs         []error
	runtimeCalls int
	eventCh      chan *cdp.Event
	eventOnce    sync.Once
	closeOnce    sync.Once
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Target.setDiscoverTargets":
		return []byte(`{}`), nil
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"session-1"}`), nil
	case "Runtime.evaluate":
		index := c.runtimeCalls
		c.runtimeCalls++
		if index < len(c.errs) && c.errs[index] != nil {
			return nil, c.errs[index]
		}
		if index < len(c.responses) {
			return c.responses[index], nil
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

func TestExtractCommentsPagePreservesErrors(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantContains string
		wantFatal    bool
	}{
		{
			name:         "普通错误",
			err:          errors.New("local eval timeout"),
			wantContains: "local eval timeout",
		},
		{
			name:      "fatal renderer",
			err:       fmt.Errorf("%w: renderer closed", ErrFatalRendererError),
			wantFatal: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &commentPagingCDPClient{errs: []error{tt.err}}
			page := newCommentPagingPage(t, client)
			_, err := extractCommentsPageWithProgressFromDOM(context.Background(), page, "feed-1", nil, 2)
			if err == nil {
				t.Fatal("期望分页 Eval 返回错误")
			}
			if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("错误应包含 %q: %v", tt.wantContains, err)
			}
			if tt.wantFatal && !IsFatalRendererError(err) {
				t.Fatalf("fatal renderer 错误未原样保留: %v", err)
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
