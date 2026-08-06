package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/downloader"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/ratelimit"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/xhsutil"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// XiaohongshuService 小红书业务服务
type XiaohongshuService struct {
	browserManager *browser.Manager
	actionState    *xiaohongshu.ActionStateStore
	browseSessions *xiaohongshu.BrowseSessionManager
	rateLimiter    *ratelimit.Limiter

	commentCursors     sync.Map
	commentCursorTTL   time.Duration
	commentReplays     sync.Map
	feedCursors        sync.Map
	feedCursorTTL      time.Duration
	commentCursorGen   int64
	commentReplayGen   int64
	cursorGuardMu   sync.Mutex
	cursorGuardMap  map[string]*cursorGuardEntry

	createSessionMu sync.Mutex
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
		commentCursorTTL: 15 * time.Minute,
		feedCursorTTL:   5 * time.Minute,
		cursorGuardMap:  make(map[string]*cursorGuardEntry),
	}
}

func (s *XiaohongshuService) SetRateLimiter(limiter *ratelimit.Limiter) {
	s.rateLimiter = limiter
}

func (s *XiaohongshuService) setCommentCursor(id string, cursor *xiaohongshu.CommentCursor, scope replayScope) {
	id = strings.TrimSpace(id)
	if id == "" || cursor == nil {
		return
	}
	ttl := s.commentCursorTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	gen := atomic.AddInt64(&s.commentCursorGen, 1)
	entry := &commentCursorEntry{Cursor: cursor, Generation: gen, Scope: scope}
	s.commentCursors.Store(id, entry)
	time.AfterFunc(ttl, func() {
		s.commentCursors.CompareAndDelete(id, entry)
	})
}

func normalizeMaxItems(maxItems int) int {
	if maxItems <= 0 {
		return 20
	}
	return maxItems
}

func normalizeScopeSpeed(speed string) string {
	if speed != "slow" && speed != "normal" && speed != "fast" {
		return xiaohongshu.DefaultCommentLoadConfig().ScrollSpeed
	}
	return speed
}

func scopeMatch(a, b replayScope) bool {
	if a.EntryType != b.EntryType {
		return false
	}
	if a.SessionID != b.SessionID {
		return false
	}
	if a.FeedID != b.FeedID {
		return false
	}
	if a.MaxItems != b.MaxItems {
		return false
	}
	if a.ClickMoreReplies != b.ClickMoreReplies {
		return false
	}
	if a.MaxRepliesThreshold != b.MaxRepliesThreshold {
		return false
	}
	if a.ScrollSpeed != b.ScrollSpeed {
		return false
	}
	if a.XsecDigest != b.XsecDigest {
		return false
	}
	return true
}

func (s *XiaohongshuService) getCommentCursor(id string, expected replayScope) (*xiaohongshu.CommentCursor, error) {
	id = strings.TrimSpace(id)
	value, ok := s.commentCursors.Load(id)
	if !ok {
		return nil, fmt.Errorf("cursor expired, please start a new session")
	}
	entry, ok := value.(*commentCursorEntry)
	if !ok || entry == nil || entry.Cursor == nil {
		return nil, fmt.Errorf("cursor expired, please start a new session")
	}
	if !scopeMatch(entry.Scope, expected) {
		return nil, fmt.Errorf("cursor scope mismatch")
	}
	gen := atomic.AddInt64(&s.commentCursorGen, 1)
	newEntry := &commentCursorEntry{Cursor: entry.Cursor, Generation: gen, Scope: entry.Scope}
	if !s.commentCursors.CompareAndSwap(id, entry, newEntry) {
		return nil, fmt.Errorf("cursor consumed or expired")
	}
	ttl := s.commentCursorTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	time.AfterFunc(ttl, func() {
		s.commentCursors.CompareAndDelete(id, newEntry)
	})
	return entry.Cursor, nil
}

type cursorGuardEntry struct {
	mu   sync.Mutex
	refs int
}

func (s *XiaohongshuService) withCursorGuard(cursorID string, scope replayScope, fn func(*xiaohongshu.CommentCursor) (*FeedDetailResponse, error)) (*FeedDetailResponse, error) {
	cursorID = strings.TrimSpace(cursorID)
	if cursorID == "" {
		return fn(nil)
	}

	s.cursorGuardMu.Lock()
	entry, ok := s.cursorGuardMap[cursorID]
	if !ok {
		entry = &cursorGuardEntry{}
		s.cursorGuardMap[cursorID] = entry
	}
	entry.refs++
	s.cursorGuardMu.Unlock()

	entry.mu.Lock()
	defer func() {
		entry.mu.Unlock()

		s.cursorGuardMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			if current, ok := s.cursorGuardMap[cursorID]; ok && current == entry {
				delete(s.cursorGuardMap, cursorID)
			}
		}
		s.cursorGuardMu.Unlock()
	}()

	if cached, ok := s.getCommentReplay(cursorID, scope); ok {
		return cached, nil
	}
	cursor, err := s.getCommentCursor(cursorID, scope)
	if err != nil {
		return nil, err
	}
	return fn(cursor)
}

func (s *XiaohongshuService) delCommentCursor(id string) {
	s.commentCursors.Delete(strings.TrimSpace(id))
}

func (s *XiaohongshuService) getCommentReplay(cursorID string, scope replayScope) (*FeedDetailResponse, bool) {
	cursorID = strings.TrimSpace(cursorID)
	if cursorID == "" {
		return nil, false
	}
	value, ok := s.commentReplays.Load(cursorID)
	if !ok {
		return nil, false
	}
	entry, ok := value.(*commentReplayEntry)
	if !ok || entry == nil {
		return nil, false
	}
	if time.Since(entry.CreatedAt) > 2*time.Minute {
		s.commentReplays.CompareAndDelete(cursorID, entry)
		return nil, false
	}
	if entry.Scope.EntryType != scope.EntryType {
		return nil, false
	}
	if scope.SessionID != "" && entry.Scope.SessionID != scope.SessionID {
		return nil, false
	}
	if entry.Scope.FeedID != scope.FeedID || entry.Scope.MaxItems != scope.MaxItems {
		return nil, false
	}
	if entry.Scope.ClickMoreReplies != scope.ClickMoreReplies ||
		entry.Scope.MaxRepliesThreshold != scope.MaxRepliesThreshold ||
		entry.Scope.ScrollSpeed != scope.ScrollSpeed {
		return nil, false
	}
	if entry.Scope.XsecDigest != scope.XsecDigest {
		return nil, false
	}
	return deepCopyFinalResult(entry.Detail), true
}

