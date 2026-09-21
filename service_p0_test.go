package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
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
	step := startPageBlockedStep(context.Background(), result)
	if step.Tool != "start_page" {
		t.Fatalf("next_step.tool = %q, 期望 start_page", step.Tool)
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

// TestToolResultWithStepOpenNoteImagePath 实际经过项目的 toolResultWithStep 外部包装，
// 断言 open_note 图片真实外部路径为 data.note.imageList[].urlDefault/urlPre。
func TestToolResultWithStepOpenNoteImagePath(t *testing.T) {
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
	result := toolResultWithStep(open, xiaohongshu.NextStep{Tool: "get_note_detail"}, []string{"get_note_detail"})
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("toolResultWithStep 结果异常: %+v", result)
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

// parseSessionErrorNextStep 取出错误响应里 next_step 的结构化部分。
func parseSessionErrorNextStep(t *testing.T, result *MCPToolResult) xiaohongshu.NextStep {
	t.Helper()
	if result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("错误响应异常: %+v", result)
	}
	text := result.Content[0].Text
	index := strings.Index(text, "\n")
	if index < 0 {
		t.Fatalf("错误响应应带 next_step JSON: %s", text)
	}
	var payload struct {
		NextStep xiaohongshu.NextStep `json:"next_step"`
	}
	if err := json.Unmarshal([]byte(text[index+1:]), &payload); err != nil {
		t.Fatalf("next_step 解析失败: %v\n%s", err, text)
	}
	return payload.NextStep
}

// TestSessionErrorNextStepCarriesKnownArgs 错误响应里的 next_step 必须带已知参数，
// 让调用方直接照抄 args 就能发出下一次调用，而不是自己猜该调什么工具。
func TestSessionErrorNextStepCarriesKnownArgs(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		errText  string
		fallback xiaohongshu.NextStep
		wantTool string
		wantArgs map[string]string
		dropArgs []string
	}{
		{
			name:     "未打开笔记指向 open_note",
			prefix:   "打开笔记失败",
			errText:  "必须先打开笔记",
			fallback: sessionNextStepState("s-1"),
			wantTool: "open_note",
			wantArgs: map[string]string{"session_id": "s-1"},
		},
		{
			name:     "引用失效指向 search_feeds 并继承 keyword",
			prefix:   "打开笔记失败",
			errText:  "未找到搜索结果引用: 9",
			fallback: sessionNextStepSearch("露营"),
			wantTool: "search_feeds",
			wantArgs: map[string]string{"keyword": "露营"},
		},
		{
			name:     "缺少评论内容留在 comment_feed",
			prefix:   "评论失败",
			errText:  "缺少content参数",
			fallback: sessionNextStepCommentInput("s-2"),
			wantTool: "comment_feed",
			wantArgs: map[string]string{"session_id": "s-2"},
		},
		{
			name:     "缺少 comment_id 指向 get_note_detail",
			prefix:   "回复评论失败",
			errText:  "缺少comment_id或user_id参数",
			fallback: sessionNextStepState("s-3"),
			wantTool: "get_note_detail",
			wantArgs: map[string]string{"session_id": "s-3"},
		},
		{
			name:     "会话过期指向 start_page 且不带 session_id",
			prefix:   "页面状态获取失败",
			errText:  "browse session 不存在或已过期",
			fallback: sessionNextStepState("s-4"),
			wantTool: "start_page",
			dropArgs: []string{"session_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := sessionMCPErrorFromErr(tc.prefix, fmt.Errorf("%s", tc.errText), tc.fallback)
			next := parseSessionErrorNextStep(t, result)
			if next.Tool != tc.wantTool {
				t.Fatalf("next_step.tool = %q, 期望 %q", next.Tool, tc.wantTool)
			}
			if next.Reason == "" || next.Hint == "" {
				t.Fatalf("next_step 必须带 reason 和 hint: %+v", next)
			}
			for key, want := range tc.wantArgs {
				if got, ok := next.Args[key]; !ok || got != want {
					t.Fatalf("next_step.args[%s] = %v, 期望 %q（args=%v）", key, got, want, next.Args)
				}
			}
			for _, key := range tc.dropArgs {
				if _, ok := next.Args[key]; ok {
					t.Fatalf("next_step.args 不应包含 %s: %v", key, next.Args)
				}
			}
		})
	}
}

