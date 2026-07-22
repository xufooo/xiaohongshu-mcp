package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/ratelimit"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// session 基础工具
var sessionBaseTools = []string{"close_browse_session"}
var sessionCreateTools = []string{"create_browse_session"}

// session 不同状态下的可用工具
var (
	afterCreateTools = append([]string{"session_search"}, sessionBaseTools...)
	afterSearchTools = append([]string{"session_open_note", "session_search"}, sessionBaseTools...)
	afterOpenTools   = append([]string{"session_read", "session_like", "session_comment", "session_detail", "session_back"}, sessionBaseTools...)
	afterReadTools   = append([]string{"session_detail", "session_like", "session_comment", "session_back"}, sessionBaseTools...)
	afterBackTools   = append([]string{"session_search", "session_open_note"}, sessionBaseTools...)
	afterCloseTools  = sessionCreateTools
)

// toolResult 包装响应数据，附上下一步可用工具列表
type toolResult struct {
	Data           interface{} `json:"data"`
	AvailableTools []string    `json:"available_tools"`
}
	"github.com/xpzouying/xiaohongshu-mcp/pkg/ratelimit"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// MCP 工具处理函数

// parseVisibility 从 MCP 参数中解析可见范围
func parseVisibility(args map[string]interface{}) string {
	v, ok := args["visibility"]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// rateLimitMCP MCP handler 速率限制检查。
func (s *AppServer) rateLimitMCP(ctx context.Context, name string, action ratelimit.Action) *MCPToolResult {
	r := s.checkRateLimitInternal(ctx, action)
	if !r.CanProceed {
		msg := r.Info.Warning
		if msg == "" {
			msg = "操作频率过高，请稍后重试"
		}
		logrus.Warnf("[ratelimit] ⚠️ [%s] 操作超限：%s", name, msg)
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("操作被限流: %s", msg),
			}},
			IsError: true,
		}
	}
	return nil
}

func (s *AppServer) requireBrowserAvailableForMCP(name string) *MCPToolResult {
	if s.xiaohongshuService == nil {
		return nil
	}
	info, ok := s.xiaohongshuService.ActiveBrowseSessionInfo()
	if !ok {
		return nil
	}
	msg := fmt.Sprintf("browser busy - session active: session_id=%s expires_at=%s. Use session_* tools or close_browse_session first.",
		info.ID, info.ExpiresAt.Format(time.RFC3339))
	logrus.Warnf("MCP: %s blocked because browse session is active: %s", name, info.ID)
	return &MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// requireBrowserForMCPWithFeed 检查浏览器可用性，但允许 feedID 匹配活跃 session 时通过
//（P2: 旧工具委托 session 式行为链）。
func (s *AppServer) requireBrowserForMCPWithFeed(name, feedID string) *MCPToolResult {
	if s.xiaohongshuService == nil {
		return nil
	}
	info, ok := s.xiaohongshuService.ActiveBrowseSessionInfo()
	if !ok {
		return nil
	}
	if info.CurrentFeedID != "" && info.CurrentFeedID == feedID {
		return nil
	}
	msg := fmt.Sprintf("browser busy - session active on different note: session_id=%s current_feed=%s. Use session tools or close_browse_session first.",
		info.ID, info.CurrentFeedID)
	logrus.Warnf("MCP: %s blocked (feed mismatch) session=%s target=%s", name, info.CurrentFeedID, feedID)
	return &MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: msg}},
		IsError: true,
	}
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

type mcpSessionNextStep struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
	Hint   string `json:"hint,omitempty"`
}

type mcpSessionErrorPayload struct {
	Error    string             `json:"error"`
	NextStep mcpSessionNextStep `json:"next_step"`
}

func sessionMCPErrorResult(message string, next mcpSessionNextStep) *MCPToolResult {
	text := message
	if next.Tool != "" {
		payload := mcpSessionErrorPayload{
			Error:    message,
			NextStep: next,
		}
		if data, err := json.MarshalIndent(payload, "", "  "); err == nil {
			text = message + "\n" + string(data)
		} else {
			text = fmt.Sprintf("%s\nnext_step: %s", message, next.Tool)
		}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: text}}, IsError: true}
}