func (s *XiaohongshuService) commitCommentBatchResult(
	feedID, cursorID string,
	inputCursor *xiaohongshu.CommentCursor,
	detail *xiaohongshu.FeedDetailResponse,
	nextCursor *xiaohongshu.CommentCursor,
	hasMore bool,
	seenCount int,
	network []xiaohongshu.NetworkCaptureEntry,
	scope replayScope,
) (*FeedDetailResponse, error) {
	cursorID = strings.TrimSpace(cursorID)
	if hasMore && (detail == nil || len(detail.Comments.List) == 0) {
		return nil, fmt.Errorf("校验失败: hasMore=true 但评论列表为空")
	}

	if hasMore {
		if nextCursor == nil {
			return nil, fmt.Errorf("校验失败: hasMore=true 但 nextCursor 为空")
		}
		if nextCursor.FeedID != "" && nextCursor.FeedID != feedID {
			return nil, fmt.Errorf("校验失败: nextCursor feed_id 不匹配")
		}
		inputLen := 0
		if inputCursor != nil {
			inputLen = len(inputCursor.ReturnedIDs)
		}
		if len(nextCursor.ReturnedIDs) <= inputLen {
			return nil, fmt.Errorf("校验失败: nextCursor.ReturnedIDs 未增长 (input=%d, output=%d)", inputLen, len(nextCursor.ReturnedIDs))
		}
	}

	nextCursorID := ""
	if hasMore && nextCursor != nil {
		nextCursorID = fmt.Sprintf("cc_%s_%d", feedID, time.Now().UnixNano())
	}

	detail.Comments.Cursor = nextCursorID
	detail.Comments.HasMore = hasMore
	if seenCount > 0 {
		detail.Comments.SeenCount = seenCount
	} else if nextCursor != nil {
		detail.Comments.SeenCount = len(nextCursor.ReturnedIDs)
	}
	if detail.Comments.List == nil {
		detail.Comments.List = []xiaohongshu.Comment{}
	}

	result := &FeedDetailResponse{FeedID: feedID, Data: detail, Network: network}
	replaySnapshot := deepCopyFinalResult(result)

	if nextCursorID != "" {
		s.setCommentCursor(nextCursorID, nextCursor, scope)
	}

	if cursorID != "" {
		gen := atomic.AddInt64(&s.commentReplayGen, 1)
		replayEntry := &commentReplayEntry{
			Detail:     replaySnapshot,
			CreatedAt:  time.Now(),
			Generation: gen,
			Scope:      scope,
		}
		s.commentReplays.Store(cursorID, replayEntry)
		time.AfterFunc(3*time.Minute, func() {
			s.commentReplays.CompareAndDelete(cursorID, replayEntry)
		})
		s.delCommentCursor(cursorID)
	}

	return result, nil
}

func (s *XiaohongshuService) setFeedCursor(id string, entry feedCursorEntry) {
	if strings.TrimSpace(id) == "" {
		return
	}
	ttl := s.feedCursorTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	s.feedCursors.Store(id, entry)
	time.AfterFunc(ttl, func() {
		if value, ok := s.feedCursors.Load(id); ok {
			if e, ok := value.(feedCursorEntry); ok && e.SessionID == entry.SessionID {
				s.feedCursors.Delete(id)
			}
		}
	})
}

func (s *XiaohongshuService) getFeedCursor(id string) (feedCursorEntry, bool) {
	value, ok := s.feedCursors.Load(strings.TrimSpace(id))
	if !ok {
		return feedCursorEntry{}, false
	}
	entry, ok := value.(feedCursorEntry)
	return entry, ok
}

func (s *XiaohongshuService) delFeedCursor(id string) {
	s.feedCursors.Delete(strings.TrimSpace(id))
}

// loadFeedCursor 读取并校验 feed cursor；空 cursor 返回 nil，返回 clone 不暴露存储对象。
func (s *XiaohongshuService) loadFeedCursor(cursorID, sessionID, queryKey string) (*xiaohongshu.FeedCursor, error) {
	if cursorID == "" {
		return nil, nil
	}
	entry, ok := s.getFeedCursor(cursorID)
	if !ok {
		return nil, fmt.Errorf("feed cursor 不存在或已过期")
	}
	if err := entry.Validate(sessionID, queryKey); err != nil {
		return nil, err
	}
	return cloneFeedCursor(entry.Cursor), nil
}

// commitFeedCursor 成功完成页面批次后消费旧 cursor 并保存新 cursor。
// 返回新 cursor ID 与已见数量；seenCount 取 next.ReturnedIDs（即使 hasMore=false 也保持现状）。
func (s *XiaohongshuService) commitFeedCursor(oldCursorID, sessionID, queryKey string, next *xiaohongshu.FeedCursor, hasMore bool) (string, int) {
	nextCursorID := ""
	if hasMore && next != nil {
		nextCursorID = fmt.Sprintf("fc_%s_%d", sessionID, time.Now().UnixNano())
		s.setFeedCursor(nextCursorID, feedCursorEntry{SessionID: sessionID, QueryKey: queryKey, Cursor: next})
	}
	if oldCursorID != "" {
		s.delFeedCursor(oldCursorID)
	}
	seenCount := 0
	if next != nil {
		seenCount = len(next.ReturnedIDs)
	}
	return nextCursorID, seenCount
}

func (s *XiaohongshuService) startReadNetworkCapture(page *hrod.Page) *xiaohongshu.NetworkCapture {
	if !configs.UseNetworkCapture() {
		return nil
	}
	return xiaohongshu.StartNetworkCapture(page, xiaohongshu.NetworkCaptureOptions{})
}

func stopReadNetworkCapture(capture *xiaohongshu.NetworkCapture) []xiaohongshu.NetworkCaptureEntry {
	if capture == nil {
		return nil
	}
	return capture.Stop()
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
	Products     []string `json:"products,omitempty"` // 商品关键词列表，用于绑定带货商品
	ConfirmToken string `json:"confirm_token,omitempty"`
}

