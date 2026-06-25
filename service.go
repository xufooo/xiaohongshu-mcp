package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/downloader"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/xhsutil"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// XiaohongshuService 小红书业务服务
type XiaohongshuService struct {
	browserManager *browser.Manager
	actionState    *xiaohongshu.ActionStateStore
	browseSessions *xiaohongshu.BrowseSessionManager
}

// NewXiaohongshuService 创建小红书服务实例
func NewXiaohongshuService() *XiaohongshuService {
	return &XiaohongshuService{
		browserManager: browser.NewManager(
			newBrowser,
			browser.WithIdleTimeout(configs.GetBrowserIdleTimeout()),
		),
		actionState: xiaohongshu.DefaultActionStateStore(
			configs.Username,
			configs.GetProfileDir(),
			cookies.GetCookiesFilePath(),
		),
		browseSessions: xiaohongshu.NewBrowseSessionManager(xiaohongshu.DefaultBrowseSessionTimeout),
	}
}

// PublishRequest 发布请求
type PublishRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Images     []string `json:"images" binding:"required,min=1"`
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	IsOriginal bool     `json:"is_original,omitempty"` // 是否声明原创
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products   []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// LoginStatusResponse 登录状态响应
type LoginStatusResponse struct {
	IsLoggedIn bool   `json:"is_logged_in"`
	Username   string `json:"username,omitempty"`
}

// LoginQrcodeResponse 登录扫码二维码
type LoginQrcodeResponse struct {
	Timeout    string `json:"timeout"`
	IsLoggedIn bool   `json:"is_logged_in"`
	Img        string `json:"img,omitempty"`
}

// PublishResponse 发布响应
type PublishResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Images  int    `json:"images"`
	Status  string `json:"status"`
	PostID  string `json:"post_id,omitempty"`
}

// PublishVideoRequest 发布视频请求（仅支持本地单个视频文件）
type PublishVideoRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Video      string   `json:"video" binding:"required"`
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products   []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// PublishVideoResponse 发布视频响应
type PublishVideoResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Video   string `json:"video"`
	Status  string `json:"status"`
	PostID  string `json:"post_id,omitempty"`
}

// FeedsListResponse Feeds列表响应
type FeedsListResponse struct {
	Feeds []xiaohongshu.Feed `json:"feeds"`
	Count int                `json:"count"`
}

// UserProfileResponse 用户主页响应
type UserProfileResponse struct {
	UserBasicInfo xiaohongshu.UserBasicInfo      `json:"userBasicInfo"`
	Interactions  []xiaohongshu.UserInteractions `json:"interactions"`
	Feeds         []xiaohongshu.Feed             `json:"feeds"`
}

// DeleteCookies 删除 cookies 文件，用于登录重置
func (s *XiaohongshuService) DeleteCookies(ctx context.Context) error {
	if err := s.browserManager.Reset(ctx); err != nil {
		return err
	}
	if profileDir := configs.GetProfileDir(); profileDir != "" {
		if err := os.RemoveAll(profileDir); err != nil {
			return fmt.Errorf("删除浏览器 profile 失败: %w", err)
		}
	}

	cookiePath := cookies.GetCookiesFilePath()
	cookieLoader := cookies.NewLoadCookie(cookiePath)
	return cookieLoader.DeleteCookies()
}

// CheckLoginStatus 检查登录状态
func (s *XiaohongshuService) CheckLoginStatus(ctx context.Context) (*LoginStatusResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	loginAction := xiaohongshu.NewLogin(page.Context(ctx))

	isLoggedIn, err := loginAction.CheckLoginStatus(ctx)
	if err != nil {
		return nil, err
	}

	response := &LoginStatusResponse{
		IsLoggedIn: isLoggedIn,
		Username:   configs.Username,
	}

	return response, nil
}

// GetLoginQrcode 获取登录的扫码二维码
func (s *XiaohongshuService) GetLoginQrcode(ctx context.Context) (*LoginQrcodeResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	deferFunc := func() {
		s.browserManager.Release(page)
	}

	loginAction := xiaohongshu.NewLogin(page)

	img, loggedIn, err := loginAction.FetchQrcodeImage(ctx)
	if err != nil || loggedIn {
		defer deferFunc()
	}
	if err != nil {
		return nil, err
	}

	timeout := 4 * time.Minute

	if !loggedIn {
		go func() {
			ctxTimeout, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			defer deferFunc()

			if loginAction.WaitForLogin(ctxTimeout) {
				if er := saveCookies(page); er != nil {
					logrus.Errorf("failed to save cookies: %v", er)
				}
			}
		}()
	}

	return &LoginQrcodeResponse{
		Timeout: func() string {
			if loggedIn {
				return "0s"
			}
			return timeout.String()
		}(),
		Img:        img,
		IsLoggedIn: loggedIn,
	}, nil
}