func sessionMCPErrorFromErr(prefix string, err error, fallback mcpSessionNextStep) *MCPToolResult {
	message := prefix
	errText := ""
	if err != nil {
		errText = err.Error()
		message += ": " + errText
	}
	return sessionMCPErrorResult(message, sessionNextStepForError(errText, fallback))
}

func sessionNextStepForError(errText string, fallback mcpSessionNextStep) mcpSessionNextStep {
	switch {
	case strings.Contains(errText, "不存在或已过期"),
		strings.Contains(errText, "已过期"),
		strings.Contains(errText, "已关闭"):
		return sessionNextStepCreateSession()
	case strings.Contains(errText, "未找到搜索结果引用"),
		strings.Contains(errText, "搜索结果参数无效"):
		return sessionNextStepSearch()
	case strings.Contains(errText, "必须先打开笔记"),
		strings.Contains(errText, "只能对已打开的笔记执行"):
		return sessionNextStepOpenNote()
	case strings.Contains(errText, "只能对已阅读的笔记执行"):
		return sessionNextStepOpenNote()
	case strings.Contains(errText, "读取当前页面 URL"),
		strings.Contains(errText, "页面不存在"),
		strings.Contains(errText, "ready"),
		strings.Contains(errText, "selector"),
		strings.Contains(errText, "选择器"):
		return sessionNextStepState()
	default:
		return fallback
	}
}

func sessionNextStepCreateSession() mcpSessionNextStep {
	return mcpSessionNextStep{
		Tool:   "create_browse_session",
		Reason: "当前 session 不可用或缺少 session_id",
		Hint:   "先创建新的浏览会话，拿到 session_id 后继续使用 session_* 工具",
	}
}

func sessionNextStepState() mcpSessionNextStep {
	return mcpSessionNextStep{
		Tool:   "session_state",
		Reason: "需要重新确认当前 session 页面和可执行动作",
		Hint:   "读取 current、results、actions 和 timeline 后再决定下一步",
	}
}

func sessionNextStepSearch() mcpSessionNextStep {
	return mcpSessionNextStep{
		Tool:   "session_search",
		Reason: "搜索结果引用不可用或已失效",
		Hint:   "重新搜索后使用 results 中最新的 result_ref 打开笔记",
	}
}

func sessionNextStepSearchInput() mcpSessionNextStep {
	return mcpSessionNextStep{
		Tool:   "session_search",
		Reason: "缺少搜索关键词",
		Hint:   "提供 session_id 和 keyword 后重新搜索",
	}
}

func sessionNextStepOpenNote() mcpSessionNextStep {
	return mcpSessionNextStep{
		Tool:   "session_open_note",
		Reason: "当前 session 还没有打开可操作的笔记",
		Hint:   "先从 session_state.results 中选择 result_ref 打开笔记",
	}
}

