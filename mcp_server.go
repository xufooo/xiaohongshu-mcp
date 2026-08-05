package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/ratelimit"
)

// readActionForTool 读工具 → 限流 action（仅限实际页面/网络操作；状态读取与资源清理不限流）。
func readActionForTool(toolName string) (ratelimit.Action, bool) {
	switch toolName {
	case "list_feeds", "go_back", "start_page", "list_notifications", "get_unread_count":
		return ratelimit.ActionBrowse, true
	case "search_feeds":
		return ratelimit.ActionSearch, true
	case "open_note", "get_note_detail", "user_profile":
		return ratelimit.ActionOpenNote, true
	}
	return "", false
}

// Helper functions for annotation pointers
func boolPtr(b bool) *bool { return &b }

// MCP 工具参数结构体定义

// PublishContentArgs 发布内容的参数
type PublishContentArgs struct {
	Title        string   `json:"title" jsonschema:"内容标题（小红书限制：最多20个中文字或英文单词）"`
	Content      string   `json:"content" jsonschema:"正文内容，不包含以#开头的标签内容，所有话题标签都用tags参数来生成和提供即可"`
	Images       []string `json:"images" jsonschema:"图片路径列表（至少需要1张图片）。支持两种方式：1. HTTP/HTTPS图片链接（自动下载）；2. 本地图片绝对路径（推荐，如:/Users/user/image.jpg）"`
	Tags         []string `json:"tags,omitempty" jsonschema:"话题标签列表（可选参数），如 [美食, 旅行, 生活]"`
	ScheduleAt   string   `json:"schedule_at,omitempty" jsonschema:"定时发布时间（可选），ISO8601格式如 2024-01-20T10:30:00+08:00，支持1小时至14天内。不填则立即发布"`
	IsOriginal   bool     `json:"is_original,omitempty" jsonschema:"是否声明原创（可选），true为声明原创，false或不填则不声明"`
	Visibility   string   `json:"visibility,omitempty" jsonschema:"可见范围（可选），支持: 公开可见(默认)、仅自己可见、仅互关好友可见。不填则默认公开可见"`
	Products     []string `json:"products,omitempty" jsonschema:"商品关键词列表（可选），用于绑定带货商品。填写商品名称或商品ID，系统会自动搜索并选择第一个匹配结果。需账号已开通商品功能。示例: [面膜, 防晒霜SPF50]"`
	ConfirmToken string   `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// PublishVideoArgs 发布视频的参数（仅支持本地单个视频文件）
type PublishVideoArgs struct {
	Title        string   `json:"title" jsonschema:"内容标题（小红书限制：最多20个中文字或英文单词）"`
	Content      string   `json:"content" jsonschema:"正文内容，不包含以#开头的标签内容，所有话题标签都用tags参数来生成和提供即可"`
	Video        string   `json:"video" jsonschema:"本地视频绝对路径（仅支持单个视频文件，如:/Users/user/video.mp4）"`
	Tags         []string `json:"tags,omitempty" jsonschema:"话题标签列表（可选参数），如 [美食, 旅行, 生活]"`
	ScheduleAt   string   `json:"schedule_at,omitempty" jsonschema:"定时发布时间（可选），ISO8601格式如 2024-01-20T10:30:00+08:00，支持1小时至14天内。不填则立即发布"`
	Visibility   string   `json:"visibility,omitempty" jsonschema:"可见范围（可选），支持: 公开可见(默认)、仅自己可见、仅互关好友可见。不填则默认公开可见"`
	Products     []string `json:"products,omitempty" jsonschema:"商品关键词列表（可选），用于绑定带货商品。填写商品名称或商品ID，系统会自动搜索并选择第一个匹配结果。需账号已开通商品功能。示例: [面膜, 防晒霜SPF50]"`
	ConfirmToken string   `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// UserProfileArgs 获取用户主页的参数
type UserProfileArgs struct {
	UserID    string `json:"user_id" jsonschema:"小红书用户ID，从Feed列表获取"`
	XsecToken string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
}

// ReplyCommentArgs 回复评论的参数
type ReplyCommentArgs struct {
	SessionID    string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	CommentID    string `json:"comment_id,omitempty" jsonschema:"目标评论ID，从评论列表获取"`
	UserID       string `json:"user_id,omitempty" jsonschema:"目标评论用户ID，从评论列表获取"`
	Content      string `json:"content" jsonschema:"回复内容"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// FavoriteFeedArgs 收藏参数
type FavoriteFeedArgs struct {
	SessionID    string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	Unfavorite   bool   `json:"unfavorite,omitempty" jsonschema:"是否取消收藏，true为取消收藏，false或未设置则为收藏"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

type BrowseSessionIDArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
}

type CreateBrowseSessionArgs struct {
	ForceRecreate bool `json:"force_recreate,omitempty"`
}

type ListFeedsArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	MaxItems  int    `json:"max_items,omitempty" jsonschema:"可选，本批最多返回数量，默认20，最大50"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"可选，继续滚动时传上次 list_feeds 返回的 cursor"`
}

