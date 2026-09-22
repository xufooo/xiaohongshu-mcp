package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// session 已关闭：没有 session 可用，只能重新开会话或检查登录。
var afterCloseTools = []string{"start_page", "check_login_status"}

// toolResult 是 session 工具的统一响应壳：数据 + 下一步该调用的工具 + 当前允许的工具。
type toolResult struct {
	Data           interface{}           `json:"data"`
	NextStep       *xiaohongshu.NextStep `json:"next_step,omitempty"`
	AvailableTools []string              `json:"available_tools,omitempty"`
}

// MCP 工具处理函数

func (s *AppServer) requireBrowserAvailableForMCP(name string) *MCPToolResult {
	if s.xiaohongshuService == nil {
		return nil
	}
	info, ok := s.xiaohongshuService.ActiveBrowseSessionInfo()
	if !ok {
		return nil
	}
	msg := fmt.Sprintf("browser busy - session active: session_id=%s expires_at=%s.",
		info.ID, info.ExpiresAt.Format(time.RFC3339))
	logrus.Warnf("MCP: %s blocked because browse session is active: %s", name, info.ID)
	// 引导回 session 工具：先看状态，或者关掉会话再重试当前工具。
	next := sessionNextStepState(info.ID)
	next.Hint = "先用 get_page_state 查看当前会话状态；不再需要该会话就 close_page 关闭后再重试 " + name
	return sessionMCPErrorResult(msg, next)
}

func (s *AppServer) requireWriteConfirmation(action, key, summary, token string) *MCPToolResult {
	if s.writeConfirm == nil || !s.writeConfirm.Enabled() {
		return nil
	}
	challenge, err := s.writeConfirm.Confirm(action, key, summary, token)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "写操作确认失败: " + err.Error()}},
			IsError: true,
		}
	}
	if challenge == nil {
		return nil
	}
	return jsonMCPResult(challenge, "写操作需要确认")
}

func compactWriteSummary(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 120 {
		return value
	}
	return value[:117] + "..."
}

type mcpSessionErrorPayload struct {
	Error    string               `json:"error"`
	NextStep xiaohongshu.NextStep `json:"next_step"`
}

func sessionMCPErrorResult(message string, next xiaohongshu.NextStep) *MCPToolResult {
	text := message
	if next.Tool != "" {
		payload := mcpSessionErrorPayload{Error: message, NextStep: next}
		if data, err := json.MarshalIndent(payload, "", "  "); err == nil {
			text = message + "\n" + string(data)
		} else {
			text = fmt.Sprintf("%s\nnext_step: %s", message, next.Tool)
		}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: text}}, IsError: true}
}

func sessionMCPErrorFromErr(prefix string, err error, fallback xiaohongshu.NextStep) *MCPToolResult {
	message := prefix
	errText := ""
	if err != nil {
		errText = err.Error()
		message += ": " + errText
	}
	return sessionMCPErrorResult(message, sessionNextStepForError(errText, fallback))
}

// sessionNextStepForError 由报错文本决定下一步该调用的工具。
// 已知参数从 fallback 继承，保证 next_step 里的 args 可以直接照抄调用。
func sessionNextStepForError(errText string, fallback xiaohongshu.NextStep) xiaohongshu.NextStep {
	var next xiaohongshu.NextStep
	switch {
	case strings.Contains(errText, "不存在或已过期"),
		strings.Contains(errText, "已过期"),
		strings.Contains(errText, "已关闭"):
		next = sessionNextStepCreateSession()
	case strings.Contains(errText, "未找到搜索结果引用"),
		strings.Contains(errText, "搜索结果参数无效"):
		next = sessionNextStepSearch("")
	case strings.Contains(errText, "必须先打开笔记"),
		strings.Contains(errText, "只能对已打开的笔记执行"),
		strings.Contains(errText, "只能对已阅读的笔记执行"):
		next = sessionNextStepOpenNote()
	case strings.Contains(errText, "comment_id"),
		strings.Contains(errText, "user_id"):
		next = sessionNextStepDetail()
	case strings.Contains(errText, "读取当前页面 URL"),
		strings.Contains(errText, "页面不存在"),
		strings.Contains(errText, "ready"),
		strings.Contains(errText, "selector"),
		strings.Contains(errText, "选择器"):
		next = sessionNextStepState("")
	default:
		next = fallback
	}
	return inheritSessionArgs(next, fallback)
}

// inheritSessionArgs 用 fallback 已知道的参数补齐 next 的 args：
// session_id 只补给真正接受它的工具（start_page 不接受），keyword 只补给 search_feeds。
func inheritSessionArgs(next, fallback xiaohongshu.NextStep) xiaohongshu.NextStep {
	args := map[string]any{}
	for key, value := range next.Args {
		args[key] = value
	}
	if next.Tool != "start_page" {
		if value, ok := fallback.Args["session_id"]; ok {
			if _, exists := args["session_id"]; !exists {
				args["session_id"] = value
			}
		}
	}
	if next.Tool == "search_feeds" {
		if value, ok := fallback.Args["keyword"]; ok {
			if _, exists := args["keyword"]; !exists {
				args["keyword"] = value
			}
		}
	}
	if len(args) > 0 {
		next.Args = args
	}
	return next
}