func sessionNextStepCommentInput() mcpSessionNextStep {
	return mcpSessionNextStep{
		Tool:   "session_comment",
		Reason: "缺少评论内容",
		Hint:   "提供 content 后重新调用 session_comment",
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
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "检查登录状态失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 根据 IsLoggedIn 判断并返回友好的提示
	var resultText string
	if status.IsLoggedIn {
		resultText = fmt.Sprintf("✅ 已登录\n用户名: %s\n\n你可以使用其他功能了。", status.Username)
	} else {
		resultText = fmt.Sprintf("❌ 未登录\n\n请使用 get_login_qrcode 工具获取二维码进行登录。")
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
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
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "获取登录扫码图片失败: " + err.Error()}},
			IsError: true,
		}
	}

	if result.IsLoggedIn {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "你当前已处于登录状态"}},
		}
	}

	now := time.Now()
	deadline := func() string {
		d, err := time.ParseDuration(result.Timeout)
		if err != nil {
			return now.Format("2006-01-02 15:04:05")
		}
		return now.Add(d).Format("2006-01-02 15:04:05")
	}()

	// 已登录：文本 + 图片
	contents := []MCPContent{
		{Type: "text", Text: "请用小红书 App 在 " + deadline + " 前扫码登录 👇"},
		{
			Type:     "image",
			MimeType: "image/png",
			Data:     strings.TrimPrefix(result.Img, "data:image/png;base64,"),
		},
	}
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
func (s *AppServer) handlePublishContent(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 发布内容")

	// 解析参数
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	imagePathsInterface, _ := args["images"].([]interface{})
	tagsInterface, _ := args["tags"].([]interface{})
	productsInterface, _ := args["products"].([]interface{})

	var imagePaths []string
	for _, path := range imagePathsInterface {
		if pathStr, ok := path.(string); ok {
			imagePaths = append(imagePaths, pathStr)
		}
	}

	var tags []string
	for _, tag := range tagsInterface {
		if tagStr, ok := tag.(string); ok {
			tags = append(tags, tagStr)
		}
	}

	var products []string
	for _, p := range productsInterface {
		if pStr, ok := p.(string); ok {
			products = append(products, pStr)
		}
	}

	// 解析定时发布参数
	scheduleAt, _ := args["schedule_at"].(string)
	visibility := parseVisibility(args)

	// 解析原创参数
	isOriginal, _ := args["is_original"].(bool)

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

	confirmToken, _ := args["confirm_token"].(string)
	key := writeConfirmationKey("publish_content", title, content, imagePaths, tags, scheduleAt, isOriginal, visibility, products)
	summary := fmt.Sprintf("发布图文: title=%q images=%d visibility=%s content=%q", title, len(imagePaths), visibility, compactWriteSummary(content))
	if blocked := s.requireBrowserAvailableForMCP("发布内容"); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("publish_content", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, "发布内容", ratelimit.ActionPublish); blocked != nil {
		return blocked
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
func (s *AppServer) handlePublishVideo(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 发布视频内容（本地）")

	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	videoPath, _ := args["video"].(string)
	tagsInterface, _ := args["tags"].([]interface{})
	productsInterface, _ := args["products"].([]interface{})

	var tags []string
	for _, tag := range tagsInterface {
		if tagStr, ok := tag.(string); ok {
			tags = append(tags, tagStr)
		}
	}

	var products []string
	for _, p := range productsInterface {
		if pStr, ok := p.(string); ok {
			products = append(products, pStr)
		}
	}

	if videoPath == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: 缺少本地视频文件路径",
			}},
			IsError: true,
		}
	}

	// 解析定时发布参数
	scheduleAt, _ := args["schedule_at"].(string)
	visibility := parseVisibility(args)

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

	confirmToken, _ := args["confirm_token"].(string)
	key := writeConfirmationKey("publish_video", title, content, videoPath, tags, scheduleAt, visibility, products)
	summary := fmt.Sprintf("发布视频: title=%q video=%q visibility=%s content=%q", title, videoPath, visibility, compactWriteSummary(content))
	if blocked := s.requireBrowserAvailableForMCP("发布视频"); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("publish_video", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, "发布视频", ratelimit.ActionPublish); blocked != nil {
		return blocked
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
func (s *AppServer) handleListFeeds(ctx context.Context) *MCPToolResult {
	if blocked := s.requireBrowserAvailableForMCP("获取Feeds列表"); blocked != nil {
		return blocked
	}
	if blocked := s.rateLimitMCP(ctx, "获取Feeds列表", ratelimit.ActionBrowse); blocked != nil {
		return blocked
	}
	logrus.Info("MCP: 获取Feeds列表")

	result, err := s.xiaohongshuService.ListFeeds(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feeds列表失败: " + err.Error(),
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
				Text: fmt.Sprintf("获取Feeds列表成功，但序列化失败: %v", err),
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

// handleSearchFeeds 处理搜索Feeds
func (s *AppServer) handleSearchFeeds(ctx context.Context, args SearchFeedsArgs) *MCPToolResult {
	logrus.Info("MCP: 搜索Feeds")

	if blocked := s.requireBrowserAvailableForMCP("搜索Feeds"); blocked != nil {
		return blocked
	}
	if blocked := s.rateLimitMCP(ctx, "搜索Feeds", ratelimit.ActionSearch); blocked != nil {
		return blocked
	}

	if args.Keyword == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "搜索Feeds失败: 缺少关键词参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 搜索Feeds - 关键词: %s", args.Keyword)

	// 将 MCP 的 FilterOption 转换为 xiaohongshu.FilterOption
	filter := xiaohongshu.FilterOption{
		SortBy:      args.Filters.SortBy,
		NoteType:    args.Filters.NoteType,
		PublishTime: args.Filters.PublishTime,
		SearchScope: args.Filters.SearchScope,
		Location:    args.Filters.Location,
	}

	result, err := s.xiaohongshuService.SearchFeeds(ctx, args.Keyword, filter)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "搜索Feeds失败: " + err.Error(),
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
				Text: fmt.Sprintf("搜索Feeds成功，但序列化失败: %v", err),
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

// handleUserProfile 获取用户主页
func (s *AppServer) handleUserProfile(ctx context.Context, args map[string]any) *MCPToolResult {
	if blocked := s.requireBrowserAvailableForMCP("获取用户主页"); blocked != nil {
		return blocked
	}
	if blocked := s.rateLimitMCP(ctx, "获取用户主页", ratelimit.ActionBrowse); blocked != nil {
		return blocked
	}
	logrus.Info("MCP: 获取用户主页")

	// 解析参数
	userID, ok := args["user_id"].(string)
	if !ok || userID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: 缺少user_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
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

// handleLikeFeed 处理点赞/取消点赞
func (s *AppServer) handleLikeFeed(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	feedID, ok := args["feed_id"].(string)
	if !ok || strings.TrimSpace(feedID) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少feed_id参数"}}, IsError: true}
	}
	feedID = strings.TrimSpace(feedID)
	xsecToken, ok := args["xsec_token"].(string)
	if !ok || strings.TrimSpace(xsecToken) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少xsec_token参数"}}, IsError: true}
	}
	xsecToken = strings.TrimSpace(xsecToken)
	unlike, _ := args["unlike"].(bool)
	action := "点赞"
	if unlike {
		action = "取消点赞"
	}

	confirmToken, _ := args["confirm_token"].(string)
	key := writeConfirmationKey("like_feed", feedID, xsecToken, unlike)
	summary := fmt.Sprintf("%s: feed_id=%s", action, feedID)
	if blocked := s.requireBrowserForMCPWithFeed(action, feedID); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("like_feed", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, action, ratelimit.ActionLike); blocked != nil {
		return blocked
	}

	var res *ActionResult
	var err error

	if unlike {
		res, err = s.xiaohongshuService.UnlikeFeed(ctx, feedID, xsecToken)
	} else {
		res, err = s.xiaohongshuService.LikeFeed(ctx, feedID, xsecToken)
	}

	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: action + "失败: " + err.Error()}}, IsError: true}
	}

	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("%s成功 - Feed ID: %s", action, res.FeedID)}}}
}

