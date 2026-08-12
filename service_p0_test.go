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

func TestSearchDebugFieldsSerialization(t *testing.T) {
	result := xiaohongshu.SearchPageResult{
		Feeds:                        nil,
		DebugSearchTotalMS:           31842,
		DebugSearchWaitMS:            30112,
		DebugSearchResultProbeMs:     []int64{5001, 5000},
		DebugSearchResultProbeCount:  2,
		DebugSearchResultProbeFailed: 1,
		DebugSearchWaitExit:          "keyword_mismatch",
		DebugSearchFallback:          true,
		DebugSearchWaitRounds:        2,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	for _, key := range []string{"debug_search_total_ms", "debug_search_wait_ms", "debug_search_result_probe_ms", "debug_search_result_probe_count", "debug_search_result_probe_failed", "debug_search_wait_exit", "debug_search_fallback", "debug_search_wait_rounds"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("JSON 缺少探针字段 %s: %s", key, string(data))
		}
	}
	if v, ok := m["debug_search_total_ms"].(float64); !ok || int64(v) != 31842 {
		t.Fatalf("debug_search_total_ms = %v, 期望 31842", m["debug_search_total_ms"])
	}
	if v, ok := m["debug_search_fallback"].(bool); !ok || !v {
		t.Fatalf("debug_search_fallback = %v, 期望 true", m["debug_search_fallback"])
	}
}

func TestSearchDebugZeroValuesAreVisible(t *testing.T) {
	result := xiaohongshu.SearchPageResult{}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	for _, key := range []string{"debug_search_total_ms", "debug_search_wait_ms", "debug_search_result_probe_ms", "debug_search_wait_exit", "debug_search_fallback", "debug_search_wait_rounds"} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("零值探针字段 %s 必须可见（不得 omitempty）: %s", key, string(data))
		}
	}
}

func TestTryReuseSessionDegradesToCreateOnUnhealthy(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browseSessions: manager}

	// page=nil 使 CheckReusable 返回 SessionNotReady，触发降级路径
	session := manager.Create(nil, nil, nil)

	result := service.tryReuseSession(context.Background())
	if result != nil {
		t.Fatalf("not-ready session 应降级为创建（返回 nil），得到 %+v", result)
	}
	if _, err := manager.Get(session.ID()); err == nil {
		t.Fatalf("降级创建后旧 session 应已关闭")
	}
}

func TestTryReuseSessionCanceledContextDoesNotDegrade(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browseSessions: manager}

	session := manager.Create(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := service.tryReuseSession(ctx)
	if result == nil || result.Outcome != "blocked" {
		t.Fatalf("取消请求不应降级创建，得到 %+v", result)
	}
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("取消请求不应关闭 session: %v", err)
	}
}

// TestJsonMCPResultWithToolsOpenNoteImagePath 实际经过项目的 jsonMCPResultWithTools 外部包装，
// 断言 open_note 图片真实外部路径为 data.note.imageList[].urlDefault/urlPre。
func TestJsonMCPResultWithToolsOpenNoteImagePath(t *testing.T) {
	open := &xiaohongshu.SessionOpenNoteResponse{
		BrowseSessionInfo: xiaohongshu.BrowseSessionInfo{ID: "s1", Opened: true, Read: true},
		Note: xiaohongshu.OpenedNoteContent{
			NoteID: "n1",
			ImageList: []xiaohongshu.DetailImageInfo{
				{Width: 400, Height: 300, URLDefault: "https://example.com/image-default.jpg", URLPre: "https://example.com/image-pre.jpg"},
			},
		},
		Comments: []xiaohongshu.Comment{{ID: "c1"}},
	}
	result := jsonMCPResultWithTools(open, afterOpenTools)
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("jsonMCPResultWithTools 结果异常: %+v", result)
	}
	raw := []byte(result.Content[0].Text)
	var payload struct {
		Data struct {
			Note struct {
				ImageList []struct {
					URLDefault string `json:"urlDefault"`
					URLPre     string `json:"urlPre"`
				} `json:"imageList"`
			} `json:"note"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("外部 JSON 解析失败: %v\n%s", err, result.Content[0].Text)
	}
	images := payload.Data.Note.ImageList
	if len(images) != 1 || images[0].URLDefault != "https://example.com/image-default.jpg" || images[0].URLPre != "https://example.com/image-pre.jpg" {
		t.Fatalf("open_note 图片路径应为 data.note.imageList[].urlDefault/urlPre: %s", result.Content[0].Text)
	}
}

// TestPublishArgsKeepImagesVideoKeys 锁定发布参数合法 key：publish_content 的 images 与 publish_with_video 的 video 不得误删。
func TestPublishArgsKeepImagesVideoKeys(t *testing.T) {
	content := PublishContentArgs{Title: "图文", Content: "正文", Images: []string{"/tmp/a.jpg"}}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("publish_content 序列化失败: %v", err)
	}
	var payload struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("publish_content 解析失败: %v", err)
	}
	if len(payload.Images) != 1 || payload.Images[0] != "/tmp/a.jpg" {
		t.Fatalf("publish_content images key 缺失或不准确: %s", raw)
	}
	video := PublishVideoArgs{Title: "视频", Content: "正文", Video: "/tmp/v.mp4"}
	rawV, err := json.Marshal(video)
	if err != nil {
		t.Fatalf("publish_with_video 序列化失败: %v", err)
	}
	var vp struct {
		Video string `json:"video"`
	}
	if err := json.Unmarshal(rawV, &vp); err != nil {
		t.Fatalf("publish_with_video 解析失败: %v", err)
	}
	if vp.Video != "/tmp/v.mp4" {
		t.Fatalf("publish_with_video video key 缺失或不准确: %s", rawV)
	}
}
