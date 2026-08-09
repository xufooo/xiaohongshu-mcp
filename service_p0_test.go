package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func TestHandleSessionOperationErrorClosesFatalSession(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	service.handleSessionOperationError(context.Background(), session.ID(), session, fmt.Errorf("wrapped: %w", xiaohongshu.ErrFatalRendererError))
	if _, err := manager.Get(session.ID()); err == nil {
		t.Fatal("fatal renderer error 后 session 应被移除")
	}
}

func TestHandleSessionOperationErrorKeepsCanceledContext(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.handleSessionOperationError(ctx, session.ID(), session, fmt.Errorf("operation failed"))
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("请求取消后应保留 session: %v", err)
	}
	session.Close()
}

func TestHandleSessionOperationErrorKeepsDeadlineSession(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	service.handleSessionOperationError(context.Background(), session.ID(), session, fmt.Errorf("wrapped: %w", context.DeadlineExceeded))
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("deadline error 后应保留 session: %v", err)
	}
	session.Close()
}

func TestHandleSessionOperationErrorKeepsOrdinarySession(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	service.handleSessionOperationError(context.Background(), session.ID(), session, fmt.Errorf("business error"))
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("普通业务错误应保留 session: %v", err)
	}
	session.Close()
}

func TestBuildBrowseSessionReuseResultRejectsUnextendedTTL(t *testing.T) {
	now := time.Now()
	previous := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now.Add(time.Minute)}
	renewed := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now.Add(time.Minute)}
	result := buildBrowseSessionReuseResult(previous, renewed, now)
	if result.Outcome != "blocked" {
		t.Fatalf("Outcome = %q, 期望 blocked", result.Outcome)
	}
	if result.RecommendedAction != "retry" {
		t.Fatalf("RecommendedAction = %q, 期望 retry", result.RecommendedAction)
	}
	if result.Status.Status != xiaohongshu.SessionExpired {
		t.Fatalf("Status.Status = %v, 期望 SessionExpired", result.Status.Status)
	}
	if result.Status.Ready {
		t.Fatal("Status.Ready 应为 false")
	}
	if result.Status.LastError != "session 已过期" {
		t.Fatalf("Status.LastError = %q, 期望 session 已过期", result.Status.LastError)
	}
	if result.Session != nil {
		t.Fatal("顶层 Session 应为 nil")
	}
	if result.Status.Session != nil {
		t.Fatal("Status.Session 应为 nil")
	}
}

func TestBuildBrowseSessionReuseResultAcceptsExtendedTTL(t *testing.T) {
	now := time.Now()
	previous := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now}
	renewed := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now.Add(10 * time.Minute)}
	result := buildBrowseSessionReuseResult(previous, renewed, now)
	if result.Outcome != "reused" {
		t.Fatalf("Outcome = %q, 期望 reused", result.Outcome)
	}
	if result.RecommendedAction != "continue" {
		t.Fatalf("RecommendedAction = %q, 期望 continue", result.RecommendedAction)
	}
	if result.Status.Status != xiaohongshu.SessionReady {
		t.Fatalf("Status.Status = %v, 期望 SessionReady", result.Status.Status)
	}
	if !result.Status.Ready {
		t.Fatal("Status.Ready 应为 true")
	}
	if result.Session == nil || result.Session.ExpiresAt != renewed.ExpiresAt {
		t.Fatal("顶层 Session 应为续期后信息")
	}
	if result.Status.Session == nil || result.Status.Session.ExpiresAt != renewed.ExpiresAt {
		t.Fatal("Status.Session 应为续期后信息")
	}
	if !result.Status.HealthCheckedAt.Equal(now) {
		t.Fatalf("HealthCheckedAt = %v, 期望 %v", result.Status.HealthCheckedAt, now)
	}
}

func TestCreateBrowseSessionResultDebugFieldsJSON(t *testing.T) {
	result := &xiaohongshu.CreateBrowseSessionResult{
		Outcome:           "created",
		RecommendedAction: "continue",
		DebugStartupMS:    68432,
		DebugReadyMS:      27118,
		DebugTotalMS:      95674,
		DebugReused:       false,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	for _, key := range []string{"debug_startup_ms", "debug_ready_ms", "debug_total_ms", "debug_reused"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("JSON 缺少探针字段 %s: %s", key, string(data))
		}
	}
	if v, ok := m["debug_startup_ms"].(float64); !ok || int64(v) != 68432 {
		t.Fatalf("debug_startup_ms = %v, 期望 68432", m["debug_startup_ms"])
	}
	if v, ok := m["debug_reused"].(bool); !ok || v {
		t.Fatalf("debug_reused = %v, 期望 false", m["debug_reused"])
	}
}

func TestCreateBrowseSessionResultDebugZeroValuesAreVisible(t *testing.T) {
	result := &xiaohongshu.CreateBrowseSessionResult{Outcome: "reused"}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	for _, key := range []string{"debug_startup_ms", "debug_ready_ms", "debug_total_ms", "debug_reused"} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("零值探针字段 %s 必须可见（不得 omitempty）: %s", key, string(data))
		}
	}
}
