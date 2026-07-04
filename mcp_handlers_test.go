package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func TestSessionMCPErrorResultIncludesNextStepPayload(t *testing.T) {
	result := sessionMCPErrorResult("session状态获取失败: 缺少session_id参数", sessionNextStepCreateSession())
	if result == nil || !result.IsError {
		t.Fatalf("sessionMCPErrorResult should return an error result: %+v", result)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(result.Content))
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "session状态获取失败: 缺少session_id参数") {
		t.Fatalf("error text missing original message: %q", text)
	}
	if !strings.Contains(text, `"next_step"`) || !strings.Contains(text, `"tool": "create_browse_session"`) {
		t.Fatalf("next_step payload missing from error text: %q", text)
	}
}

func TestSessionMCPErrorFromErrSuggestsReadForUnreadInteraction(t *testing.T) {
	result := sessionMCPErrorFromErr("session点赞失败", errors.New("互动只能对已阅读的笔记执行"), sessionNextStepState())
	text := result.Content[0].Text
	if !strings.Contains(text, `"tool": "session_read"`) {
		t.Fatalf("expected session_read next step, got %q", text)
	}
}

func TestSessionMCPErrorFromErrSuggestsSearchForMissingResultRef(t *testing.T) {
	result := sessionMCPErrorFromErr("session打开笔记失败", errors.New("未找到搜索结果引用: 3"), sessionNextStepState())
	text := result.Content[0].Text
	if !strings.Contains(text, `"tool": "session_search"`) {
		t.Fatalf("expected session_search next step, got %q", text)
	}
}

func TestSessionStateResultKeepsJSONTextWithSummaryField(t *testing.T) {
	state := &xiaohongshu.BrowseSessionPageState{
		Summary:      "当前: search ready=true results=3 seen=1",
		Kind:         xiaohongshu.XHSReadySearch,
		Ready:        true,
		ResultsCount: 3,
		SeenCount:    1,
		Current: xiaohongshu.BrowseSessionCurrent{
			NextHint: "可用 session_open_note 打开 results 中的 result_ref",
		},
		RecommendedAction: &xiaohongshu.BrowseSessionAction{
			Ref:       "open_note:2",
			Tool:      "session_open_note",
			ResultRef: "2",
			FeedID:    "feed-2",
		},
	}

	result := jsonMCPResult(state, "session状态获取成功")
	text := result.Content[0].Text
	if !strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Fatalf("session state result should remain JSON text, got %q", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("session state result should be valid JSON: %v\n%s", err, text)
	}
	if decoded["summary"] != "当前: search ready=true results=3 seen=1" {
		t.Fatalf("summary field missing from JSON: %+v", decoded)
	}
	if _, ok := decoded["recommended_action"]; !ok {
		t.Fatalf("recommended_action missing from JSON: %+v", decoded)
	}
}

func TestSessionDetailMissingSessionIDSuggestsCreateSession(t *testing.T) {
	result := (&AppServer{}).handleSessionDetail(context.Background(), BrowseSessionIDArgs{})
	if result == nil || !result.IsError {
		t.Fatalf("session detail without session_id should return an error result: %+v", result)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "session详情获取失败: 缺少session_id参数") {
		t.Fatalf("error text missing session_detail message: %q", text)
	}
	if !strings.Contains(text, `"tool": "create_browse_session"`) {
		t.Fatalf("expected create_browse_session next step, got %q", text)
	}
}

func TestSessionOpenNoteAllowsMissingXsecTokenThroughValidation(t *testing.T) {
	app := &AppServer{
		xiaohongshuService: &XiaohongshuService{
			browseSessions: xiaohongshu.NewBrowseSessionManager(time.Minute),
		},
	}

	result := app.handleSessionOpenNote(context.Background(), SessionOpenNoteArgs{
		SessionID: "missing-session",
		ResultRef: "0",
	})
	if result == nil || !result.IsError {
		t.Fatalf("session open note without xsec_token should reach session layer and return an error result: %+v", result)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "缺少xsec_token参数") {
		t.Fatalf("xsec_token should not be required by parameter validation, got %q", text)
	}
	if !strings.Contains(text, `"next_step"`) {
		t.Fatalf("expected next_step payload when xsec_token is omitted, got %q", text)
	}
}