type SessionDetailArgs struct {
	SessionID        string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	MaxItems         int    `json:"max_items,omitempty" jsonschema:"可选，分批加载每批最多返回数量，默认20，最大50；不传或传0则仅返回当前可见评论"`
	Cursor           string `json:"cursor,omitempty" jsonschema:"可选，分批加载游标，由上次 get_note_detail 返回的 cursor 字段提供"`
	ClickMoreReplies *bool  `json:"click_more_replies,omitempty" jsonschema:"可选，是否自动点击展开子评论（二级回复），默认false"`
	ReplyLimit       *int   `json:"reply_limit,omitempty" jsonschema:"可选，子评论展开阈值，默认10；0表示不限制，正数表示回复数超过此值的评论不展开"`
	ScrollSpeed      string `json:"scroll_speed,omitempty" jsonschema:"可选，滚动速度: slow|normal|fast，默认fast"`
}

type SessionSearchArgs struct {
	SessionID string       `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	Keyword   string       `json:"keyword" jsonschema:"搜索关键词；续页时必须与首次调用相同"`
	Filters   FilterOption `json:"filters,omitempty" jsonschema:"筛选选项；续页时必须与首次调用相同"`
	MaxItems  int          `json:"max_items,omitempty" jsonschema:"可选，本批最多返回数量，默认20，最大50"`
	Cursor    string       `json:"cursor,omitempty" jsonschema:"可选，继续滚动时传上次 search_feeds 返回的 cursor"`
}

type SessionOpenNoteArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	ResultRef string `json:"result_ref" jsonschema:"搜索结果引用。可传搜索结果的index或feed_id"`
	XsecToken string `json:"xsec_token,omitempty" jsonschema:"访问令牌。通常可省略，session会使用搜索结果里的xsecToken"`
}

type SessionLikeArgs struct {
	SessionID    string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	Unlike       bool   `json:"unlike,omitempty" jsonschema:"是否取消点赞，true为取消点赞，false或未设置则为点赞"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

type SessionCommentArgs struct {
	SessionID    string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	Content      string `json:"content" jsonschema:"评论内容"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

type UnreadNotificationCountArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
}

type ListNotificationsArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	Tab       string `json:"tab,omitempty" jsonschema:"可选，通知tab: mentions(评论和@，默认)|likes(赞和收藏)|connections(新增关注)"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"可选，续页时传上次 list_notifications 返回的 cursor；切 tab 或传空则重新读取首批"`
	MaxItems  int    `json:"max_items,omitempty" jsonschema:"可选，本批最多返回数量，默认10，最大20"`
}

type LikeNotificationArgs struct {
	SessionID       string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	NotificationRef string `json:"notification_ref" jsonschema:"通知条目引用，由 list_notifications 返回的 notification_ref 提供"`
	Unlike          bool   `json:"unlike,omitempty" jsonschema:"是否取消点赞，true为取消点赞，false或未设置则为点赞"`
	ConfirmToken    string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

type ReplyNotificationArgs struct {
	SessionID       string `json:"session_id" jsonschema:"浏览会话ID，由start_page返回"`
	NotificationRef string `json:"notification_ref" jsonschema:"通知条目引用，由 list_notifications 返回的 notification_ref 提供"`
	Content         string `json:"content" jsonschema:"回复内容"`
	ConfirmToken    string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// InitMCPServer 初始化 MCP Server
func InitMCPServer(appServer *AppServer) *mcp.Server {
	// 创建 MCP Server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "xiaohongshu-mcp",
			Version: "2.0.0",
		},
		nil,
	)

	// 注册所有工具
	registerTools(server, appServer)

	logrus.Info("MCP Server initialized with official SDK")

	return server
}

func withPanicRecovery[T any](
	toolName string,
	limiter *ratelimit.Limiter,
	handler func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {

	return func(ctx context.Context, req *mcp.CallToolRequest, args T) (result *mcp.CallToolResult, resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logrus.WithFields(logrus.Fields{
					"tool":  toolName,
					"panic": r,
				}).Error("Tool handler panicked")

				logrus.Errorf("Stack trace:\n%s", debug.Stack())

				result = &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf("工具 %s 执行时发生内部错误: %v\n\n请查看服务端日志获取详细信息。", toolName, r),
						},
					},
					IsError: true,
				}
				resp = nil
				err = nil
			}
		}()

		// 读工具限流；写工具在 handler 内 confirm_token 验证后接入（避免确认挑战消耗额度）。
		if action, ok := readActionForTool(toolName); ok {
			if res := checkRateLimitWithLimiter(ctx, limiter, action); !res.CanProceed {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "操作超限，请稍后再试: " + res.Info.Warning}},
					IsError: true,
				}, nil, nil
			}
		}

		return handler(ctx, req, args)
	}
}

func registerTools(server *mcp.Server, appServer *AppServer) {
	// 工具 1: 检查登录状态
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "check_login_status",
			Description: "检查小红书登录状态",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Check Login Status",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("check_login_status", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			result := appServer.handleCheckLoginStatus(ctx)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 2: 获取登录二维码
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_login_qrcode",
			Description: "获取登录二维码（返回 Base64 图片和超时时间）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Get Login QR Code",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("get_login_qrcode", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			result := appServer.handleGetLoginQrcode(ctx)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 3: 删除 cookies（登录重置）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_cookies",
			Description: "删除 cookies 文件，重置登录状态。删除后需要重新登录。",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Delete Cookies",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("delete_cookies", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			result := appServer.handleDeleteCookies(ctx)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 4: 发布内容
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "publish_content",
			Description: "发布小红书图文内容",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Publish Content",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("publish_content", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args PublishContentArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handlePublishContent(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 5: 获取Feed列表（需要 session_id）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_feeds",
			Description: "获取首页 Feeds 列表（需要 session_id）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "List Feeds",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("list_feeds", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args ListFeedsArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleListFeeds(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 6: 搜索内容
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_feeds",
			Description: "在保留页面中通过真实UI搜索小红书内容（需要 session_id），返回的 result_ref 可供 open_note 使用",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Search Feeds",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("search_feeds", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args SessionSearchArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionSearch(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 8: 获取用户主页
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "user_profile",
			Description: "获取指定的小红书用户主页，返回用户基本信息，关注、粉丝、获赞量及其笔记内容",
			Annotations: &mcp.ToolAnnotations{
				Title:        "User Profile",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("user_profile", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args UserProfileArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleUserProfile(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 9: 发表评论
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "comment_feed",
			Description: "评论当前 session 已打开且已阅读的笔记",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Comment Feed",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("comment_feed", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args SessionCommentArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionComment(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 10: 回复评论
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "reply_comment_in_feed",
			Description: "回复当前 session 笔记中的目标评论",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Reply Comment",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("reply_comment_in_feed", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args ReplyCommentArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleReplyComment(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 11: 发布视频（仅本地文件）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "publish_with_video",
			Description: "发布小红书视频内容（仅支持本地单个视频文件）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Publish Video",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("publish_with_video", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args PublishVideoArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handlePublishVideo(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 12: 点赞笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "like_feed",
			Description: "点赞或取消点赞当前 session 笔记（如已点赞将跳过点赞，如未点赞将跳过取消点赞）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Like Feed",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("like_feed", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args SessionLikeArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionLike(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 13: 收藏笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "favorite_feed",
			Description: "收藏或取消收藏当前 session 笔记（如已收藏将跳过收藏，如未收藏将跳过取消收藏）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Favorite Feed",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("favorite_feed", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args FavoriteFeedArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleFavoriteFeed(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 14: 创建页面会话
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "start_page",
			Description: "创建一个保留同一浏览器页面的页面会话（start_page），用于连续执行搜索、打开、阅读、互动和返回",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Start Page",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("start_page", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args CreateBrowseSessionArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleCreateBrowseSession(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 15: 页面状态
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_page_state",
			Description: "获取页面会话的紧凑页面状态，包括当前URL、页面类型、就绪状态、风险信号和可执行的下一步动作",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Get Page State",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("get_page_state", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args BrowseSessionIDArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionState(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 17: 打开笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "open_note",
			Description: "在页面会话内从搜索结果卡片点击打开笔记，并直接返回首屏标题和正文。result_ref可传搜索结果index或feed_id，xsec_token可选",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Open Note",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("open_note", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args SessionOpenNoteArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionOpenNote(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 18: 笔记详情
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_note_detail",
			Description: "在页面会话当前已打开的笔记页面上继续读取当前可见评论。传 max_items 和 cursor 可分批加载更多评论（去重、支持子评论展开）。笔记首屏标题和正文已由 open_note 返回；图片、视频读取暂未实现。",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Get Note Detail",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("get_note_detail", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args SessionDetailArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionDetail(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 22: 后退（通用）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "go_back",
			Description: "在页面会话内后退到上一页（支持任意页面：笔记详情、作者主页等）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Go Back",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("go_back", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args BrowseSessionIDArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionBack(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 23: 关闭页面会话
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "close_page",
			Description: "关闭页面会话并释放浏览器页面",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Close Page",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("close_page", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args BrowseSessionIDArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleCloseBrowseSession(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 24: 通知未读数
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_unread_count",
			Description: "读取通知未读数（只读，不点击通知入口，不清除未读）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Get Unread Count",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("get_unread_count", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args UnreadNotificationCountArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleGetUnreadCount(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 25: 通知列表
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_notifications",
			Description: "在页面会话中通过侧栏通知入口进入通知页并读取通知列表（可切换tab、cursor续页）。进入通知页或切tab会使对应未读被小红书清除。返回的 notification_ref 供 like_notification/reply_notification 使用。仅 mentions tab 的条目可写。",
			Annotations: &mcp.ToolAnnotations{
				Title: "List Notifications",
			},
		},
		withPanicRecovery("list_notifications", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args ListNotificationsArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleListNotifications(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 26: 点赞通知
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "like_notification",
			Description: "点赞或取消点赞通知中的评论（幂等，如已点赞将跳过点赞，如未点赞将跳过取消点赞）。仅支持 list_notifications 中 mentions tab 的条目，且必须使用其 notification_ref。",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Like Notification",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("like_notification", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args LikeNotificationArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleLikeNotification(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 27: 回复通知
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "reply_notification",
			Description: "回复通知中的评论。仅支持 list_notifications 中 mentions tab 的条目，且必须使用其 notification_ref。",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Reply Notification",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("reply_notification", appServer.rateLimiter, func(ctx context.Context, req *mcp.CallToolRequest, args ReplyNotificationArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleReplyNotification(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	logrus.Infof("Registered %d MCP tools", 22)
}

// convertToMCPResult 将自定义的 MCPToolResult 转换为官方 SDK 的格式
func convertToMCPResult(result *MCPToolResult) *mcp.CallToolResult {
	var contents []mcp.Content
	for _, c := range result.Content {
		switch c.Type {
		case "text":
			contents = append(contents, &mcp.TextContent{Text: c.Text})
		case "image":
			// 解码 base64 字符串为 []byte
			imageData, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil {
				logrus.WithError(err).Error("Failed to decode base64 image data")
				// 如果解码失败，添加错误文本
				contents = append(contents, &mcp.TextContent{
					Text: "图片数据解码失败: " + err.Error(),
				})
			} else {
				contents = append(contents, &mcp.ImageContent{
					Data:     imageData,
					MIMEType: c.MimeType,
				})
			}
		}
	}

	return &mcp.CallToolResult{
		Content: contents,
		IsError: result.IsError,
	}
}