// PublishContent 发布内容
func (s *XiaohongshuService) PublishContent(ctx context.Context, req *PublishRequest) (*PublishResponse, error) {
	// 验证标题长度（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 处理图片：下载URL图片或使用本地路径
	imagePaths, err := s.processImages(req.Images)
	if err != nil {
		return nil, err
	}

	// 解析定时发布时间
	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	// 构建发布内容
	content := xiaohongshu.PublishImageContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		ImagePaths:   imagePaths,
		ScheduleTime: scheduleTime,
		IsOriginal:   req.IsOriginal,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	// 执行发布
	if err := s.publishContent(ctx, content); err != nil {
		logrus.Errorf("发布内容失败: title=%s %v", content.Title, err)
		return nil, err
	}

	response := &PublishResponse{
		Title:   req.Title,
		Content: req.Content,
		Images:  len(imagePaths),
		Status:  "发布完成",
	}

	return response, nil
}

// processImages 处理图片列表，支持URL下载和本地路径
func (s *XiaohongshuService) processImages(images []string) ([]string, error) {
	processor := downloader.NewImageProcessor()
	return processor.ProcessImages(images)
}

// publishContent 执行内容发布
func (s *XiaohongshuService) publishContent(ctx context.Context, content xiaohongshu.PublishImageContent) error {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return err
	}
	defer s.browserManager.Release(page)

	action, err := xiaohongshu.NewPublishImageAction(page.Context(ctx))
	if err != nil {
		return err
	}

	// 执行发布
	return action.Publish(ctx, content)
}

// PublishVideo 发布视频（本地文件）
func (s *XiaohongshuService) PublishVideo(ctx context.Context, req *PublishVideoRequest) (*PublishVideoResponse, error) {
	// 标题长度校验（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 本地视频文件校验
	if req.Video == "" {
		return nil, fmt.Errorf("必须提供本地视频文件")
	}
	if _, err := os.Stat(req.Video); err != nil {
		return nil, fmt.Errorf("视频文件不存在或不可访问: %v", err)
	}

	// 解析定时发布时间
	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	// 构建发布内容
	content := xiaohongshu.PublishVideoContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		VideoPath:    req.Video,
		ScheduleTime: scheduleTime,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	// 执行发布
	if err := s.publishVideo(ctx, content); err != nil {
		return nil, err
	}

	resp := &PublishVideoResponse{
		Title:   req.Title,
		Content: req.Content,
		Video:   req.Video,
		Status:  "发布完成",
	}
	return resp, nil
}

// publishVideo 执行视频发布
func (s *XiaohongshuService) publishVideo(ctx context.Context, content xiaohongshu.PublishVideoContent) error {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return err
	}
	defer s.browserManager.Release(page)

	action, err := xiaohongshu.NewPublishVideoAction(page.Context(ctx))
	if err != nil {
		return err
	}

	return action.PublishVideo(ctx, content)
}

// ListFeeds 获取Feeds列表
func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	// 创建 Feeds 列表 action
	action := xiaohongshu.NewFeedsListAction(page.Context(ctx))

	// 获取 Feeds 列表
	feeds, err := action.GetFeedsList(ctx)
	if err != nil {
		logrus.Errorf("获取 Feeds 列表失败: %v", err)
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}

func (s *XiaohongshuService) SearchFeeds(ctx context.Context, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewSearchActionWithState(page.Context(ctx), s.actionState)

	feeds, err := action.Search(ctx, keyword, filters...)
	if err != nil {
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}

// GetFeedDetail 获取Feed详情
func (s *XiaohongshuService) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
	return s.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, xiaohongshu.DefaultCommentLoadConfig())
}

// GetFeedDetailWithConfig 使用配置获取Feed详情
func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	// 创建 Feed 详情 action
	action := xiaohongshu.NewFeedDetailActionWithState(page.Context(ctx), s.actionState)

	// 获取 Feed 详情
	result, err := action.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
	if err != nil {
		return nil, err
	}

	response := &FeedDetailResponse{
		FeedID: feedID,
		Data:   result,
	}

	return response, nil
}

// UserProfile 获取用户信息
func (s *XiaohongshuService) UserProfile(ctx context.Context, userID, xsecToken string) (*UserProfileResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewUserProfileAction(page.Context(ctx))

	result, err := action.UserProfile(ctx, userID, xsecToken)
	if err != nil {
		return nil, err
	}
	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil

}

// PostCommentToFeed 发表评论到Feed
func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewCommentFeedActionWithState(page.Context(ctx), s.actionState)

	if err := action.PostComment(ctx, feedID, xsecToken, content); err != nil {
		return nil, err
	}

	return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
}

// LikeFeed 点赞笔记
func (s *XiaohongshuService) LikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewLikeActionWithState(page.Context(ctx), s.actionState)
	if err := action.Like(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "点赞成功或已点赞"}, nil
}

// UnlikeFeed 取消点赞笔记
func (s *XiaohongshuService) UnlikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewLikeActionWithState(page.Context(ctx), s.actionState)
	if err := action.Unlike(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消点赞成功或未点赞"}, nil
}

