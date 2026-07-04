package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
)

// Helper functions for annotation pointers
func boolPtr(b bool) *bool { return &b }

// MCP 工具参数结构体定义

// PublishContentArgs 发布内容的参数
type PublishContentArgs struct {
	Title      string   `json:"title" jsonschema:"内容标题（小红书限制：最多20个中文字或英文单词）"`
	Content    string   `json:"content" jsonschema:"正文内容，不包含以#开头的标签内容，所有话题标签都用tags参数来生成和提供即可"`
	Images     []string `json:"images" jsonschema:"图片路径列表（至少需要1张图片）。支持两种方式：1. HTTP/HTTPS图片链接（自动下载）；2. 本地图片绝对路径（推荐，如:/Users/user/image.jpg）"`
	Tags       []string `json:"tags,omitempty" jsonschema:"话题标签列表（可选参数），如 [美食, 旅行, 生活]"`
	ScheduleAt string   `json:"schedule_at,omitempty" jsonschema:"定时发布时间（可选），ISO8601格式如 2024-01-20T10:30:00+08:00，支持1小时至14天内。不填则立即发布"`
	IsOriginal bool     `json:"is_original,omitempty" jsonschema:"是否声明原创（可选），true为声明原创，false或不填则不声明"`
	Visibility string   `json:"visibility,omitempty" jsonschema:"可见范围（可选），支持: 公开可见(默认)、仅自己可见、仅互关好友可见。不填则默认公开可见"`
	Products     []string `json:"products,omitempty" jsonschema:"商品关键词列表（可选），用于绑定带货商品。填写商品名称或商品ID，系统会自动搜索并选择第一个匹配结果。需账号已开通商品功能。示例: [面膜, 防晒霜SPF50]"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// PublishVideoArgs 发布视频的参数（仅支持本地单个视频文件）
type PublishVideoArgs struct {
	Title      string   `json:"title" jsonschema:"内容标题（小红书限制：最多20个中文字或英文单词）"`
	Content    string   `json:"content" jsonschema:"正文内容，不包含以#开头的标签内容，所有话题标签都用tags参数来生成和提供即可"`
	Video      string   `json:"video" jsonschema:"本地视频绝对路径（仅支持单个视频文件，如:/Users/user/video.mp4）"`
	Tags       []string `json:"tags,omitempty" jsonschema:"话题标签列表（可选参数），如 [美食, 旅行, 生活]"`
	ScheduleAt string   `json:"schedule_at,omitempty" jsonschema:"定时发布时间（可选），ISO8601格式如 2024-01-20T10:30:00+08:00，支持1小时至14天内。不填则立即发布"`
	Visibility string   `json:"visibility,omitempty" jsonschema:"可见范围（可选），支持: 公开可见(默认)、仅自己可见、仅互关好友可见。不填则默认公开可见"`
	Products     []string `json:"products,omitempty" jsonschema:"商品关键词列表（可选），用于绑定带货商品。填写商品名称或商品ID，系统会自动搜索并选择第一个匹配结果。需账号已开通商品功能。示例: [面膜, 防晒霜SPF50]"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// SearchFeedsArgs 搜索内容的参数
type SearchFeedsArgs struct {
	Keyword string       `json:"keyword" jsonschema:"搜索关键词"`
	Filters FilterOption `json:"filters,omitempty" jsonschema:"筛选选项"`
}

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// FeedDetailArgs 获取Feed详情的参数
type FeedDetailArgs struct {
	FeedID           string `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken        string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	LoadAllComments  bool   `json:"load_all_comments,omitempty" jsonschema:"是否加载全部评论。false仅返回前10条一级评论（默认），true滚动加载更多评论"`
	Limit            int    `json:"limit,omitempty" jsonschema:"【仅当load_all_comments为true时生效】限制加载的一级评论数量。例如20表示最多加载20条；不传或传0表示加载所有"`
	ClickMoreReplies bool   `json:"click_more_replies,omitempty" jsonschema:"【仅当load_all_comments为true时生效】是否展开二级回复。true展开子评论，false不展开（默认）"`
	ReplyLimit       int    `json:"reply_limit,omitempty" jsonschema:"【仅当click_more_replies为true时生效】跳过回复数过多的评论。例如10表示跳过超过10条回复的，默认10"`
	ScrollSpeed      string `json:"scroll_speed,omitempty" jsonschema:"【仅当load_all_comments为true时生效】滚动速度slow慢速、normal正常、fast快速；默认fast"`
}

// UserProfileArgs 获取用户主页的参数
type UserProfileArgs struct {
	UserID    string `json:"user_id" jsonschema:"小红书用户ID，从Feed列表获取"`
	XsecToken string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
}

// PostCommentArgs 发表评论的参数
type PostCommentArgs struct {
	FeedID    string `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	Content      string `json:"content" jsonschema:"评论内容"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// ReplyCommentArgs 回复评论的参数
type ReplyCommentArgs struct {
	FeedID    string `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	CommentID string `json:"comment_id,omitempty" jsonschema:"目标评论ID，从评论列表获取"`
	UserID    string `json:"user_id,omitempty" jsonschema:"目标评论用户ID，从评论列表获取"`
	Content      string `json:"content" jsonschema:"回复内容"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// LikeFeedArgs 点赞参数
type LikeFeedArgs struct {
	FeedID    string `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	Unlike       bool   `json:"unlike,omitempty" jsonschema:"是否取消点赞，true为取消点赞，false或未设置则为点赞"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

// FavoriteFeedArgs 收藏参数
type FavoriteFeedArgs struct {
	FeedID     string `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken  string `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	Unfavorite   bool   `json:"unfavorite,omitempty" jsonschema:"是否取消收藏，true为取消收藏，false或未设置则为收藏"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

type BrowseSessionIDArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由create_browse_session返回"`
}

type SessionDetailArgs struct {
	SessionID     string `json:"session_id" jsonschema:"浏览会话ID，由create_browse_session返回"`
	LoadComments bool   `json:"load_comments,omitempty" jsonschema:"是否先加载更多评论再提取，默认false只提取当前可见DOM"`
	Pages        int    `json:"pages,omitzero" jsonschema:"加载评论页数；不传默认1页，正数指定页数，-1表示加载到尽头"`
}

type SessionSearchArgs struct {
	SessionID string       `json:"session_id" jsonschema:"浏览会话ID，由create_browse_session返回"`
	Keyword   string       `json:"keyword" jsonschema:"搜索关键词"`
	Filters   FilterOption `json:"filters,omitempty" jsonschema:"筛选选项"`
}

type SessionOpenNoteArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由create_browse_session返回"`
	ResultRef string `json:"result_ref" jsonschema:"搜索结果引用。可传搜索结果的index或feed_id"`
	XsecToken string `json:"xsec_token,omitempty" jsonschema:"访问令牌。通常可省略，session会使用搜索结果里的xsecToken"`
}