// handleFavoriteFeed 处理收藏/取消收藏
func (s *AppServer) handleFavoriteFeed(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	feedID, ok := args["feed_id"].(string)
	if !ok || strings.TrimSpace(feedID) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少feed_id参数"}}, IsError: true}
	}
	feedID = strings.TrimSpace(feedID)
	xsecToken, ok := args["xsec_token"].(string)
	if !ok || strings.TrimSpace(xsecToken) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少xsec_token参数"}}, IsError: true}
	}
	xsecToken = strings.TrimSpace(xsecToken)
	unfavorite, _ := args["unfavorite"].(bool)
	action := "收藏"
	if unfavorite {
		action = "取消收藏"
	}

	confirmToken, _ := args["confirm_token"].(string)
	key := writeConfirmationKey("favorite_feed", feedID, xsecToken, unfavorite)
	summary := fmt.Sprintf("%s: feed_id=%s", action, feedID)
	if blocked := s.requireBrowserForMCPWithFeed(action, feedID); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("favorite_feed", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, action, ratelimit.ActionFavorite); blocked != nil {
		return blocked
	}

	var res *ActionResult
	var err error

	if unfavorite {
		res, err = s.xiaohongshuService.UnfavoriteFeed(ctx, feedID, xsecToken)
	} else {
		res, err = s.xiaohongshuService.FavoriteFeed(ctx, feedID, xsecToken)
	}

	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: action + "失败: " + err.Error()}}, IsError: true}
	}

	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("%s成功 - Feed ID: %s", action, res.FeedID)}}}
}