// LoginStatusResponse 登录状态响应
type LoginStatusResponse struct {
	IsLoggedIn bool   `json:"is_logged_in"`
	Username   string `json:"username,omitempty"` // 当前登录账号的昵称
	UserID     string `json:"user_id,omitempty"`  // 用户唯一标识（个人主页 URL 中的 ID）
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
}

// PublishVideoRequest 发布视频请求（仅支持本地单个视频文件）
type PublishVideoRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Video      string   `json:"video" binding:"required"`
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products     []string `json:"products,omitempty"` // 商品关键词列表，用于绑定带货商品
	ConfirmToken string `json:"confirm_token,omitempty"`
}

// PublishVideoResponse 发布视频响应
type PublishVideoResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Video   string `json:"video"`
	Status  string `json:"status"`
}

// FeedsListResponse Feeds列表响应
type FeedsListResponse struct {
	Feeds     []xiaohongshu.Feed                `json:"feeds"`
	AIChat    *xiaohongshu.AIChatReply          `json:"ai_chat,omitempty"`
	Count     int                               `json:"count"`
	Cursor    string                            `json:"cursor,omitempty"`
	HasMore   bool                              `json:"has_more"`
	SeenCount int                               `json:"seen_count"`
	Network   []xiaohongshu.NetworkCaptureEntry `json:"network,omitempty"`
}

type commentCursorEntry struct {
	Cursor     *xiaohongshu.CommentCursor
	Generation int64
	Scope      replayScope
}

type replayScope struct {
	EntryType           string
	SessionID           string
	FeedID              string
	MaxItems            int
	ClickMoreReplies    bool
	MaxRepliesThreshold int
	ScrollSpeed         string
	XsecDigest          string
}

func xsecDigest(token string) string {
	if token == "" {
		return ""
	}
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

type commentReplayEntry struct {
	Detail     *FeedDetailResponse
	CreatedAt  time.Time
	Generation int64
	Scope      replayScope
}

func deepCopyCommentList(src []xiaohongshu.Comment) []xiaohongshu.Comment {
	if src == nil {
		return nil
	}
	dst := make([]xiaohongshu.Comment, len(src))
	for i := range src {
		dst[i] = src[i]
		dst[i].SubComments = deepCopyCommentList(src[i].SubComments)
		if src[i].ShowTags != nil {
			dst[i].ShowTags = make([]string, len(src[i].ShowTags))
			copy(dst[i].ShowTags, src[i].ShowTags)
		}
	}
	return dst
}

func deepCopyFeedDetailResponse(src *xiaohongshu.FeedDetailResponse) *xiaohongshu.FeedDetailResponse {
	if src == nil {
		return nil
	}
	noteCopy := src.Note
	if src.Note.ImageList != nil {
		noteCopy.ImageList = make([]xiaohongshu.DetailImageInfo, len(src.Note.ImageList))
		copy(noteCopy.ImageList, src.Note.ImageList)
	}
	return &xiaohongshu.FeedDetailResponse{
		Note: noteCopy,
		Comments: xiaohongshu.CommentList{
			List:       deepCopyCommentList(src.Comments.List),
			Cursor:     src.Comments.Cursor,
			HasMore:    src.Comments.HasMore,
			TotalItems: src.Comments.TotalItems,
			SeenCount:  src.Comments.SeenCount,
		},
	}
}

func deepCopyFinalResult(src *FeedDetailResponse) *FeedDetailResponse {
	if src == nil {
		return nil
	}
	var dataCopy *xiaohongshu.FeedDetailResponse
	if fdr, ok := src.Data.(*xiaohongshu.FeedDetailResponse); ok {
		dataCopy = deepCopyFeedDetailResponse(fdr)
	}
	network := append([]xiaohongshu.NetworkCaptureEntry(nil), src.Network...)
	return &FeedDetailResponse{
		FeedID:  src.FeedID,
		Data:    dataCopy,
		Network: network,
	}
}

type feedCursorEntry struct {
	SessionID string
	QueryKey  string
	Cursor    *xiaohongshu.FeedCursor
}

func cloneFeedCursor(cursor *xiaohongshu.FeedCursor) *xiaohongshu.FeedCursor {
	if cursor == nil {
		return nil
	}
	cloned := *cursor
	cloned.ReturnedIDs = append([]string(nil), cursor.ReturnedIDs...)
	return &cloned
}

func (e feedCursorEntry) Validate(sessionID, queryKey string) error {
	if e.SessionID != sessionID || e.QueryKey != queryKey {
		return fmt.Errorf("feed cursor 与当前 session 或查询不匹配")
	}
	return nil
}

func feedQueryKey(kind, keyword string, filters []xiaohongshu.FilterOption) (string, error) {
	data, err := json.Marshal(filters)
	if err != nil {
		return "", err
	}
	return kind + ":" + strings.TrimSpace(keyword) + ":" + string(data), nil
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
	if err := cookieLoader.DeleteCookies(); err != nil {
		return err
	}
	if s.actionState != nil {
		if err := s.actionState.ClearIdentity(); err != nil {
			logrus.Warnf("clear browser identity metadata failed: %v", err)
		}
	}
	return nil
}

// CheckLoginStatus 检查登录状态
func (s *XiaohongshuService) CheckLoginStatus(ctx context.Context) (*LoginStatusResponse, error) {
	loginCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	page, err := s.acquirePageFor(loginCtx, "check_login_status")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	loginAction := xiaohongshu.NewLogin(page.Context(ctx))

	isLoggedIn, err := loginAction.CheckLoginStatus(loginCtx)
	if err != nil {
		return nil, err
	}

	response := &LoginStatusResponse{
		IsLoggedIn: isLoggedIn,
	}

	// 已登录时从当前页读取真实账号信息；读不到只记 warn，不影响状态返回。
	if isLoggedIn {
		if user, err := loginAction.CurrentUser(ctx); err != nil {
			logrus.Warnf("failed to get current user info: %v", err)
		} else {
			response.Username = user.Nickname
			response.UserID = user.UserID
		}
	}

	return response, nil
}

// GetLoginQrcode 获取登录的扫码二维码
func (s *XiaohongshuService) GetLoginQrcode(ctx context.Context) (*LoginQrcodeResponse, error) {
	page, err := s.acquirePageFor(ctx, "login_qrcode")
	if err != nil {
		return nil, err
	}

	releaseInCaller := true
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			s.browserManager.Release(page)
		})
	}
	defer func() {
		if releaseInCaller {
			release()
		}
	}()

	loginAction := xiaohongshu.NewLogin(page)

	img, loggedIn, err := loginAction.FetchQrcodeImage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := 4 * time.Minute

	if !loggedIn {
		releaseInCaller = false
		s.browserManager.UpdateOwner("login_qrcode_wait")
		go func() {
			defer release()
			defer func() {
				if recovered := recover(); recovered != nil {
					logrus.Errorf("login qrcode wait panicked: %v", recovered)
				}
			}()

			ctxTimeout, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

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
	page, err := s.acquirePageFor(ctx, "publish")
	if err != nil {
		return err
	}
	defer s.browserManager.Release(page)

	action, err := xiaohongshu.NewPublishImageAction(page.Context(ctx))
	if err != nil {
		s.recordRiskFromPage(page, err)
		return err
	}

	// 执行发布
	if err := action.Publish(ctx, content); err != nil {
		s.recordRiskFromPage(page, err)
		return err
	}
	return nil
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
	page, err := s.acquirePageFor(ctx, "publish")
	if err != nil {
		return err
	}
	defer s.browserManager.Release(page)

	action, err := xiaohongshu.NewPublishVideoAction(page.Context(ctx))
	if err != nil {
		s.recordRiskFromPage(page, err)
		return err
	}

	if err := action.PublishVideo(ctx, content); err != nil {
		s.recordRiskFromPage(page, err)
		return err
	}
	return nil
}

// ListFeeds 获取Feeds列表
func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
	page, err := s.acquirePageFor(ctx, "list_feeds")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	// 创建 Feeds 列表 action
	action, err := xiaohongshu.NewFeedsListAction(page.Context(ctx))
	if err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}

	// 获取 Feeds 列表
	feeds, err := action.GetFeedsList(ctx)
	if err != nil {
		s.recordRiskFromPage(page, err)
		logrus.Errorf("获取 Feeds 列表失败: %v", err)
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}
// SearchFeeds 搜索 Feeds
func (s *XiaohongshuService) SearchFeeds(ctx context.Context, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	searchCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	page, err := s.acquirePageFor(searchCtx, "search")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewSearchActionWithState(page.Context(searchCtx), s.actionState)
	capture := s.startReadNetworkCapture(page)

	feeds, err := action.SearchFeedsOnly(searchCtx, keyword, filters...)
	network := stopReadNetworkCapture(capture)
	if err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds:   feeds,
		Count:   len(feeds),
		Network: network,
	}

	return response, nil
}