type SessionReadArgs struct {
	SessionID  string `json:"session_id" jsonschema:"浏览会话ID，由create_browse_session返回"`
	MinSeconds int    `json:"min_seconds,omitempty" jsonschema:"最短阅读秒数，默认20秒"`
}

type SessionLikeArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由create_browse_session返回"`
	Unlike       bool   `json:"unlike,omitempty" jsonschema:"是否取消点赞，true为取消点赞，false或未设置则为点赞"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
}

type SessionCommentArgs struct {
	SessionID string `json:"session_id" jsonschema:"浏览会话ID，由create_browse_session返回"`
	Content      string `json:"content" jsonschema:"评论内容"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"写操作确认令牌。启用XHS_WRITE_CONFIRM时，首次调用会返回该令牌，使用相同参数二次调用时传入"`
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

		return handler(ctx, req, args)
	}
}

// registerTools 注册所有 MCP 工具
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
		withPanicRecovery("check_login_status", func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
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
		withPanicRecovery("get_login_qrcode", func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
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
		withPanicRecovery("delete_cookies", func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
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
		withPanicRecovery("publish_content", func(ctx context.Context, req *mcp.CallToolRequest, args PublishContentArgs) (*mcp.CallToolResult, any, error) {
			// 转换参数格式到现有的 handler
			argsMap := map[string]interface{}{
				"title":         args.Title,
				"content":       args.Content,
				"images":        convertStringsToInterfaces(args.Images),
				"tags":          convertStringsToInterfaces(args.Tags),
				"schedule_at":   args.ScheduleAt,
				"is_original":   args.IsOriginal,
				"visibility":    args.Visibility,
				"products":      convertStringsToInterfaces(args.Products),
				"confirm_token": args.ConfirmToken,
			}
			result := appServer.handlePublishContent(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 5: 获取Feed列表
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_feeds",
			Description: "获取首页 Feeds 列表",
			Annotations: &mcp.ToolAnnotations{
				Title:        "List Feeds",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("list_feeds", func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			result := appServer.handleListFeeds(ctx)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 6: 搜索内容
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_feeds",
			Description: "搜索小红书内容（需要已登录）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Search Feeds",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("search_feeds", func(ctx context.Context, req *mcp.CallToolRequest, args SearchFeedsArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSearchFeeds(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 7: 获取Feed详情
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_feed_detail",
			Description: "获取小红书笔记详情，返回笔记内容、图片、作者信息、互动数据（点赞/收藏/分享数）及评论列表。默认返回前10条一级评论，如需更多评论请设置load_all_comments=true",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Get Feed Detail",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("get_feed_detail", func(ctx context.Context, req *mcp.CallToolRequest, args FeedDetailArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"feed_id":           args.FeedID,
				"xsec_token":        args.XsecToken,
				"load_all_comments": args.LoadAllComments,
			}

			// 只有当 load_all_comments=true 时，才处理其他参数
			if args.LoadAllComments {
				argsMap["click_more_replies"] = args.ClickMoreReplies

				if args.Limit > 0 {
					argsMap["max_comment_items"] = args.Limit
				}

				// 设置回复数量阈值，默认10
				replyLimit := args.ReplyLimit
				if replyLimit <= 0 {
					replyLimit = 10
				}
				argsMap["max_replies_threshold"] = replyLimit

				if args.ScrollSpeed != "" {
					argsMap["scroll_speed"] = args.ScrollSpeed
				}
			}

			result := appServer.handleGetFeedDetail(ctx, argsMap)
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
		withPanicRecovery("user_profile", func(ctx context.Context, req *mcp.CallToolRequest, args UserProfileArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user_id":    args.UserID,
				"xsec_token": args.XsecToken,
			}
			result := appServer.handleUserProfile(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 9: 发表评论
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "post_comment_to_feed",
			Description: "发表评论到小红书笔记",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Post Comment",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("post_comment_to_feed", func(ctx context.Context, req *mcp.CallToolRequest, args PostCommentArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"feed_id":       args.FeedID,
				"xsec_token":    args.XsecToken,
				"content":       args.Content,
				"confirm_token": args.ConfirmToken,
			}
			result := appServer.handlePostComment(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 10: 回复评论
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "reply_comment_in_feed",
			Description: "回复小红书笔记下的指定评论",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Reply Comment",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("reply_comment_in_feed", func(ctx context.Context, req *mcp.CallToolRequest, args ReplyCommentArgs) (*mcp.CallToolResult, any, error) {
			if args.CommentID == "" && args.UserID == "" {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "缺少 comment_id 或 user_id"}},
				}, nil, nil
			}

			argsMap := map[string]interface{}{
				"feed_id":       args.FeedID,
				"xsec_token":    args.XsecToken,
				"comment_id":    args.CommentID,
				"user_id":       args.UserID,
				"content":       args.Content,
				"confirm_token": args.ConfirmToken,
			}
			result := appServer.handleReplyComment(ctx, argsMap)
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
		withPanicRecovery("publish_with_video", func(ctx context.Context, req *mcp.CallToolRequest, args PublishVideoArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"title":         args.Title,
				"content":       args.Content,
				"video":         args.Video,
				"tags":          convertStringsToInterfaces(args.Tags),
				"schedule_at":   args.ScheduleAt,
				"visibility":    args.Visibility,
				"products":      convertStringsToInterfaces(args.Products),
				"confirm_token": args.ConfirmToken,
			}
			result := appServer.handlePublishVideo(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 12: 点赞笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "like_feed",
			Description: "为指定笔记点赞或取消点赞（如已点赞将跳过点赞，如未点赞将跳过取消点赞）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Like Feed",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("like_feed", func(ctx context.Context, req *mcp.CallToolRequest, args LikeFeedArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"feed_id":       args.FeedID,
				"xsec_token":    args.XsecToken,
				"unlike":        args.Unlike,
				"confirm_token": args.ConfirmToken,
			}
			result := appServer.handleLikeFeed(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 13: 收藏笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "favorite_feed",
			Description: "收藏指定笔记或取消收藏（如已收藏将跳过收藏，如未收藏将跳过取消收藏）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Favorite Feed",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("favorite_feed", func(ctx context.Context, req *mcp.CallToolRequest, args FavoriteFeedArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"feed_id":       args.FeedID,
				"xsec_token":    args.XsecToken,
				"unfavorite":    args.Unfavorite,
				"confirm_token": args.ConfirmToken,
			}
			result := appServer.handleFavoriteFeed(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 14: 创建浏览会话
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_browse_session",
			Description: "创建一个保留同一浏览器页面的浏览会话，用于连续执行搜索、打开、阅读、互动和返回",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Create Browse Session",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("create_browse_session", func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			result := appServer.handleCreateBrowseSession(ctx)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 15: session 状态
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_state",
			Description: "获取浏览会话的紧凑页面状态，包括当前URL、页面类型、就绪状态、风险信号和可执行的下一步动作",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Session State",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("session_state", func(ctx context.Context, req *mcp.CallToolRequest, args BrowseSessionIDArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionState(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 16: session 搜索
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_search",
			Description: "在浏览会话内通过真实UI搜索内容，并返回可用于session_open_note的结果引用",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Session Search",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("session_search", func(ctx context.Context, req *mcp.CallToolRequest, args SessionSearchArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionSearch(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 17: session 打开笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_open_note",
			Description: "在浏览会话内从搜索结果卡片点击打开笔记。result_ref可传搜索结果index或feed_id",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Session Open Note",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("session_open_note", func(ctx context.Context, req *mcp.CallToolRequest, args SessionOpenNoteArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionOpenNote(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 18: session 阅读
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_read",
			Description: "在浏览会话内阅读当前已打开笔记，记录阅读和滚动状态",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Session Read",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("session_read", func(ctx context.Context, req *mcp.CallToolRequest, args SessionReadArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionRead(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 19: session 详情
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_detail",
			Description: "在浏览会话当前已打开的笔记页面上直接从可见DOM提取笔记正文、作者、互动状态和评论列表；load_comments=true 时会先加载更多评论，pages不传默认1页，pages>0加载指定页数，pages<0加载到尽头；默认false只提取当前可见DOM",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Session Detail",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("session_detail", func(ctx context.Context, req *mcp.CallToolRequest, args SessionDetailArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionDetail(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 20: session 点赞
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_like",
			Description: "在浏览会话内点赞或取消点赞当前已打开且已阅读的笔记",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Session Like",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("session_like", func(ctx context.Context, req *mcp.CallToolRequest, args SessionLikeArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionLike(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 21: session 评论
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_comment",
			Description: "在浏览会话内评论当前已打开且已阅读的笔记",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Session Comment",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("session_comment", func(ctx context.Context, req *mcp.CallToolRequest, args SessionCommentArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionComment(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 22: session 返回
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "session_back",
			Description: "在浏览会话内从笔记详情返回来源页",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Session Back",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("session_back", func(ctx context.Context, req *mcp.CallToolRequest, args BrowseSessionIDArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSessionBack(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 23: 关闭浏览会话
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "close_browse_session",
			Description: "关闭浏览会话并释放浏览器页面",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Close Browse Session",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("close_browse_session", func(ctx context.Context, req *mcp.CallToolRequest, args BrowseSessionIDArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleCloseBrowseSession(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	logrus.Infof("Registered %d MCP tools", 23)
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

// convertStringsToInterfaces 辅助函数：将 []string 转换为 []interface{}
func convertStringsToInterfaces(strs []string) []interface{} {
	result := make([]interface{}, len(strs))
	for i, s := range strs {
		result[i] = s
	}
	return result
}