// handlePostComment 处理发表评论到Feed
func (s *AppServer) handlePostComment(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 发表评论到Feed")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || strings.TrimSpace(feedID) == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}
	feedID = strings.TrimSpace(feedID)

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || strings.TrimSpace(xsecToken) == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}
	xsecToken = strings.TrimSpace(xsecToken)

	content, ok := args["content"].(string)
	if !ok || strings.TrimSpace(content) == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少content参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 发表评论 - Feed ID: %s, 内容长度: %d", feedID, len(content))

	confirmToken, _ := args["confirm_token"].(string)
	key := writeConfirmationKey("post_comment", feedID, xsecToken, content)
	summary := fmt.Sprintf("发表评论: feed_id=%s content=%q", feedID, compactWriteSummary(content))
	if blocked := s.requireBrowserForMCPWithFeed("发表评论", feedID); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("post_comment", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, "发表评论", ratelimit.ActionComment); blocked != nil {
		return blocked
	}

	// 发表评论
	result, err := s.xiaohongshuService.PostCommentToFeed(ctx, feedID, xsecToken, content)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 返回成功结果，只包含feed_id
	resultText := fmt.Sprintf("评论发表成功 - Feed ID: %s", result.FeedID)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handleReplyComment 处理回复评论
func (s *AppServer) handleReplyComment(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 回复评论")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || strings.TrimSpace(feedID) == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}
	feedID = strings.TrimSpace(feedID)

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || strings.TrimSpace(xsecToken) == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}
	xsecToken = strings.TrimSpace(xsecToken)

	commentID, _ := args["comment_id"].(string)
	userID, _ := args["user_id"].(string)
	if commentID == "" && userID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少comment_id或user_id参数",
			}},
			IsError: true,
		}
	}

	content, ok := args["content"].(string)
	if !ok || strings.TrimSpace(content) == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少content参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 回复评论 - Feed ID: %s, Comment ID: %s, User ID: %s, 内容长度: %d", feedID, commentID, userID, len(content))

	confirmToken, _ := args["confirm_token"].(string)
	key := writeConfirmationKey("reply_comment", feedID, xsecToken, commentID, userID, content)
	summary := fmt.Sprintf("回复评论: feed_id=%s comment_id=%s user_id=%s content=%q", feedID, commentID, userID, compactWriteSummary(content))
	if blocked := s.requireBrowserForMCPWithFeed("回复评论", feedID); blocked != nil {
		return blocked
	}
	if confirm := s.requireWriteConfirmation("reply_comment", key, summary, confirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, "回复评论", ratelimit.ActionReply); blocked != nil {
		return blocked
	}

	// 回复评论
	result, err := s.xiaohongshuService.ReplyCommentToFeed(ctx, feedID, xsecToken, commentID, userID, content)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 返回成功结果
	responseText := fmt.Sprintf("评论回复成功 - Feed ID: %s, Comment ID: %s, User ID: %s", result.FeedID, result.TargetCommentID, result.TargetUserID)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: responseText,
		}},
	}
}