func sessionNextStepCreateSession() xiaohongshu.NextStep {
	return xiaohongshu.NextStep{
		Tool:   "start_page",
		Reason: "当前页面会话不可用或缺少 session_id",
		Hint:   "先调用 start_page 获取 session_id；会话状态异常时带 force_recreate=true 重建",
	}
}

func sessionNextStepState(sessionID string) xiaohongshu.NextStep {
	return xiaohongshu.NextStep{
		Tool:   "get_page_state",
		Args:   xiaohongshu.NextStepArgs(map[string]any{"session_id": sessionID}),
		Reason: "需要重新确认当前页面会话状态和可执行动作",
		Hint:   "读取 next_step、available_tools、results 后再决定下一步",
	}
}

func sessionNextStepSearch(keyword string) xiaohongshu.NextStep {
	return xiaohongshu.NextStep{
		Tool:   "search_feeds",
		Args:   xiaohongshu.NextStepArgs(map[string]any{"keyword": keyword}),
		Reason: "搜索结果引用不可用或已失效",
		Hint:   "重新搜索后使用 results 中最新的 result_ref 打开笔记",
	}
}

func sessionNextStepSearchInput(sessionID string) xiaohongshu.NextStep {
	return xiaohongshu.NextStep{
		Tool:   "search_feeds",
		Args:   xiaohongshu.NextStepArgs(map[string]any{"session_id": sessionID}),
		Reason: "缺少搜索关键词",
		Hint:   "补齐 keyword 参数后重新调用 search_feeds",
	}
}

func sessionNextStepOpenNote() xiaohongshu.NextStep {
	return xiaohongshu.NextStep{
		Tool:   "open_note",
		Reason: "当前页面会话还没有打开可操作的笔记",
		Hint:   "先从 get_page_state.results 中选择 result_ref 再调用 open_note",
	}
}

func sessionNextStepCommentInput(sessionID string) xiaohongshu.NextStep {
	return xiaohongshu.NextStep{
		Tool:   "comment_feed",
		Args:   xiaohongshu.NextStepArgs(map[string]any{"session_id": sessionID}),
		Reason: "缺少评论内容",
		Hint:   "补齐 content 参数后重新调用 comment_feed",
	}
}

func sessionNextStepDetail() xiaohongshu.NextStep {
	return xiaohongshu.NextStep{
		Tool:   "get_note_detail",
		Reason: "回复评论需要 comment_id 或 user_id",
		Hint:   "先 get_note_detail 读取当前笔记评论，用其中的 comment_id/user_id 再回复",
	}
}

// handleCheckLoginStatus 处理检查登录状态
func (s *AppServer) handleCheckLoginStatus(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 检查登录状态")
	if blocked := s.requireBrowserAvailableForMCP("检查登录状态"); blocked != nil {
		return blocked
	}

	status, err := s.xiaohongshuService.CheckLoginStatus(ctx)
	if err != nil {
		return sessionMCPErrorFromErr("检查登录状态失败", err, sessionNextStepCreateSession())
	}

	// 根据 IsLoggedIn 判断并返回友好的提示，并给出下一步该调用的工具
	if status.IsLoggedIn {
		return &MCPToolResult{Content: appendNextStep([]MCPContent{{
			Type: "text",
			Text: fmt.Sprintf("✅ 已登录\n用户名: %s", status.Username),
		}}, xiaohongshu.NextStep{
			Tool:   "start_page",
			Reason: "登录有效，可以创建页面会话",
			Hint:   "调用 start_page 拿 session_id 后即可搜索、打开笔记和互动",
		})}
	}
	return &MCPToolResult{Content: appendNextStep([]MCPContent{{
		Type: "text",
		Text: "❌ 未登录",
	}}, xiaohongshu.NextStep{
		Tool:   "get_login_qrcode",
		Reason: "未登录，除登录相关工具外其它工具都会失败",
		Hint:   "调用 get_login_qrcode 拿二维码，用小红书 App 扫码；扫码确认后再调 check_login_status",
	})}
}

// handleGetLoginQrcode 处理获取登录二维码请求。
// 返回二维码图片的 Base64 编码和超时时间，供前端展示扫码登录。
func (s *AppServer) handleGetLoginQrcode(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 获取登录扫码图片")
	if blocked := s.requireBrowserAvailableForMCP("获取登录扫码图片"); blocked != nil {
		return blocked
	}

	result, err := s.xiaohongshuService.GetLoginQrcode(ctx)
	if err != nil {
		return sessionMCPErrorFromErr("获取登录扫码图片失败", err, xiaohongshu.NextStep{
			Tool:   "check_login_status",
			Reason: "扫码图片获取失败，先确认浏览器与登录状态",
			Hint:   "调用 check_login_status 确认登录状态，再决定是否重试 get_login_qrcode",
		})
	}

	if result.IsLoggedIn {
		return &MCPToolResult{Content: appendNextStep([]MCPContent{{
			Type: "text",
			Text: "你当前已处于登录状态",
		}}, xiaohongshu.NextStep{
			Tool:   "start_page",
			Reason: "已登录，无需扫码",
			Hint:   "直接调用 start_page 创建页面会话",
		})}
	}

	now := time.Now()
	deadline := func() string {
		d, err := time.ParseDuration(result.Timeout)
		if err != nil {
			return now.Format("2006-01-02 15:04:05")
		}
		return now.Add(d).Format("2006-01-02 15:04:05")
	}()

	contents := appendNextStep([]MCPContent{
		{Type: "text", Text: "请用小红书 App 在 " + deadline + " 前扫码登录，并在手机上确认 👇"},
		{
			Type:     "image",
			MimeType: "image/png",
			Data:     strings.TrimPrefix(result.Img, "data:image/png;base64,"),
		},
	}, xiaohongshu.NextStep{
		Tool:   "check_login_status",
		Reason: "二维码已生成，等手机上确认登录",
		Hint:   "扫码并在手机端确认后调用 check_login_status；确认登录成功再调 start_page",
	})
	return &MCPToolResult{Content: contents}
}