// GetFeedDetail 获取Feed详情
func (s *XiaohongshuService) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
	return s.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, xiaohongshu.DefaultCommentLoadConfig())
}

// GetFeedDetailWithConfig 使用配置获取Feed详情。
// 当存在活跃 session 且已打开同一笔记时，委托给 session。
func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	if sid, ok := s.activeSessionForFeed(feedID); ok {
		detail, err := s.SessionDetailForFeed(ctx, sid, feedID, loadAllComments, config)
		if err != nil {
			return nil, err
		}
		return &FeedDetailResponse{FeedID: feedID, Data: detail}, nil
	}
	detailCtx := ctx
	if detailCtx == nil {
		detailCtx = context.Background()
	}

	page, err := s.acquirePageFor(detailCtx, "feed_detail")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	// 创建 Feed 详情 action，并绑定到本次详情操作的有界上下文。
	action := xiaohongshu.NewFeedDetailActionWithState(page.Context(detailCtx), s.actionState)
	capture := s.startReadNetworkCapture(page)

	// 获取 Feed 详情
	result, err := action.GetFeedDetailWithConfig(detailCtx, feedID, xsecToken, loadAllComments, config)
	network := stopReadNetworkCapture(capture)
	if err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}

	response := &FeedDetailResponse{
		FeedID:  feedID,
		Data:    result,
		Network: network,
	}

	return response, nil
}

func (s *XiaohongshuService) GetFeedDetailCommentsBatch(ctx context.Context, feedID, xsecToken, cursorID string, maxItems int, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	maxItems = normalizeMaxItems(maxItems)
	config.ScrollSpeed = normalizeScopeSpeed(config.ScrollSpeed)

	if sid, ok := s.activeSessionForFeed(feedID); ok {
		scope := replayScope{
			EntryType:           "session",
			SessionID:           sid,
			FeedID:              feedID,
			MaxItems:            maxItems,
			ClickMoreReplies:    config.ClickMoreReplies,
			MaxRepliesThreshold: config.MaxRepliesThreshold,
			ScrollSpeed:         config.ScrollSpeed,
		}
		if cached, ok := s.getCommentReplay(cursorID, scope); ok {
			return cached, nil
		}
		return s.withCursorGuard(cursorID, scope, func(guardCursor *xiaohongshu.CommentCursor) (*FeedDetailResponse, error) {
			detail, nextCursor, hasMore, err := s.SessionDetailCommentsBatch(ctx, sid, feedID, guardCursor, maxItems, config)
			if err != nil {
				return nil, err
			}
			return s.commitCommentBatchResult(feedID, cursorID, guardCursor, detail, nextCursor, hasMore, 0, nil, scope)
		})
	}

	scope := replayScope{
		EntryType:           "standalone",
		FeedID:              feedID,
		MaxItems:            maxItems,
		ClickMoreReplies:    config.ClickMoreReplies,
		MaxRepliesThreshold: config.MaxRepliesThreshold,
		ScrollSpeed:         config.ScrollSpeed,
		XsecDigest:          xsecDigest(xsecToken),
	}
	if cached, ok := s.getCommentReplay(cursorID, scope); ok {
		return cached, nil
	}
	return s.withCursorGuard(cursorID, scope, func(guardCursor *xiaohongshu.CommentCursor) (*FeedDetailResponse, error) {
		detailCtx := ctx
		if detailCtx == nil {
			detailCtx = context.Background()
		}
		page, err := s.acquirePageFor(detailCtx, "feed_detail_comments_batch")
		if err != nil {
			return nil, err
		}
		defer s.browserManager.Release(page)
		action := xiaohongshu.NewFeedDetailActionWithState(page.Context(detailCtx), s.actionState)
		capture := s.startReadNetworkCapture(page)
		detail, nextCursor, hasMore, err := action.GetFeedDetailCommentsBatch(detailCtx, feedID, xsecToken, guardCursor, maxItems, config)
		network := stopReadNetworkCapture(capture)
		if err != nil {
			s.recordRiskFromPage(page, err)
			return nil, err
		}
		return s.commitCommentBatchResult(feedID, cursorID, guardCursor, detail, nextCursor, hasMore, 0, network, scope)
	})
}