func (s *AppServer) handleCreateBrowseSession(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 创建浏览会话")
	if blocked := s.rateLimitMCP(ctx, "创建浏览会话", ratelimit.ActionBrowse); blocked != nil {
		return blocked
	}
	info, err := s.xiaohongshuService.CreateBrowseSession(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "创建浏览会话失败: " + err.Error()}},
			IsError: true,
		}
	}
	return jsonMCPResultWithTools(info, afterCreateTools)
}

func (s *AppServer) handleCloseBrowseSession(ctx context.Context, args BrowseSessionIDArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("关闭浏览会话失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if err := s.xiaohongshuService.CloseBrowseSession(args.SessionID); err != nil {
		return sessionMCPErrorFromErr("关闭浏览会话失败", err, sessionNextStepCreateSession())
	}
	return jsonMCPResultWithTools(map[string]string{"closed_session_id": args.SessionID}, afterCloseTools)
}

func (s *AppServer) handleSessionState(ctx context.Context, args BrowseSessionIDArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("session状态获取失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	state, err := s.xiaohongshuService.SessionState(ctx, args.SessionID)
	if err != nil {
		return sessionMCPErrorFromErr("session状态获取失败", err, sessionNextStepCreateSession())
	}
	return jsonMCPResult(state, "session状态获取成功")
}

func (s *AppServer) handleSessionSearch(ctx context.Context, args SessionSearchArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("session搜索失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.Keyword == "" {
		return sessionMCPErrorResult("session搜索失败: 缺少keyword参数", sessionNextStepSearchInput())
	}
	if blocked := s.rateLimitMCP(ctx, "session搜索", ratelimit.ActionSearch); blocked != nil {
		return blocked
	}
	filter := xiaohongshu.FilterOption{
		SortBy:      args.Filters.SortBy,
		NoteType:    args.Filters.NoteType,
		PublishTime: args.Filters.PublishTime,
		SearchScope: args.Filters.SearchScope,
		Location:    args.Filters.Location,
	}
	result, err := s.xiaohongshuService.SessionSearch(ctx, args.SessionID, args.Keyword, filter)
	if err != nil {
		return sessionMCPErrorFromErr("session搜索失败", err, sessionNextStepState())
	}
	return jsonMCPResultWithTools(result, afterSearchTools)
}

func (s *AppServer) handleSessionOpenNote(ctx context.Context, args SessionOpenNoteArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	args.ResultRef = strings.TrimSpace(args.ResultRef)
	args.XsecToken = strings.TrimSpace(args.XsecToken)
	if args.SessionID == "" {
		return sessionMCPErrorResult("session打开笔记失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.ResultRef == "" {
		return sessionMCPErrorResult("session打开笔记失败: 缺少result_ref参数", sessionNextStepState())
	}
	if blocked := s.rateLimitMCP(ctx, "session打开笔记", ratelimit.ActionOpenNote); blocked != nil {
		return blocked
	}
	info, err := s.xiaohongshuService.SessionOpenNote(ctx, args.SessionID, args.ResultRef, args.XsecToken)
	if err != nil {
		return sessionMCPErrorFromErr("session打开笔记失败", err, sessionNextStepState())
	}
	return jsonMCPResultWithTools(info, afterOpenTools)
}

func (s *AppServer) handleSessionDetail(ctx context.Context, args SessionDetailArgs) *MCPToolResult {
	args.SessionID = strings.TrimSpace(args.SessionID)
	if args.SessionID == "" {
		return sessionMCPErrorResult("session详情获取失败: 缺少session_id参数", sessionNextStepCreateSession())
	}

	if args.MaxItems > 0 || args.Cursor != "" {
		maxItems := args.MaxItems
		if maxItems <= 0 {
			maxItems = 20
		}
		if maxItems > 50 {
			maxItems = 50
		}
		config := xiaohongshu.DefaultCommentLoadConfig()
		if args.ClickMoreReplies != nil {
			config.ClickMoreReplies = *args.ClickMoreReplies
		}
		if args.ReplyLimit > 0 {
			config.MaxRepliesThreshold = args.ReplyLimit
		}
		if args.ScrollSpeed != "" {
			config.ScrollSpeed = args.ScrollSpeed
		}
		result, err := s.xiaohongshuService.SessionDetailBatch(ctx, args.SessionID, args.Cursor, maxItems, config)
		if err != nil {
			return sessionMCPErrorFromErr("session分批加载评论失败", err, sessionNextStepOpenNote())
		}
		return jsonMCPResultWithTools(result, afterOpenTools)
	}

	detail, err := s.xiaohongshuService.SessionDetail(ctx, args.SessionID, false, 0)
	if err != nil {
		return sessionMCPErrorFromErr("session详情获取失败", err, sessionNextStepOpenNote())
	}
	// 确保 list 不为 null
	if detail.Comments == nil {
		detail.Comments = []xiaohongshu.Comment{}
	}
	return jsonMCPResultWithTools(detail, afterOpenTools)
}

func (s *AppServer) handleSessionLike(ctx context.Context, args SessionLikeArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("session点赞失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	action := "session点赞"
	if args.Unlike {
		action = "session取消点赞"
	}
	key := writeConfirmationKey("session_like", args.SessionID, args.Unlike)
	summary := fmt.Sprintf("%s: session_id=%s", action, args.SessionID)
	if confirm := s.requireWriteConfirmation("session_like", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, action, ratelimit.ActionLike); blocked != nil {
		return blocked
	}
	result, err := s.xiaohongshuService.SessionLike(ctx, args.SessionID, args.Unlike)
	if err != nil {
		return sessionMCPErrorFromErr("session点赞失败", err, sessionNextStepState())
	}
	return jsonMCPResultWithTools(result, afterOpenTools)
}

func (s *AppServer) handleSessionComment(ctx context.Context, args SessionCommentArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("session评论失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	if args.Content == "" {
		return sessionMCPErrorResult("session评论失败: 缺少content参数", sessionNextStepCommentInput())
	}
	key := writeConfirmationKey("session_comment", args.SessionID, args.Content)
	summary := fmt.Sprintf("session评论: session_id=%s content=%q", args.SessionID, compactWriteSummary(args.Content))
	if confirm := s.requireWriteConfirmation("session_comment", key, summary, args.ConfirmToken); confirm != nil {
		return confirm
	}
	if blocked := s.rateLimitMCP(ctx, "session评论", ratelimit.ActionComment); blocked != nil {
		return blocked
	}
	result, err := s.xiaohongshuService.SessionComment(ctx, args.SessionID, args.Content)
	if err != nil {
		return sessionMCPErrorFromErr("session评论失败", err, sessionNextStepState())
	}
	return jsonMCPResultWithTools(result, afterOpenTools)
}

func (s *AppServer) handleSessionBack(ctx context.Context, args BrowseSessionIDArgs) *MCPToolResult {
	if args.SessionID == "" {
		return sessionMCPErrorResult("session返回失败: 缺少session_id参数", sessionNextStepCreateSession())
	}
	info, err := s.xiaohongshuService.SessionBack(ctx, args.SessionID)
	if err != nil {
		return sessionMCPErrorFromErr("session返回失败", err, sessionNextStepState())
	}
	return jsonMCPResultWithTools(info, afterBackTools)
}

func jsonMCPResult(value any, fallback string) *MCPToolResult {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fallback + "，但序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(data)}}}
}

// jsonMCPResultWithTools 在返回数据中附带下一步可用工具列表
func jsonMCPResultWithTools(value any, tools []string) *MCPToolResult {
	data, err := json.MarshalIndent(toolResult{Data: value, AvailableTools: tools}, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作成功，但序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(data)}}}
}