// TestSessionNextStepOmitsUnknownArgs 没有已知参数时不能写出空参数（MCP 参数会按 schema 校验）。
func TestSessionNextStepOmitsUnknownArgs(t *testing.T) {
	next := sessionNextStepState("")
	if next.Args != nil {
		t.Fatalf("无 session_id 时 args 应为空: %v", next.Args)
	}
	result := toolResultWithStep(map[string]string{"ok": "1"}, next, []string{"get_page_state"})
	if strings.Contains(result.Content[0].Text, `"args"`) {
		t.Fatalf("空 args 不应出现在 JSON 里: %s", result.Content[0].Text)
	}
}

// TestStartPageBlockedStepPointsBackToStartPage start_page 拿不到会话时只能重新调 start_page。
func TestStartPageBlockedStepPointsBackToStartPage(t *testing.T) {
	busy := &xiaohongshu.CreateBrowseSessionResult{
		Outcome: "blocked",
		Status:  xiaohongshu.BrowseSessionStatusInfo{Status: xiaohongshu.SessionBusy, LastError: "session 正在执行操作"},
	}
	step := startPageBlockedStep(context.Background(), busy)
	if step.Tool != "start_page" {
		t.Fatalf("busy 时 next_step.tool = %q, 期望 start_page", step.Tool)
	}
	if _, ok := step.Args["force_recreate"]; ok {
		t.Fatalf("busy 时不应建议 force_recreate: %v", step.Args)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	retry := startPageBlockedStep(cancelled, busy)
	if retry.Tool != "start_page" || retry.Hint == "" {
		t.Fatalf("取消时指引异常: %+v", retry)
	}

	unknown := &xiaohongshu.CreateBrowseSessionResult{
		Outcome: "blocked",
		Status:  xiaohongshu.BrowseSessionStatusInfo{Status: xiaohongshu.SessionUnhealthy, LastError: "renderer 不可用"},
	}
	recreate := startPageBlockedStep(context.Background(), unknown)
	if recreate.Args["force_recreate"] != true {
		t.Fatalf("不可复用时应带 force_recreate=true: %v", recreate.Args)
	}
	if !strings.Contains(recreate.Reason, "unhealthy") {
		t.Fatalf("reason 应说明具体状态: %s", recreate.Reason)
	}
}

// TestNextStepArgsMatchToolSchemas 防止指引里塞入工具 schema 不存在的参数
// （曾误写 get_note_detail 的 load_comments）。合法键空间来自 MCP 参数结构体的 json tag。
func TestNextStepArgsMatchToolSchemas(t *testing.T) {
	valid := map[string]bool{}
	samples := []any{
		CreateBrowseSessionArgs{}, BrowseSessionIDArgs{}, ListFeedsArgs{}, SessionSearchArgs{},
		SessionOpenNoteArgs{}, SessionDetailArgs{}, SessionLikeArgs{}, FavoriteFeedArgs{},
		SessionCommentArgs{}, ReplyCommentArgs{}, UnreadNotificationCountArgs{},
		ListNotificationsArgs{}, LikeNotificationArgs{}, ReplyNotificationArgs{},
	}
	for _, sample := range samples {
		rt := reflect.TypeOf(sample)
		for i := 0; i < rt.NumField(); i++ {
			name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				valid[name] = true
			}
		}
	}
	nextStepArgs := regexp.MustCompile(`NextStepArgs\(map\[string\]any\{([^}]*)\}`)
	argKey := regexp.MustCompile(`"([a-z_]+)":`)
	scanned := 0
	for _, file := range []string{"xiaohongshu/browse_session.go", "mcp_handlers.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", file, err)
		}
		for _, match := range nextStepArgs.FindAllStringSubmatch(string(data), -1) {
			for _, key := range argKey.FindAllStringSubmatch(match[1], -1) {
				scanned++
				if !valid[key[1]] {
					t.Fatalf("%s 里 next_step 参数 %q 不在任何 MCP 工具的 schema 中", file, key[1])
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("没有扫描到任何 next_step 参数，正则失配")
	}
}