// UserProfile 获取用户信息
func (s *XiaohongshuService) UserProfile(ctx context.Context, userID, xsecToken string) (*UserProfileResponse, error) {
	page, err := s.acquirePageFor(ctx, "user_profile")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewUserProfileAction(page.Context(ctx))

	result, err := action.UserProfile(ctx, userID, xsecToken)
	if err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}
	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil

}

// PostCommentToFeed 发表评论到Feed。当存在活跃 session 且已打开同一笔记时，委托给 session。
func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
	if sid, ok := s.activeSessionForFeed(feedID); ok {
		return s.SessionCommentForFeed(ctx, sid, feedID, content)
	}
	page, err := s.acquirePageFor(ctx, "comment")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewCommentFeedActionWithState(page.Context(ctx), s.actionState)

	if err := action.PostComment(ctx, feedID, xsecToken, content); err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}

	return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
}

// LikeFeed 点赞笔记。当存在活跃 session 且已打开同一笔记时，委托给 session。
func (s *XiaohongshuService) LikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if sid, ok := s.activeSessionForFeed(feedID); ok {
		return s.SessionLikeForFeed(ctx, sid, feedID, false)
	}
	page, err := s.acquirePageFor(ctx, "like")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewLikeActionWithState(page.Context(ctx), s.actionState)
	if err := action.Like(ctx, feedID, xsecToken); err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "点赞成功或已点赞"}, nil
}

// UnlikeFeed 取消点赞笔记。当存在活跃 session 且已打开同一笔记时，委托给 session。
func (s *XiaohongshuService) UnlikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if sid, ok := s.activeSessionForFeed(feedID); ok {
		return s.SessionLikeForFeed(ctx, sid, feedID, true)
	}
	page, err := s.acquirePageFor(ctx, "unlike")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewLikeActionWithState(page.Context(ctx), s.actionState)
	if err := action.Unlike(ctx, feedID, xsecToken); err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消点赞成功或未点赞"}, nil
}

// FavoriteFeed 收藏笔记。当存在活跃 session 且已打开同一笔记时，委托给 session。
func (s *XiaohongshuService) FavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if sid, ok := s.activeSessionForFeed(feedID); ok {
		return s.SessionFavoriteForFeed(ctx, sid, feedID, false)
	}
	page, err := s.acquirePageFor(ctx, "favorite")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewFavoriteActionWithState(page.Context(ctx), s.actionState)
	if err := action.Favorite(ctx, feedID, xsecToken); err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "收藏成功或已收藏"}, nil
}

// UnfavoriteFeed 取消收藏笔记。当存在活跃 session 且已打开同一笔记时，委托给 session。
func (s *XiaohongshuService) UnfavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if sid, ok := s.activeSessionForFeed(feedID); ok {
		return s.SessionFavoriteForFeed(ctx, sid, feedID, true)
	}
	page, err := s.acquirePageFor(ctx, "unfavorite")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewFavoriteActionWithState(page.Context(ctx), s.actionState)
	if err := action.Unfavorite(ctx, feedID, xsecToken); err != nil {
		s.recordRiskFromPage(page, err)
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消收藏成功或未收藏"}, nil
}

// ReplyCommentToFeed 回复指定评论。当存在活跃 session 且已打开同一笔记时，委托给 session。
func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
	if sid, ok := s.activeSessionForFeed(feedID); ok {
		return s.SessionReplyForFeed(ctx, sid, feedID, commentID, userID, content)
	}
	page, err := s.acquirePageFor(ctx, "reply")
	if err != nil {
		return nil, err
	}
	defer s.browserManager.Release(page)

	action := xiaohongshu.NewCommentFeedActionWithState(page.Context(ctx), s.actionState)

	if err := action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content); err != nil {
		s.recordRiskFromPage(page, err)
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

func (s *XiaohongshuService) CreateBrowseSession(ctx context.Context, forceRecreate bool) (*xiaohongshu.CreateBrowseSessionResult, error) {
	s.createSessionMu.Lock()
	if err := ctx.Err(); err != nil {
		s.createSessionMu.Unlock()
		return nil, err
	}
	defer s.createSessionMu.Unlock()

	if !forceRecreate {
		if result := s.tryReuseSession(ctx); result != nil {
			return result, nil
		}
	}

	s.browseSessions.CloseAll()

	page, err := s.acquirePageFor(ctx, "session")
	if err != nil {
		return nil, err
	}

	if err := page.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		s.browserManager.Release(page)
		return nil, fmt.Errorf("导航探索页失败: %w", err)
	}
	if err := xiaohongshu.WaitForXHSReady(page, xiaohongshu.XHSReadyOptions{Kind: xiaohongshu.XHSReadyHomeSearch}); err != nil {
		s.browserManager.Release(page)
		return nil, fmt.Errorf("等待探索页就绪失败: %w", err)
	}

	session := s.browseSessions.Create(page, s.actionState, s.browserManager.Release)
	s.browserManager.UpdateOwner("session:" + session.ID())
	info := session.Info()
	return &xiaohongshu.CreateBrowseSessionResult{
		Outcome:           "created",
		Session:           &info,
		RecommendedAction: "continue",
		Status: xiaohongshu.BrowseSessionStatusInfo{
			Status:  xiaohongshu.SessionReady,
			Session: &info,
			Ready:   true,
		},
	}, nil
}