// handleDeleteCookies 处理删除 cookies 请求，用于登录重置
func (s *AppServer) handleDeleteCookies(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 删除 cookies，重置登录状态")
	if blocked := s.requireBrowserAvailableForMCP("删除 cookies"); blocked != nil {
		return blocked
	}

	err := s.xiaohongshuService.DeleteCookies(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "删除 cookies 失败: " + err.Error()}},
			IsError: true,
		}
	}

	cookiePath := cookies.GetCookiesFilePath()
	resultText := fmt.Sprintf("Cookies 已成功删除，登录状态已重置。\n\n删除的文件路径: %s\n\n下次操作时，需要重新登录。", cookiePath)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handlePublishContent 处理发布内容
func (s *AppServer) handlePublishContent(ctx context.Context, args PublishContentArgs) *MCPToolResult {
	logrus.Info("MCP: 发布内容")

	title := args.Title
	content := args.Content
	imagePaths := args.Images
	tags := args.Tags
	products := args.Products
	scheduleAt := args.ScheduleAt
	visibility := args.Visibility
	isOriginal := args.IsOriginal
	confirmToken := args.ConfirmToken

	logrus.Infof("MCP: 发布内容 - 标题: %s, 图片数量: %d, 标签数量: %d, 定时: %s, 原创: %v, visibility: %s, 商品: %v", title, len(imagePaths), len(tags), scheduleAt, isOriginal, visibility, products)

	// 构建发布请求
	req := &PublishRequest{
		Title:      title,
		Content:    content,
		Images:     imagePaths,
		Tags:       tags,
		ScheduleAt: scheduleAt,
		IsOriginal: isOriginal,
		Visibility: visibility,
		Products:   products,
	}

	key := writeConfirmationKey("publish_content", title, content, imagePaths, tags, scheduleAt, isOriginal, visibility, products)
	summary := fmt.Sprintf("发布图文: title=%q images=%d visibility=%s content=%q", title, len(imagePaths), visibility, compactWriteSummary(content))
	if blocked := s.requireBrowserAvailableForMCP("发布内容"); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("publish_content", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	// 执行发布
	result, err := s.xiaohongshuService.PublishContent(ctx, req)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	resultText := fmt.Sprintf("内容发布成功: %+v", result)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handlePublishVideo 处理发布视频内容（仅本地单个视频文件）
func (s *AppServer) handlePublishVideo(ctx context.Context, args PublishVideoArgs) *MCPToolResult {
	logrus.Info("MCP: 发布视频内容（本地）")

	title := args.Title
	content := args.Content
	videoPath := args.Video
	tags := args.Tags
	products := args.Products

	if videoPath == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: 缺少本地视频文件路径",
			}},
			IsError: true,
		}
	}

	scheduleAt := args.ScheduleAt
	visibility := args.Visibility
	confirmToken := args.ConfirmToken

	logrus.Infof("MCP: 发布视频 - 标题: %s, 标签数量: %d, 定时: %s, visibility: %s, 商品: %v", title, len(tags), scheduleAt, visibility, products)

	// 构建发布请求
	req := &PublishVideoRequest{
		Title:      title,
		Content:    content,
		Video:      videoPath,
		Tags:       tags,
		ScheduleAt: scheduleAt,
		Visibility: visibility,
		Products:   products,
	}

	key := writeConfirmationKey("publish_video", title, content, videoPath, tags, scheduleAt, visibility, products)
	summary := fmt.Sprintf("发布视频: title=%q video=%q visibility=%s content=%q", title, videoPath, visibility, compactWriteSummary(content))
	if blocked := s.requireBrowserAvailableForMCP("发布视频"); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("publish_video", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	// 执行发布
	result, err := s.xiaohongshuService.PublishVideo(ctx, req)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	resultText := fmt.Sprintf("视频发布成功: %+v", result)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handleListFeeds 处理获取Feeds列表
func (s *AppServer) handleListFeeds(ctx context.Context, args ListFeedsArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("获取Feeds列表失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	logrus.Info("MCP: 获取Feeds列表", "session_id", args.SessionID)

	if args.MaxItems <= 0 {
		args.MaxItems = 20
	}
	result, err := s.xiaohongshuService.SessionListFeeds(ctx, args.SessionID, args.Cursor, args.MaxItems)
	if err != nil {
		return sessionMCPErrorFromErr("获取Feeds列表失败", err, sessionNextStepState(args.SessionID))
	}

	// 成功后直接包上 available_tools，避免序列化往返
	return s.sessionToolResult(args.SessionID, "list_feeds", result)
}

// handleUserProfile 获取用户主页
func (s *AppServer) handleUserProfile(ctx context.Context, args UserProfileArgs) *MCPToolResult {
	if blocked := s.requireBrowserAvailableForMCP("获取用户主页"); blocked != nil {
		return blocked
	}
	logrus.Info("MCP: 获取用户主页")

	userID := args.UserID
	if userID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: 缺少user_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken := args.XsecToken
	if xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 获取用户主页 - User ID: %s", userID)

	result, err := s.xiaohongshuService.UserProfile(ctx, userID, xsecToken)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 格式化输出，转换为JSON字符串
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("获取用户主页，但序列化失败: %v", err),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonData),
		}},
	}
}