// FavoriteFeed 收藏笔记
func (s *XiaohongshuService) FavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewFavoriteActionWithState(page.Context(ctx), s.actionState)
	if err := action.Favorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "收藏成功或已收藏"}, nil
}

// UnfavoriteFeed 取消收藏笔记
func (s *XiaohongshuService) UnfavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewFavoriteActionWithState(page.Context(ctx), s.actionState)
	if err := action.Unfavorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消收藏成功或未收藏"}, nil
}

// ReplyCommentToFeed 回复指定评论
func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewCommentFeedActionWithState(page.Context(ctx), s.actionState)

	if err := action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content); err != nil {
		return nil, err
	}

	return &ReplyCommentResponse{
		FeedID:          feedID,
		TargetCommentID: commentID,
		TargetUserID:    userID,
		Success:         true,
		Message:         "评论回复成功",
	}, nil
}

func (s *XiaohongshuService) CreateBrowseSession(ctx context.Context) (*xiaohongshu.BrowseSessionInfo, error) {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	session := s.browseSessions.Create(page, s.actionState, s.browserManager.Release)
	info := session.Info()
	return &info, nil
}

func (s *XiaohongshuService) CloseBrowseSession(id string) error {
	return s.browseSessions.Close(id)
}

func (s *XiaohongshuService) SessionSearch(ctx context.Context, id, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	feeds, err := session.Search(ctx, keyword, filters...)
	if err != nil {
		return nil, err
	}
	return &FeedsListResponse{Feeds: feeds, Count: len(feeds)}, nil
}

func (s *XiaohongshuService) SessionOpenNote(ctx context.Context, id, resultRef, xsecToken string) (*xiaohongshu.BrowseSessionInfo, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.OpenNote(ctx, resultRef, xsecToken); err != nil {
		return nil, err
	}
	info := session.Info()
	return &info, nil
}

func (s *XiaohongshuService) SessionRead(ctx context.Context, id string, minDuration time.Duration) (*xiaohongshu.BrowseSessionInfo, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.Read(ctx, minDuration); err != nil {
		return nil, err
	}
	info := session.Info()
	return &info, nil
}

func (s *XiaohongshuService) SessionLike(ctx context.Context, id string, unlike bool) (*ActionResult, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	info := session.Info()
	if err := session.Like(ctx, unlike); err != nil {
		return nil, err
	}
	action := "点赞成功或已点赞"
	if unlike {
		action = "取消点赞成功或未点赞"
	}
	return &ActionResult{FeedID: info.CurrentFeedID, Success: true, Message: action}, nil
}

func (s *XiaohongshuService) SessionComment(ctx context.Context, id, content string) (*PostCommentResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	info := session.Info()
	if err := session.Comment(ctx, content); err != nil {
		return nil, err
	}
	return &PostCommentResponse{FeedID: info.CurrentFeedID, Success: true, Message: "评论发表成功"}, nil
}

func (s *XiaohongshuService) SessionBack(ctx context.Context, id string) (*xiaohongshu.BrowseSessionInfo, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.Back(ctx); err != nil {
		return nil, err
	}
	info := session.Info()
	return &info, nil
}

func newBrowser() *hrod.Browser {
	return browser.NewBrowser(
		configs.IsHeadless(),
		browser.WithBinPath(configs.GetBinPath()),
		browser.WithProfileDir(configs.GetProfileDir()),
		browser.WithCloakBrowser(configs.UseCloakBrowser()),
		browser.WithExtraArgs(configs.GetBrowserExtraArgs()),
	)
}

func saveCookies(page *hrod.Page) error {
	cks, err := page.Rod.Browser().GetCookies()
	if err != nil {
		return err
	}

	data, err := json.Marshal(cks)
	if err != nil {
		return err
	}

	cookieLoader := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	return cookieLoader.SaveCookies(data)
}

// withBrowserPage 执行需要浏览器页面的操作的通用函数
func (s *XiaohongshuService) withBrowserPage(ctx context.Context, fn func(*hrod.Page) error) error {
	page, err := s.browserManager.Acquire(ctx)
	if err != nil {
		return err
	}
	defer s.browserManager.Release(page)

	return fn(page)
}

// Close 关闭常驻浏览器。
func (s *XiaohongshuService) Close(ctx context.Context) error {
	if s.browseSessions != nil {
		s.browseSessions.CloseAll()
	}
	return s.browserManager.Close(ctx)
}

// GetMyProfile 获取当前登录用户的个人信息
func (s *XiaohongshuService) GetMyProfile(ctx context.Context) (*UserProfileResponse, error) {
	var result *xiaohongshu.UserProfileResponse
	var err error

	err = s.withBrowserPage(ctx, func(page *hrod.Page) error {
		action := xiaohongshu.NewUserProfileAction(page.Context(ctx))
		result, err = action.GetMyProfileViaSidebar(ctx)
		return err
	})

	if err != nil {
		return nil, err
	}

	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil
}