func (s *XiaohongshuService) tryReuseSession(ctx context.Context) *xiaohongshu.CreateBrowseSessionResult {
	info, ok := s.browseSessions.ActiveInfo()
	if !ok {
		return nil
	}

	session, err := s.browseSessions.Get(info.ID)
	if err != nil {
		return nil
	}

	check := session.CheckReusable(ctx)

	switch check.Status {
	case xiaohongshu.SessionReady:
		info = session.Renew()
		return &xiaohongshu.CreateBrowseSessionResult{
			Outcome:           "reused",
			Session:           &info,
			RecommendedAction: "continue",
			Status: xiaohongshu.BrowseSessionStatusInfo{
				Status:          xiaohongshu.SessionReady,
				Session:         &info,
				HealthCheckedAt: check.HealthCheckedAt,
				Ready:           true,
			},
		}
	case xiaohongshu.SessionBusy:
		return &xiaohongshu.CreateBrowseSessionResult{
			Outcome:           "blocked",
			RecommendedAction: "wait",
			Status: xiaohongshu.BrowseSessionStatusInfo{
				Status:    xiaohongshu.SessionBusy,
				LastError: "session 正在执行操作",
			},
		}
	case xiaohongshu.SessionExpired, xiaohongshu.SessionNotReady:
		return &xiaohongshu.CreateBrowseSessionResult{
			Outcome:           "blocked",
			RecommendedAction: "retry",
			Status: xiaohongshu.BrowseSessionStatusInfo{
				Status:    check.Status,
				LastError: check.LastError,
			},
		}
	default:
		return &xiaohongshu.CreateBrowseSessionResult{
			Outcome:           "blocked",
			RecommendedAction: "recreate",
			Status: xiaohongshu.BrowseSessionStatusInfo{
				Status:    check.Status,
				LastError: check.LastError,
			},
		}
	}
}

func (s *XiaohongshuService) CloseBrowseSession(id string) error {
	return s.browseSessions.Close(id)
}

func (s *XiaohongshuService) ActiveBrowseSessionInfo() (xiaohongshu.BrowseSessionInfo, bool) {
	if s.browseSessions == nil {
		return xiaohongshu.BrowseSessionInfo{}, false
	}
	return s.browseSessions.ActiveInfo()
}

// activeSessionForFeed 检查是否存在活跃 session 且当前已打开同一篇笔记。
// 如果存在则返回 session ID，用于 P2 旧工具委托到 session 式行为链。
func (s *XiaohongshuService) activeSessionForFeed(feedID string) (string, bool) {
	info, ok := s.ActiveBrowseSessionInfo()
	if !ok || info.CurrentFeedID == "" || info.CurrentFeedID != feedID {
		return "", false
	}
	return info.ID, true
}

func (s *XiaohongshuService) SessionState(ctx context.Context, id string) (*xiaohongshu.BrowseSessionPageState, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	return session.PageState(ctx)
}

// SessionListFeeds 在 session 浏览器中获取首页 Feeds 列表
func (s *XiaohongshuService) SessionListFeeds(ctx context.Context, id, cursorID string, maxItems int) (*FeedsListResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}

	queryKey := "home::"
	cursor, err := s.loadFeedCursor(cursorID, id, queryKey)
	if err != nil {
		return nil, err
	}

	feeds, nextCursor, hasMore, err := session.ListFeedsBatch(ctx, cursor, maxItems)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}

	nextCursorID, seenCount := s.commitFeedCursor(cursorID, id, queryKey, nextCursor, hasMore)
	return &FeedsListResponse{
		Feeds:     feeds,
		Count:     len(feeds),
		Cursor:    nextCursorID,
		HasMore:   hasMore,
		SeenCount: seenCount,
	}, nil
}

func (s *XiaohongshuService) SessionSearch(ctx context.Context, id, keyword, cursorID string, maxItems int, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}

	queryKey, err := feedQueryKey("search", keyword, filters)
	if err != nil {
		return nil, fmt.Errorf("计算 query key 失败: %w", err)
	}

	cursor, err := s.loadFeedCursor(cursorID, id, queryKey)
	if err != nil {
		return nil, err
	}

	searchResult, nextCursor, hasMore, err := session.SearchBatchWithAI(ctx, keyword, filters, cursor, maxItems)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	feeds := searchResult.Feeds

	nextCursorID, seenCount := s.commitFeedCursor(cursorID, id, queryKey, nextCursor, hasMore)
	return &FeedsListResponse{
		Feeds:     feeds,
		AIChat:    searchResult.AIChat,
		Count:     len(feeds),
		Cursor:    nextCursorID,
		HasMore:   hasMore,
		SeenCount: seenCount,
	}, nil
}

func (s *XiaohongshuService) SessionOpenNote(ctx context.Context, id, resultRef, xsecToken string) (*xiaohongshu.SessionOpenNoteResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	result, err := session.OpenNote(ctx, resultRef, xsecToken)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	return result, nil
}

func (s *XiaohongshuService) SessionDetail(ctx context.Context, id string, loadComments bool, pages int) (*xiaohongshu.SessionDetailResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	detail, err := session.Detail(ctx, loadComments, pages)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	return detail, nil
}

// inheritCommentScope 从已保存的 cursor/replay entry 继承首次请求的 scope 配置，
// 供续页时保持展开参数一致（避免 cursor scope mismatch）。
func (s *XiaohongshuService) inheritCommentScope(cursorID string) (replayScope, bool) {
	cursorID = strings.TrimSpace(cursorID)
	if cursorID == "" {
		return replayScope{}, false
	}
	if value, ok := s.commentCursors.Load(cursorID); ok {
		if entry, ok := value.(*commentCursorEntry); ok && entry != nil && entry.Cursor != nil {
			return entry.Scope, true
		}
	}
	if value, ok := s.commentReplays.Load(cursorID); ok {
		if entry, ok := value.(*commentReplayEntry); ok && entry != nil {
			if time.Since(entry.CreatedAt) <= 2*time.Minute {
				return entry.Scope, true
			}
			s.commentReplays.CompareAndDelete(cursorID, entry)
		}
	}
	return replayScope{}, false
}