// handleFavoriteFeed 处理收藏/取消收藏（session 语义）
func (s *AppServer) handleFavoriteFeed(ctx context.Context, args FavoriteFeedArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	if args.SessionID == "" {
		return sessionMCPErrorResult("收藏失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	action := "收藏"
	if args.Unfavorite {
		action = "取消收藏"
	}
	key := writeConfirmationKey("favorite_feed", args.SessionID, args.Unfavorite)
	summary := fmt.Sprintf("%s: session_id=%s", action, args.SessionID)
	if confirm := s.requireWriteConfirmation("favorite_feed", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	result, err := s.xiaohongshuService.SessionFavorite(ctx, args.SessionID, args.Unfavorite)
	if err != nil {
		return sessionMCPErrorFromErr(action+"失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "favorite_feed", result)
}

// handleReplyComment 处理回复评论（session 语义）
func (s *AppServer) handleReplyComment(ctx context.Context, args ReplyCommentArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	if args.SessionID == "" {
		return sessionMCPErrorResult("回复评论失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	args.CommentID = strings.TrimSpace(args.CommentID)
	args.UserID = strings.TrimSpace(args.UserID)
	args.Content = strings.TrimSpace(args.Content)
	if args.CommentID == "" && args.UserID == "" {
		return sessionMCPErrorResult("回复评论失败: 缺少comment_id或user_id参数", sessionNextStepState(args.SessionID))
	}
	if args.Content == "" {
		return sessionMCPErrorResult("回复评论失败: 缺少content参数", sessionNextStepState(args.SessionID))
	}
	key := writeConfirmationKey("reply_comment_in_feed", args.SessionID, args.CommentID, args.UserID, args.Content)
	summary := fmt.Sprintf("回复评论: session_id=%s comment_id=%s user_id=%s content=%q", args.SessionID, args.CommentID, args.UserID, compactWriteSummary(args.Content))
	if confirm := s.requireWriteConfirmation("reply_comment_in_feed", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	result, err := s.xiaohongshuService.SessionReply(ctx, args.SessionID, args.CommentID, args.UserID, args.Content)
	if err != nil {
		return sessionMCPErrorFromErr("回复评论失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "reply_comment_in_feed", result)
}

func (s *AppServer) handleCreateBrowseSession(ctx context.Context, args CreateBrowseSessionArgs) *MCPToolResult {
	logrus.Info("MCP: 创建页面会话 (start_page)")
	info, err := s.xiaohongshuService.CreateBrowseSession(ctx, args.ForceRecreate)
	if err != nil {
		return s.startPageErrorResult(ctx, err)
	}
	return s.startPageToolResult(ctx, info)
}

func (s *AppServer) handleCloseBrowseSession(ctx context.Context, args BrowseSessionIDArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("关闭浏览会话失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if err := s.xiaohongshuService.CloseBrowseSession(args.SessionID); err != nil {
		return sessionMCPErrorFromErr("关闭浏览会话失败", err, sessionNextStepCreateSession())
	}
	return toolResultWithStep(map[string]string{"closed_session_id": args.SessionID}, sessionNextStepCreateSession(), afterCloseTools)
}

func (s *AppServer) handleSessionState(ctx context.Context, args BrowseSessionIDArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("页面状态获取失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	state, err := s.xiaohongshuService.SessionState(ctx, args.SessionID)
	if err != nil {
		return sessionMCPErrorFromErr("页面状态获取失败", err, sessionNextStepCreateSession())
	}
	return jsonMCPResult(state, "页面状态获取成功")
}

func (s *AppServer) handleSessionSearch(ctx context.Context, args SessionSearchArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("搜索失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.Keyword == "" {
		return sessionMCPErrorResult("搜索失败: 缺少keyword参数", sessionNextStepSearchInput(args.SessionID))
	}
	filter := xiaohongshu.FilterOption{
		SortBy:      args.Filters.SortBy,
		NoteType:    args.Filters.NoteType,
		PublishTime: args.Filters.PublishTime,
		SearchScope: args.Filters.SearchScope,
		Location:    args.Filters.Location,
	}
	if args.MaxItems <= 0 {
		args.MaxItems = 20
	}
	result, err := s.xiaohongshuService.SessionSearch(ctx, args.SessionID, args.Keyword, args.Cursor, args.MaxItems, filter)
	if err != nil {
		return sessionMCPErrorFromErr("搜索失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "search_feeds", result)
}

func detailVisibilityDiagnosticSuffix(err error) string {
	var visibilityErr *xiaohongshu.DetailVisibilityError
	if errors.As(err, &visibilityErr) && visibilityErr != nil {
		if diagnostic := visibilityErr.Diagnostic(); diagnostic != "" {
			return "（" + diagnostic + "）"
		}
	}
	return ""
}

func shareURLOpenErrorStage(err error) string {
	if err == nil {
		return "未知错误"
	}
	if suffix := detailVisibilityDiagnosticSuffix(err); suffix != "" {
		return "详情可见性校验失败" + suffix
	}
	var urlPollErr *xiaohongshu.NoteURLPollError
	if errors.As(err, &urlPollErr) && urlPollErr != nil {
		if diagnostic := urlPollErr.Diagnostic(); diagnostic != "" {
			return "最终详情URL读取失败（" + diagnostic + "）"
		}
	}
	errText := err.Error()
	switch {
	case strings.Contains(errText, "share_url_source_url_read_failed"):
		return "导航阶段失败（source_url_read_failed）"
	case strings.Contains(errText, "share_url_navigate_error_not_navigation_error"):
		return "导航阶段失败（navigate_error_not_navigation_error）"
	case strings.Contains(errText, "share_url_navigation_rejected_detail_source"):
		return "导航阶段失败（navigation_rejected_detail_source）"
	case strings.HasPrefix(errText, "导航到share_url失败"), strings.HasPrefix(errText, "读取当前页面URL"):
		return "导航阶段失败"
	case strings.HasPrefix(errText, "等待笔记URL稳定"):
		return "等待最终详情URL失败"
	case strings.HasPrefix(errText, "等待笔记详情可见"):
		return "详情可见性校验失败"
	case strings.Contains(errText, "DOM Eval超时"):
		return "首屏内容读取失败（DOM Eval超时）"
	case strings.Contains(errText, "DOM Eval异常"):
		return "首屏内容读取失败（DOM Eval异常）"
	case strings.Contains(errText, "JSON解析异常"):
		return "首屏内容读取失败（快照JSON解析异常）"
	case strings.Contains(errText, "DOM快照返回为空"):
		return "首屏内容读取失败（DOM快照为空）"
	case strings.HasPrefix(errText, "提取打开笔记快照"), strings.HasPrefix(errText, "首屏内容读取阶段"), strings.HasPrefix(errText, "笔记已打开但内容未就绪"):
		return "首屏内容读取失败"
	case strings.HasPrefix(errText, "图片状态读取阶段"):
		return "图片状态读取失败"
	case strings.HasPrefix(errText, "笔记标题为空"):
		return "笔记标题读取失败"
	case strings.HasPrefix(errText, "笔记作者为空"):
		return "笔记作者读取失败"
	case strings.HasPrefix(errText, "最终note ID与预期不一致"), strings.HasPrefix(errText, "最终URL"):
		return "目标笔记校验失败"
	case strings.HasPrefix(errText, "URL"), strings.HasPrefix(errText, "share_url"), strings.HasPrefix(errText, "短链"):
		return "分享链接校验失败"
	default:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "操作被取消或超时"
		}
		return "执行阶段失败（未分类）"
	}
}

func (s *AppServer) handleSessionOpenNote(ctx context.Context, args SessionOpenNoteArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	args.ResultRef = strings.TrimSpace(args.ResultRef)
	args.ShareURL = strings.TrimSpace(args.ShareURL)
	args.XsecToken = strings.TrimSpace(args.XsecToken)
	if args.SessionID == "" {
		return sessionMCPErrorResult("打开笔记失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	hasResultRef := args.ResultRef != ""
	hasShareURL := args.ShareURL != ""
	if !hasResultRef && !hasShareURL {
		return sessionMCPErrorResult("打开笔记失败: result_ref与share_url必须且只能提供一个", sessionNextStepState(args.SessionID))
	}
	if hasResultRef && hasShareURL {
		return sessionMCPErrorResult("打开笔记失败: result_ref与share_url必须且只能提供一个", sessionNextStepState(args.SessionID))
	}
	if hasShareURL && args.XsecToken != "" {
		return sessionMCPErrorResult("打开笔记失败: share_url不能与xsec_token同时使用", sessionNextStepState(args.SessionID))
	}
	info, err := s.xiaohongshuService.SessionOpenNote(ctx, args.SessionID, args.ResultRef, args.ShareURL, args.XsecToken)
	if err != nil {
		if hasShareURL {
			return sessionMCPErrorResult("打开笔记失败: "+shareURLOpenErrorStage(err), sessionNextStepState(args.SessionID))
		}
		return sessionMCPErrorFromErr("打开笔记失败", fmt.Errorf("%w%s", err, detailVisibilityDiagnosticSuffix(err)), sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "open_note", info)
}

func (s *AppServer) handleSessionDetail(ctx context.Context, args SessionDetailArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	if args.SessionID == "" {
		return sessionMCPErrorResult("笔记详情获取失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.ReplyLimit != nil && *args.ReplyLimit < -1 {
		return sessionMCPErrorResult("分批加载评论失败: reply_limit不能小于-1（-1=不展开子评论，0=全部展开，正数=阈值过滤）", sessionNextStepOpenNote())
	}

	if args.MaxItems > 0 || args.Cursor != "" {
		maxItems := args.MaxItems
		if maxItems <= 0 {
			maxItems = 20
		}
		// RPi 性能实测：单轮 >50 条会超时（100 条在评论深处 DOM 大时 >300s MCP 超时）。
		// 保持 50 上限，大帖靠多轮续页读完（50×9 轮读完 434 评实测可行）。
		if maxItems > 50 {
			maxItems = 50
		}
		config := xiaohongshu.DefaultCommentLoadConfig()
		if args.ClickMoreReplies != nil {
			config.ClickMoreReplies = *args.ClickMoreReplies
		}
		if args.ReplyLimit != nil {
			config.MaxRepliesThreshold = *args.ReplyLimit
		}
		if args.ScrollSpeed != "" {
			config.ScrollSpeed = args.ScrollSpeed
		}
		result, err := s.xiaohongshuService.SessionDetailBatch(ctx, args.SessionID, args.Cursor, maxItems, config)
		if err != nil {
			return sessionMCPErrorFromErr("分批加载评论失败", err, sessionNextStepOpenNote())
		}
		return s.sessionToolResultPaged(args.SessionID, "get_note_detail", result, commentContinueStep(args, maxItems, config, result))
	}

	detail, err := s.xiaohongshuService.SessionDetail(ctx, args.SessionID, false, 0)
	if err != nil {
		return sessionMCPErrorFromErr("笔记详情获取失败", err, sessionNextStepOpenNote())
	}
	// 确保 list 不为 null
	if detail.Comments == nil {
		detail.Comments = []xiaohongshu.Comment{}
	}
	return s.sessionToolResult(args.SessionID, "get_note_detail", detail)
}

func (s *AppServer) handleSessionLike(ctx context.Context, args SessionLikeArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("点赞失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	action := "点赞"
	if args.Unlike {
		action = "取消点赞"
	}
	key := writeConfirmationKey("like_feed", args.SessionID, args.Unlike)
	summary := fmt.Sprintf("%s: session_id=%s", action, args.SessionID)
	if confirm := s.requireWriteConfirmation("like_feed", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	result, err := s.xiaohongshuService.SessionLike(ctx, args.SessionID, args.Unlike)
	if err != nil {
		return sessionMCPErrorFromErr("点赞失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "like_feed", result)
}

func (s *AppServer) handleSessionComment(ctx context.Context, args SessionCommentArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("评论失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.Content == "" {
		return sessionMCPErrorResult("评论失败: 缺少content参数", sessionNextStepCommentInput(args.SessionID))
	}
	key := writeConfirmationKey("comment_feed", args.SessionID, args.Content)
	summary := fmt.Sprintf("评论当前笔记: session_id=%s content=%q", args.SessionID, compactWriteSummary(args.Content))
	if confirm := s.requireWriteConfirmation("comment_feed", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	result, err := s.xiaohongshuService.SessionComment(ctx, args.SessionID, args.Content)
	if err != nil {
		return sessionMCPErrorFromErr("评论失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "comment_feed", result)
}

func (s *AppServer) handleSessionBack(ctx context.Context, args BrowseSessionIDArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("返回上一页失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	info, err := s.xiaohongshuService.SessionBack(ctx, args.SessionID)
	if err != nil {
		return sessionMCPErrorFromErr("返回上一页失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "go_back", info)
}

func (s *AppServer) handleGetUnreadCount(ctx context.Context, args UnreadNotificationCountArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("获取通知未读失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	count, err := s.xiaohongshuService.SessionUnreadNotificationCount(ctx, args.SessionID)
	if err != nil {
		return sessionMCPErrorFromErr("获取通知未读失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "get_unread_count", count)
}

func (s *AppServer) handleListNotifications(ctx context.Context, args ListNotificationsArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	args.Tab = strings.TrimSpace(args.Tab)
	args.Cursor = strings.TrimSpace(args.Cursor)
	if args.SessionID == "" {
		return sessionMCPErrorResult("通知列表失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.MaxItems <= 0 {
		args.MaxItems = 10
	}
	if args.MaxItems > 20 {
		args.MaxItems = 20
	}
	list, err := s.xiaohongshuService.SessionListNotifications(ctx, args.SessionID, args.Tab, args.Cursor, args.MaxItems)
	if err != nil {
		return sessionMCPErrorFromErr("通知列表失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "list_notifications", list)
}

func (s *AppServer) handleLikeNotification(ctx context.Context, args LikeNotificationArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	args.NotificationRef = strings.TrimSpace(args.NotificationRef)
	if args.SessionID == "" {
		return sessionMCPErrorResult("点赞通知失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.NotificationRef == "" {
		return sessionMCPErrorResult("点赞通知失败: 缺少notification_ref参数", sessionNextStepState(args.SessionID))
	}
	action := "点赞通知评论"
	if args.Unlike {
		action = "取消点赞通知评论"
	}
	key := writeConfirmationKey("like_notification", args.SessionID, args.NotificationRef, args.Unlike)
	summary := fmt.Sprintf("%s: session_id=%s notification_ref=%s", action, args.SessionID, args.NotificationRef)
	if confirm := s.requireWriteConfirmation("like_notification", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	result, err := s.xiaohongshuService.SessionLikeNotification(ctx, args.SessionID, args.NotificationRef, args.Unlike)
	if err != nil {
		return sessionMCPErrorFromErr("点赞通知失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "like_notification", result)
}

func (s *AppServer) handleReplyNotification(ctx context.Context, args ReplyNotificationArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	args.NotificationRef = strings.TrimSpace(args.NotificationRef)
	args.Content = strings.TrimSpace(args.Content)
	if args.SessionID == "" {
		return sessionMCPErrorResult("回复通知失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.NotificationRef == "" {
		return sessionMCPErrorResult("回复通知失败: 缺少notification_ref参数", sessionNextStepState(args.SessionID))
	}
	if args.Content == "" {
		return sessionMCPErrorResult("回复通知失败: 缺少content参数", sessionNextStepState(args.SessionID))
	}
	key := writeConfirmationKey("reply_notification", args.SessionID, args.NotificationRef, args.Content)
	summary := fmt.Sprintf("回复通知评论: session_id=%s notification_ref=%s content=%q",
		args.SessionID, args.NotificationRef, compactWriteSummary(args.Content))
	if confirm := s.requireWriteConfirmation("reply_notification", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	result, err := s.xiaohongshuService.SessionReplyNotification(ctx, args.SessionID, args.NotificationRef, args.Content)
	if err != nil {
		return sessionMCPErrorFromErr("回复通知失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "reply_notification", result)
}

func jsonMCPResult(value any, fallback string) *MCPToolResult {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fallback + "，但序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(data)}}}
}

// appendNextStep 在纯文本/图片响应后追加一段 {"next_step":{...}}，
// 让不返回 data 的工具（登录流程）也能给出下一步工具，且与其它响应用同一个键名。
func appendNextStep(contents []MCPContent, next xiaohongshu.NextStep) []MCPContent {
	if next.Tool == "" {
		return contents
	}
	payload := struct {
		NextStep xiaohongshu.NextStep `json:"next_step"`
	}{NextStep: next}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return contents
	}
	return append(contents, MCPContent{Type: "text", Text: string(data)})
}

// sessionToolResult 输出 {data, next_step, available_tools}；
// next_step 由会话已跟踪的状态推导（纯内存，不探测页面），所以不会推荐当前状态不允许的工具。
func (s *AppServer) sessionToolResult(sessionID, calledTool string, value any) *MCPToolResult {
	return s.sessionToolResultPaged(sessionID, calledTool, value, nil)
}

// sessionToolResultPaged 同上，但 cont 非 nil 时把"还有下一页"作为下一步（同一个工具 + 新 cursor）。
func (s *AppServer) sessionToolResultPaged(sessionID, calledTool string, value any, cont *xiaohongshu.NextStep) *MCPToolResult {
	guidance := s.xiaohongshuService.SessionGuidance(sessionID, calledTool, cont)
	next := xiaohongshu.NextStep{}
	if guidance.NextStep != nil {
		next = *guidance.NextStep
	}
	return toolResultWithStep(value, next, guidance.AvailableTools)
}

// continueStep 把"这一批还有下一页"翻译成下一步：同一个工具 + 同样的参数 + 新的 cursor。
// 只有调用方确认还有下一页（评论看 complete=false，列表看 has_more=true）且 cursor 非空时才给；
// 否则返回 nil，下一步仍由会话状态推导。
func continueStep(tool string, args map[string]any, cursor, reason string) *xiaohongshu.NextStep {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil
	}
	args["cursor"] = cursor
	return &xiaohongshu.NextStep{
		Tool:   tool,
		Args:   xiaohongshu.NextStepArgs(args),
		Reason: reason,
		Hint:   "带上返回的 cursor 再调用同一个工具，直到读完（评论 complete=true、列表 has_more=false）；参数要和本批保持一致",
	}
}

// commentContinueStep 判断这一批评论是否还没读完：complete=false 且有 cursor 就继续读同一篇笔记的下一批。
// 参数按本批**真正生效**的值回填（含从 cursor scope 继承来的值），否则续页会因参数不一致报 scope mismatch。
func commentContinueStep(args SessionDetailArgs, maxItems int, config xiaohongshu.CommentLoadConfig, result *FeedDetailResponse) *xiaohongshu.NextStep {
	detail, ok := result.Data.(*xiaohongshu.FeedDetailResponse)
	if !ok || detail == nil || detail.Comments.Complete {
		return nil
	}
	reason := "评论还没读完（complete=false），继续读下一批"
	if detail.Comments.IncompleteReason != "" {
		reason = "评论还没读完（complete=false，" + detail.Comments.IncompleteReason + "），继续读下一批"
	}
	return continueStep("get_note_detail", map[string]any{
		"session_id":         args.SessionID,
		"max_items":          maxItems,
		"click_more_replies": config.ClickMoreReplies,
		"reply_limit":        config.MaxRepliesThreshold,
		"scroll_speed":       config.ScrollSpeed,
	}, detail.Comments.Cursor, reason)
}

// handleSessionAISummary 读取当前搜索页的 AI 总结。
func (s *AppServer) handleSessionAISummary(ctx context.Context, args BrowseSessionIDArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("读取AI总结失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	result, err := s.xiaohongshuService.SessionAISummary(ctx, args.SessionID)
	if err != nil {
		return sessionMCPErrorFromErr("读取AI总结失败", err, sessionNextStepState(args.SessionID))
	}
	return s.sessionToolResult(args.SessionID, "get_ai_summary", result)
}

// startPageErrorResult 把 start_page 的失败翻译成下一步工具：
// 页面已加载但就绪判定失败时先读登录态（同页读取，不额外导航），未登录就直接引导扫码。
func (s *AppServer) startPageErrorResult(ctx context.Context, err error) *MCPToolResult {
	message := "创建页面会话失败 (start_page): " + err.Error()
	if strings.Contains(err.Error(), "等待探索页就绪失败") {
		if status, statusErr := s.xiaohongshuService.CheckLoginStatus(ctx); statusErr == nil && !status.IsLoggedIn {
			return sessionMCPErrorResult(message, xiaohongshu.NextStep{
				Tool:   "get_login_qrcode",
				Reason: "探索页已加载但处于未登录状态",
				Hint:   "先 get_login_qrcode 扫码登录，登录成功后再 start_page",
			})
		}
	}
	return sessionMCPErrorResult(message, startPageNextStep(err))
}

// startPageNextStep 用风险关键词表（与页面探测同一份）决定重建会话还是需要人工处理。
func startPageNextStep(err error) xiaohongshu.NextStep {
	text := ""
	if err != nil {
		text = err.Error()
	}
	switch xiaohongshu.RiskKindFromText(text) {
	case xiaohongshu.RiskLoginExpired:
		return xiaohongshu.NextStep{
			Tool:   "get_login_qrcode",
			Reason: "页面提示登录状态失效",
			Hint:   "先 get_login_qrcode 扫码登录，登录成功后再 start_page",
		}
	case xiaohongshu.RiskCaptcha, xiaohongshu.RiskSliderChallenge:
		return xiaohongshu.NextStep{
			Tool:   "start_page",
			Reason: "页面出现验证码或滑块验证",
			Hint:   "需要人工在浏览器窗口完成验证，完成后再重新 start_page",
		}
	case xiaohongshu.RiskAccessAnomaly:
		return xiaohongshu.NextStep{
			Tool:   "start_page",
			Reason: "访问异常或操作频繁",
			Hint:   "先等待一段时间再重新 start_page，不要连续重试",
		}
	}
	return xiaohongshu.NextStep{
		Tool:   "start_page",
		Args:   xiaohongshu.NextStepArgs(map[string]any{"force_recreate": true}),
		Reason: "会话创建失败",
		Hint:   "用 force_recreate=true 重建会话；若页面仍是登录页，先 get_login_qrcode",
	}
}

// startPageToolResult 拿到会话就按会话状态给指引；没拿到会话只能重新调 start_page。
func (s *AppServer) startPageToolResult(ctx context.Context, info *xiaohongshu.CreateBrowseSessionResult) *MCPToolResult {
	if info.Session != nil {
		return s.sessionToolResult(info.Session.ID, "start_page", info)
	}
	return toolResultWithStep(info, startPageBlockedStep(ctx, info), nil)
}

// startPageBlockedStep 在 start_page 拿不到可用会话时，给出重新调用 start_page 的最小参数。
func startPageBlockedStep(ctx context.Context, info *xiaohongshu.CreateBrowseSessionResult) xiaohongshu.NextStep {
	switch {
	case ctx.Err() != nil:
		return xiaohongshu.NextStep{
			Tool:   "start_page",
			Reason: "请求已取消或超时，会话未就绪",
			Hint:   "重新调用 start_page",
		}
	case info.Status.Status == xiaohongshu.SessionBusy:
		return xiaohongshu.NextStep{
			Tool:   "start_page",
			Reason: "会话正在执行其他操作，暂不可复用",
			Hint:   "等当前操作结束后重新调用 start_page",
		}
	default:
		step := xiaohongshu.NextStep{
			Tool:   "start_page",
			Args:   xiaohongshu.NextStepArgs(map[string]any{"force_recreate": true}),
			Reason: "会话状态不可复用",
			Hint:   "用 force_recreate=true 关闭旧会话并重建",
		}
		if info.Status.Status != "" {
			step.Reason = "会话状态不可复用: " + string(info.Status.Status)
		}
		if info.Status.LastError != "" {
			step.Hint += "（" + info.Status.LastError + "）"
		}
		return step
	}
}

// toolResultWithStep 输出 {data, next_step, available_tools}；next_step 为空时不写该字段。
func toolResultWithStep(value any, next xiaohongshu.NextStep, tools []string) *MCPToolResult {
	payload := toolResult{Data: value, AvailableTools: tools}
	if next.Tool != "" {
		payload.NextStep = &next
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作成功，但序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(data)}}}
}
