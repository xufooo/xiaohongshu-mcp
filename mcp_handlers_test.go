package main

import (
	"errors"
	"strings"
	"testing"
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