func (s *XiaohongshuService) SessionDetailBatch(ctx context.Context, id, cursorID string, maxItems int, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	info := session.Info()

	maxItems = normalizeMaxItems(maxItems)
	config.ScrollSpeed = normalizeScopeSpeed(config.ScrollSpeed)

	// 续页时继承首次请求的展开配置（max_items/click_more_replies/reply_limit/scroll_speed），
	// 避免本次请求参数与已保存 cursor scope 不一致而触发 scope mismatch。
	if inherited, ok := s.inheritCommentScope(cursorID); ok {
		maxItems = inherited.MaxItems
		config.ClickMoreReplies = inherited.ClickMoreReplies
		config.MaxRepliesThreshold = inherited.MaxRepliesThreshold
		config.ScrollSpeed = inherited.ScrollSpeed
	}

	scope := replayScope{
		EntryType:           "session",
		SessionID:           id,
		FeedID:              info.CurrentFeedID,
		MaxItems:            maxItems,
		ClickMoreReplies:    config.ClickMoreReplies,
		MaxRepliesThreshold: config.MaxRepliesThreshold,
		ScrollSpeed:         config.ScrollSpeed,
	}

	if cached, ok := s.getCommentReplay(cursorID, scope); ok {
		return cached, nil
	}

	return s.withCursorGuard(cursorID, scope, func(guardCursor *xiaohongshu.CommentCursor) (*FeedDetailResponse, error) {
		if guardCursor == nil {
			if ids := session.GetInitialCommentIDs(); len(ids) > 0 {
				guardCursor = &xiaohongshu.CommentCursor{
					FeedID:      info.CurrentFeedID,
					ReturnedIDs: ids,
				}
			}
		}
		detail, nextCursor, hasMore, err := session.DetailCommentsBatch(ctx, info.CurrentFeedID, guardCursor, maxItems, config)
		if err != nil {
			s.recordRiskFromSession(session, err)
			return nil, err
		}
		seenCount := 0
		if nextCursor != nil {
			seenCount = len(nextCursor.ReturnedIDs)
		}
		return s.commitCommentBatchResult(info.CurrentFeedID, cursorID, guardCursor, detail, nextCursor, hasMore, seenCount, nil, scope)
	})
}

func (s *XiaohongshuService) SessionDetailForFeed(ctx context.Context, id, feedID string, loadComments bool, config xiaohongshu.CommentLoadConfig) (*xiaohongshu.FeedDetailResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	detail, err := session.DetailForFeed(ctx, feedID, loadComments, config)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	return detail, nil
}

func (s *XiaohongshuService) SessionDetailCommentsBatch(ctx context.Context, id, feedID string, cursor *xiaohongshu.CommentCursor, maxItems int, config xiaohongshu.CommentLoadConfig) (*xiaohongshu.FeedDetailResponse, *xiaohongshu.CommentCursor, bool, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, nil, false, err
	}
	detail, nextCursor, hasMore, err := session.DetailCommentsBatch(ctx, feedID, cursor, maxItems, config)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, nil, false, err
	}
	return detail, nextCursor, hasMore, nil
}

func (s *XiaohongshuService) SessionLike(ctx context.Context, id string, unlike bool) (*ActionResult, error) {
	return s.sessionLike(ctx, id, "", unlike)
}

func (s *XiaohongshuService) SessionLikeForFeed(ctx context.Context, id, feedID string, unlike bool) (*ActionResult, error) {
	return s.sessionLike(ctx, id, feedID, unlike)
}

func (s *XiaohongshuService) sessionLike(ctx context.Context, id, feedID string, unlike bool) (*ActionResult, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.LikeForFeed(ctx, feedID, unlike); err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	info := session.Info()
	action := "点赞成功或已点赞"
	if unlike {
		action = "取消点赞成功或未点赞"
	}
	return &ActionResult{FeedID: info.CurrentFeedID, Success: true, Message: action}, nil
}

func (s *XiaohongshuService) SessionFavorite(ctx context.Context, id string, unfavorite bool) (*ActionResult, error) {
	return s.sessionFavorite(ctx, id, "", unfavorite)
}

func (s *XiaohongshuService) SessionFavoriteForFeed(ctx context.Context, id, feedID string, unfavorite bool) (*ActionResult, error) {
	return s.sessionFavorite(ctx, id, feedID, unfavorite)
}

func (s *XiaohongshuService) sessionFavorite(ctx context.Context, id, feedID string, unfavorite bool) (*ActionResult, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.FavoriteForFeed(ctx, feedID, unfavorite); err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	info := session.Info()
	action := "收藏成功或已收藏"
	if unfavorite {
		action = "取消收藏成功或未收藏"
	}
	return &ActionResult{FeedID: info.CurrentFeedID, Success: true, Message: action}, nil
}

func (s *XiaohongshuService) SessionComment(ctx context.Context, id, content string) (*PostCommentResponse, error) {
	return s.sessionComment(ctx, id, "", content)
}

func (s *XiaohongshuService) SessionCommentForFeed(ctx context.Context, id, feedID, content string) (*PostCommentResponse, error) {
	return s.sessionComment(ctx, id, feedID, content)
}

func (s *XiaohongshuService) sessionComment(ctx context.Context, id, feedID, content string) (*PostCommentResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.CommentForFeed(ctx, feedID, content); err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	info := session.Info()
	return &PostCommentResponse{FeedID: info.CurrentFeedID, Success: true, Message: "评论发表成功"}, nil
}

func (s *XiaohongshuService) SessionReply(ctx context.Context, id, commentID, userID, content string) (*ReplyCommentResponse, error) {
	return s.sessionReply(ctx, id, "", commentID, userID, content)
}

func (s *XiaohongshuService) SessionReplyForFeed(ctx context.Context, id, feedID, commentID, userID, content string) (*ReplyCommentResponse, error) {
	return s.sessionReply(ctx, id, feedID, commentID, userID, content)
}

func (s *XiaohongshuService) sessionReply(ctx context.Context, id, feedID, commentID, userID, content string) (*ReplyCommentResponse, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.ReplyForFeed(ctx, feedID, commentID, userID, content); err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	info := session.Info()
	return &ReplyCommentResponse{
		FeedID:          info.CurrentFeedID,
		TargetCommentID: commentID,
		TargetUserID:    userID,
		Success:         true,
		Message:         "评论回复成功",
	}, nil
}

func (s *XiaohongshuService) SessionBack(ctx context.Context, id string) (*xiaohongshu.BrowseSessionInfo, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	if err := session.Back(ctx); err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	info := session.Info()
	return &info, nil
}

// SessionUnreadNotificationCount 读取通知未读数，只读不点击入口。
func (s *XiaohongshuService) SessionUnreadNotificationCount(ctx context.Context, id string) (*xiaohongshu.NotificationCount, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	count, err := session.UnreadNotificationCount(ctx)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	return count, nil
}

// SessionListNotifications 在 session 中进入通知页并读取通知列表。
func (s *XiaohongshuService) SessionListNotifications(ctx context.Context, id, tab, cursor string, maxItems int) (*xiaohongshu.NotificationList, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	list, err := session.ListNotifications(ctx, tab, cursor, maxItems)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	return list, nil
}

// SessionLikeNotification 点赞/取消点赞通知中的评论。
func (s *XiaohongshuService) SessionLikeNotification(ctx context.Context, id, notificationRef string, unlike bool) (*xiaohongshu.NotificationLikeResult, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	result, err := session.LikeNotification(ctx, notificationRef, unlike)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	return result, nil
}

// SessionReplyNotification 回复通知中的评论。
func (s *XiaohongshuService) SessionReplyNotification(ctx context.Context, id, notificationRef, content string) (*xiaohongshu.NotificationReplyResult, error) {
	session, err := s.browseSessions.Get(id)
	if err != nil {
		return nil, err
	}
	result, err := session.ReplyNotification(ctx, notificationRef, content)
	if err != nil {
		s.recordRiskFromSession(session, err)
		return nil, err
	}
	return result, nil
}

func newBrowser(ctx context.Context) (*hrod.Browser, error) {
	return browser.NewBrowser(
		ctx,
		configs.IsHeadless(),
		browser.WithBinPath(configs.GetBinPath()),
		browser.WithUserAgent(configs.GetBrowserUserAgent()),
		browser.WithProfileDir(configs.GetProfileDir()),
		browser.WithCloakBrowser(configs.UseCloakBrowser()),
		browser.WithCloakLauncherProfile(configs.CloakLauncherProfile()),
		browser.WithExtraArgs(configs.GetBrowserExtraArgs()),
		browser.WithFingerprintSeed(configs.FingerprintSeed()),
		browser.WithLanguage(configs.BrowserLanguage()),
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

func (s *XiaohongshuService) recordRiskFromPage(page *hrod.Page, sourceErr error) {
	if page == nil || sourceErr == nil {
		return
	}
	riskCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	signal, err := xiaohongshu.ClassifyRisk(page.Context(riskCtx))
	if err != nil {
		logrus.Debugf("classify XHS risk after error failed: %v", err)
		return
	}
	if !xiaohongshu.IsRisk(signal) {
		return
	}
	s.recordRiskSignal(signal, sourceErr)
}

func (s *XiaohongshuService) recordRiskFromSession(session *xiaohongshu.BrowseSession, sourceErr error) {
	if session == nil || sourceErr == nil {
		return
	}
	riskCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	signal, err := session.ClassifyRiskContext(riskCtx)
	if err != nil {
		logrus.Debugf("classify XHS session risk after error failed: %v", err)
		return
	}
	if !xiaohongshu.IsRisk(signal) {
		return
	}
	s.recordRiskSignal(signal, sourceErr)
}

func (s *XiaohongshuService) recordRiskSignal(signal xiaohongshu.RiskSignal, sourceErr error) {
	reason := formatRiskReason(signal)
	if s.actionState != nil {
		if err := s.actionState.RecordRisk(reason, signal.Cooldown); err != nil {
			logrus.Warnf("record action risk failed: %v", err)
		}
	}
	if s.rateLimiter != nil {
		s.rateLimiter.RecordRisk(reason, signal.Cooldown)
	}
	logrus.Warnf("detected XHS risk kind=%s recoverable=%v cooldown=%s reason=%s op_error=%v",
		signal.Kind, signal.Recoverable, signal.Cooldown, reason, sourceErr)
}

func formatRiskReason(signal xiaohongshu.RiskSignal) string {
	reason := string(signal.Kind)
	if signal.Reason != "" {
		reason = signal.Reason
	}
	if signal.MatchedText != "" {
		reason = fmt.Sprintf("%s: %s", reason, signal.MatchedText)
	}
	return reason
}

func (s *XiaohongshuService) acquirePageFor(ctx context.Context, owner string) (*hrod.Page, error) {
	page, err := s.browserManager.AcquireFor(ctx, owner)
	if err != nil {
		return nil, err
	}
	if err := s.checkFixedIdentity(page); err != nil {
		logrus.Warnf("browser identity check skipped: %v", err)
	}
	return page, nil
}

func (s *XiaohongshuService) checkFixedIdentity(page *hrod.Page) error {
	if !configs.UseFixedIdentity() || s.actionState == nil {
		return nil
	}
	current, err := xiaohongshu.CaptureIdentityMetadata(page)
	if err != nil {
		return fmt.Errorf("browser identity fingerprint check failed: %w", err)
	}
	baseline, drift, err := s.actionState.CheckIdentity(current)
	if err != nil {
		return fmt.Errorf("browser identity state check failed: %w", err)
	}
	if len(drift) == 0 {
		return nil
	}

	reason := formatIdentityDriftReason(baseline, current, drift)
	logrus.Warn(reason)
	return nil
}

func formatIdentityDriftReason(baseline, current xiaohongshu.IdentityMetadata, drift []xiaohongshu.IdentityDrift) string {
	parts := make([]string, 0, len(drift))
	for i, item := range drift {
		if i >= 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(drift)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %q -> %q", item.Field, capIdentityValue(item.Before), capIdentityValue(item.After)))
	}
	return fmt.Sprintf("browser identity drift detected baseline=%s current=%s: %s",
		shortFingerprint(baseline.Fingerprint), shortFingerprint(current.Fingerprint), strings.Join(parts, "; "))
}

func shortFingerprint(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func capIdentityValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 80 {
		return value
	}
	return value[:77] + "..."
}

// withBrowserPage 执行需要浏览器页面的操作的通用函数
func (s *XiaohongshuService) withBrowserPage(ctx context.Context, fn func(*hrod.Page) error) error {
	page, err := s.acquirePageFor(ctx, "my_profile")
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
		if err != nil {
			s.recordRiskFromPage(page, err)
		}
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
