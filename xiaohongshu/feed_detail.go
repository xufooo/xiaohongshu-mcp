package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// ========== 配置常量 ==========
const (
	defaultMaxAttempts     = 500
	stagnantLimit          = 12
	minScrollDelta         = 10
	maxClickPerRound       = 3
	stagnantCheckThreshold = 2 // 达到目标后需要停滞几次才确认
	largeScrollTrigger     = 5 // 停滞多少次后触发大滚动
	buttonClickInterval    = 3 // 每隔多少次尝试点击一次按钮
	finalSprintPushCount   = 15
	commentPollInterval    = 100 * time.Millisecond
	commentProgressTimeout = 3 * time.Second
	// The note is available before the asynchronously populated comment ref on
	// some versions of the web client. Keep this short: it is only used when
	// the note reports comments but the state snapshot has none.
	initialCommentStateTimeout = 5 * time.Second
)

const (
	feedDetailPageTimeout = 10 * time.Minute
	commentLoadTimeout    = 9 * time.Minute
)

// 延迟时间配置（毫秒）
type delayConfig struct {
	min, max int
}

var (
	humanDelayRange   = delayConfig{300, 700}
	reactionTimeRange = delayConfig{300, 800}
	hoverTimeRange    = delayConfig{100, 300}
	readTimeRange     = delayConfig{500, 1200}
	shortReadRange    = delayConfig{600, 1200}
	scrollWaitRange   = delayConfig{100, 200}
	postScrollRange   = delayConfig{300, 500}
)

// ========== 数据结构 ==========

type CommentLoadConfig struct {
	ClickMoreReplies    bool
	MaxRepliesThreshold int
	MaxCommentItems     int
	ScrollSpeed         string
}

func DefaultCommentLoadConfig() CommentLoadConfig {
	return CommentLoadConfig{
		ClickMoreReplies:    false,
		MaxRepliesThreshold: 10,
		MaxCommentItems:     0,
		ScrollSpeed:         "",
	}
}

// FeedDetailAction 获取Feed详情动作
type FeedDetailAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func NewFeedDetailAction(page *hrod.Page) *FeedDetailAction {
	return &FeedDetailAction{page: page}
}

func NewFeedDetailActionWithState(page *hrod.Page, state *ActionStateStore) *FeedDetailAction {
	return &FeedDetailAction{page: page, state: state}
}

// ========== 主要业务逻辑 ==========

func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
	return f.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, DefaultCommentLoadConfig())
}

func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	page := f.page.Context(ctx)

	if err := navigateToFeedDetail(page, feedID, xsecToken); err != nil {
		return nil, fmt.Errorf("导航到详情页失败: %w", err)
	}

	// 等待页面加载完成
	if err := waitForPage(page); err != nil {
		return nil, err
	}

	if err := sleepRandom(page, 1000, 1000); err != nil {
		return nil, err
	}

	if err := checkPageAccessible(page); err != nil {
		return nil, err
	}
	readDuration := 20 * time.Second
	if loadAllComments {
		readDuration = 5 * time.Second
	}
	reader := NewReadStageAction(page, f.state)
	if err := reader.Read(ctx, feedID, readDuration); err != nil {
		return nil, fmt.Errorf("阅读阶段失败: %w", err)
	}
	if !loadAllComments {
		return f.extractFeedDetail(page, feedID)
	}

	// Keep the server-provided first page before interacting with the comment
	// area. Scrolling changes the client-side store on some site versions; if
	// that later snapshot is incomplete, this remains a valid partial result.
	initialDetail, initialDetailErr := f.extractFeedDetail(page, feedID)
	if initialDetailErr != nil {
		logrus.Debugf("加载评论前提取详情失败: %v", initialDetailErr)
	}

	// Bound the scroll loop independently so comment loading failures return a
	// scoped error instead of consuming the main page context.
	commentPage := page.Timeout(commentLoadTimeout)
	commentStart := time.Now()
	commentLoadErr := f.loadAllCommentsWithConfig(commentPage, config)
	reader.RecordCommentDwell(feedID, time.Since(commentStart), true)
	if commentLoadErr != nil {
		if strings.Contains(commentLoadErr.Error(), "context deadline exceeded") ||
			strings.Contains(commentLoadErr.Error(), "timeout") ||
			strings.Contains(commentLoadErr.Error(), "Timeout") {
			logrus.Warnf("评论加载超时(%s)，返回已加载的部分数据: %v",
				time.Since(commentStart).Round(time.Second), commentLoadErr)
		} else {
			logrus.Warnf("加载全部评论失败，返回已加载的部分数据: %v", commentLoadErr)
		}
	}

	detail, err := f.extractFeedDetail(page, feedID)
	if err != nil {
		if commentLoadErr != nil {
			return nil, fmt.Errorf("加载全部评论失败: %v；提取详情失败: %w", commentLoadErr, err)
		}
		if initialDetail != nil {
			logrus.Warnf("评论加载后提取详情失败，返回加载前快照: %v", err)
			return initialDetail, nil
		}
		return nil, err
	}
	if shouldUseInitialCommentSnapshot(initialDetail, detail) {
		logrus.Warn("评论加载后的状态快照为空，返回加载前的评论列表")
		return initialDetail, nil
	}
	return detail, nil
}

func normalizeCommentLoadConfig(config CommentLoadConfig) CommentLoadConfig {
	switch config.ScrollSpeed {
	case "slow", "normal", "fast":
	default:
		config.ScrollSpeed = DefaultCommentLoadConfig().ScrollSpeed
	}
	return config
}

func sessionCommentPageLoadConfig(progress commentProgress, progressErr error) CommentLoadConfig {
	config := DefaultCommentLoadConfig()
	if progressErr == nil {
		config.MaxCommentItems = progress.Count + rand.Intn(6) + 5
		if progress.Total > 0 && config.MaxCommentItems > progress.Total {
			config.MaxCommentItems = progress.Total
		}
	} else {
		config.MaxCommentItems = 10
	}
	return config
}

// ========== 评论加载器 ==========

type commentLoader struct {
	page   *hrod.Page
	config CommentLoadConfig
	stats  *loadStats
	state  *loadState
}

type loadStats struct {
	totalClicked int
	totalSkipped int
	attempts     int
}

type loadState struct {
	lastCount      int
	lastScrollTop  int
	stagnantChecks int
}

// commentProgress is collected in one browser evaluation. Keeping the check in
// the browser avoids several round trips per scroll on slower devices.
type commentProgress struct {
	Count      int  `json:"count"`
	Total      int  `json:"total"`
	AtEnd      bool `json:"atEnd"`
	NoComments bool `json:"noComments"`
}

func (f *FeedDetailAction) loadAllCommentsWithConfig(page *hrod.Page, config CommentLoadConfig) error {
	loader := &commentLoader{
		page:   page,
		config: config,
		stats:  &loadStats{},
		state:  &loadState{},
	}

	return loader.load()
}

func (cl *commentLoader) load() error {
	maxAttempts := cl.calculateMaxAttempts()
	scrollInterval := getScrollInterval(cl.config.ScrollSpeed)

	logrus.Info("开始加载评论...")
	if err := scrollToCommentsArea(cl.page); err != nil {
		return err
	}
	if err := sleepRandom(cl.page, humanDelayRange.min, humanDelayRange.max); err != nil {
		return err
	}

	if cl.checkNoComments() {
		return nil
	}

	for cl.stats.attempts = 0; cl.stats.attempts < maxAttempts; cl.stats.attempts++ {
		logrus.Debugf("=== 尝试 %d/%d ===", cl.stats.attempts+1, maxAttempts)

		complete, err := cl.checkComplete()
		if err != nil {
			return err
		}
		if complete {
			return nil
		}

		if cl.shouldClickButtons() {
			if err := cl.clickButtonsWithRetry(); err != nil {
				return err
			}
		}

		currentCount := getCommentCount(cl.page)
		cl.updateState(currentCount)
		if cl.shouldStopAtTarget(currentCount) {
			return nil
		}
		// Total 到达检查：当 Total > 0 且已加载数 >= Total 时停止
		if cl.config.MaxCommentItems <= 0 {
			totalCount := getTotalCommentCount(cl.page)
			if totalCount > 0 && currentCount >= totalCount {
				logrus.Infof("✓ 已加载全部评论: %d/%d, 停止加载", currentCount, totalCount)
				return nil
			}
		}

		if err := cl.performScroll(); err != nil {
			return err
		}
		// 安全停止：停滞多轮且已有足够评论，页面可能不显示 Total 或 "THE END"
		if cl.state.stagnantChecks >= stagnantLimit && currentCount >= 10 {
			logrus.Infof("✓ 安全停止: 已加载 %d 条评论，停滞 %d 轮", currentCount, cl.state.stagnantChecks)
			return nil
		}
		if err := cl.handleStagnation(); err != nil {
			return err
		}
		if err := cl.page.Sleep(scrollInterval); err != nil {
			return err
		}
	}

	return cl.performFinalSprint()
}

type commentWheelAnchor struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// 评论懒加载依赖真实滚轮输入和最后一条评论附近的可见位置。
func (cl *commentLoader) scrollForMoreComments() error {
	return scrollCommentPageForMore(cl.page, cl.config.ScrollSpeed)
}

func scrollCommentPageForMore(page *hrod.Page, speed string) error {
	if err := moveToCommentWheelAnchor(page); err != nil {
		return err
	}
	viewportHeight, err := getViewportHeight(page)
	if err != nil {
		return err
	}
	scrollDistance := commentScrollDistance(viewportHeight, speed) + float64(rand.Intn(101)-50)
	if scrollDistance < minScrollDelta {
		scrollDistance = minScrollDelta
	}
	if err := page.Actor().Mouse.Scroll(0, scrollDistance); err != nil {
		return err
	}
	return sleepRandom(page, scrollWaitRange.min, scrollWaitRange.max)
}

func moveToCommentWheelAnchor(page *hrod.Page) error {
	comments, err := page.Rod.Timeout(2 * time.Second).Elements(".parent-comment")
	if err == nil && len(comments) > 0 {
		lastComment := hrod.NewElement(comments[len(comments)-1], page.Actor())
		if err := lastComment.ScrollIntoView(); err != nil {
			logrus.Warnf("滚动到最后一条评论失败: %v", err)
		}
	} else if container, err := page.Rod.Timeout(2 * time.Second).Element(".comments-container"); err == nil {
		commentContainer := hrod.NewElement(container, page.Actor())
		if err := commentContainer.ScrollIntoView(); err != nil {
			logrus.Warnf("滚动到评论区失败: %v", err)
		}
	}

	result, err := page.Eval(commentWheelAnchorScript())
	if err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("评论滚轮锚点脚本未返回结果")
	}

	var anchor commentWheelAnchor
	if err := json.Unmarshal([]byte(result.Value.Str()), &anchor); err != nil {
		return fmt.Errorf("解析评论滚轮锚点: %w", err)
	}

	if anchor.X == 0 && anchor.Y == 0 {
		return fmt.Errorf("未找到评论滚轮锚点")
	}

	return nil
}

func commentWheelAnchorScript() string {
	return `() => {
		const comments = document.querySelectorAll('.parent-comment');
		const container = document.querySelector('.comments-container');
		let targetX = window.innerWidth / 2;
		let targetY = window.innerHeight / 2;

		if (comments.length > 0) {
			const last = comments[comments.length - 1];
			const rect = last.getBoundingClientRect();
			targetX = rect.left + rect.width / 2;
			targetY = rect.top + rect.height / 2 + 50;
		} else if (container) {
			const rect = container.getBoundingClientRect();
			targetX = rect.left + rect.width / 2;
			targetY = rect.top + rect.height / 2;
		}

		return JSON.stringify({ x: targetX, y: targetY });
	}`
}

func (cl *commentLoader) calculateMaxAttempts() int {
	if cl.config.MaxCommentItems > 0 {
		return cl.config.MaxCommentItems * 3
	}
	return defaultMaxAttempts
}

func (cl *commentLoader) checkNoComments() bool {
	if checkNoCommentsArea(cl.page) {
		logrus.Infof("✓ 检测到无评论区域（这是一片荒地），跳过加载")
		return true
	}
	return false
}

func (cl *commentLoader) checkComplete() (bool, error) {
	if checkEndContainer(cl.page) {
		currentCount := getCommentCount(cl.page)
		logrus.Infof("✓ 检测到 'THE END' 元素，已滑动到底部")
		if err := sleepRandom(cl.page, humanDelayRange.min, humanDelayRange.max); err != nil {
			return false, err
		}
		logrus.Infof("✓ 加载完成: %d 条评论, 尝试次数: %d, 点击: %d, 跳过: %d",
			currentCount, cl.stats.attempts+1, cl.stats.totalClicked, cl.stats.totalSkipped)
		return true, nil
	}
	return false, nil
}

func (cl *commentLoader) shouldClickButtons() bool {
	return cl.config.ClickMoreReplies && cl.stats.attempts%buttonClickInterval == 0
}

func (cl *commentLoader) clickButtonsWithRetry() error {
	clicked, skipped, err := clickShowMoreButtonsSmart(cl.page, cl.config.MaxRepliesThreshold)
	if err != nil {
		return err
	}
	if clicked > 0 || skipped > 0 {
		cl.stats.totalClicked += clicked
		cl.stats.totalSkipped += skipped
		logrus.Infof("点击'更多': %d 个, 跳过: %d 个, 累计点击: %d, 累计跳过: %d",
			clicked, skipped, cl.stats.totalClicked, cl.stats.totalSkipped)

		if err := sleepRandom(cl.page, readTimeRange.min, readTimeRange.max); err != nil {
			return err
		}

		// 重试一轮
		clicked2, skipped2, err := clickShowMoreButtonsSmart(cl.page, cl.config.MaxRepliesThreshold)
		if err != nil {
			return err
		}
		if clicked2 > 0 || skipped2 > 0 {
			cl.stats.totalClicked += clicked2
			cl.stats.totalSkipped += skipped2
			logrus.Infof("第 2 轮: 点击 %d, 跳过 %d", clicked2, skipped2)
			if err := sleepRandom(cl.page, shortReadRange.min, shortReadRange.max); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cl *commentLoader) updateState(currentCount int) {
	totalCount := getTotalCommentCount(cl.page)
	logrus.Debugf("当前评论: %d, 目标: %d", currentCount, totalCount)

	if currentCount != cl.state.lastCount {
		logrus.Infof("✓ 评论增加: %d -> %d (+%d)",
			cl.state.lastCount, currentCount, currentCount-cl.state.lastCount)
		cl.state.lastCount = currentCount
		cl.state.stagnantChecks = 0
	} else {
		cl.state.stagnantChecks++
		if cl.state.stagnantChecks%5 == 0 {
			logrus.Debugf("评论停滞 %d 次", cl.state.stagnantChecks)
		}
	}
}

func (cl *commentLoader) shouldStopAtTarget(currentCount int) bool {
	// 如果未设置最大评论数，或者还未达到目标，继续加载
	if cl.config.MaxCommentItems <= 0 {
		return false
	}

	// 如果已达到或超过目标评论数，立即停止
	if currentCount >= cl.config.MaxCommentItems {
		logrus.Infof("✓ 已达到目标评论数: %d/%d, 停止加载",
			currentCount, cl.config.MaxCommentItems)
		return true
	}

	return false
}

func (cl *commentLoader) performScroll() error {
	currentCount := getCommentCount(cl.page)
	if currentCount > 0 {
		scrollToLastComment(cl.page)
		if err := sleepRandom(cl.page, postScrollRange.min, postScrollRange.max); err != nil {
			return err
		}
	}

	largeMode := cl.state.stagnantChecks >= largeScrollTrigger
	pushCount := 1
	if largeMode {
		pushCount = 3 + rand.Intn(3)
	}

	_, scrollDelta, currentScrollTop, err := humanScroll(cl.page, cl.config.ScrollSpeed, largeMode, pushCount)
	if err != nil {
		return err
	}

	if scrollDelta < minScrollDelta || currentScrollTop == cl.state.lastScrollTop {
		cl.state.stagnantChecks++
		if cl.state.stagnantChecks%5 == 0 {
			logrus.Debugf("滚动停滞 %d 次", cl.state.stagnantChecks)
		}
	} else {
		cl.state.stagnantChecks = 0
		cl.state.lastScrollTop = currentScrollTop
	}
	return nil
}

func (cl *commentLoader) handleStagnation() error {
	if cl.state.stagnantChecks >= stagnantLimit {
		logrus.Infof("停滞过多，尝试大冲刺...")
		if _, _, _, err := humanScroll(cl.page, cl.config.ScrollSpeed, true, 10); err != nil {
			return err
		}
		cl.state.stagnantChecks = 0

		if checkEndContainer(cl.page) {
			currentCount := getCommentCount(cl.page)
			logrus.Infof("✓ 到达底部，评论数: %d", currentCount)
		}
	}
	return nil
}

func (cl *commentLoader) performFinalSprint() error {
	logrus.Infof("达到最大尝试次数，最后冲刺...")
	if _, _, _, err := humanScroll(cl.page, cl.config.ScrollSpeed, true, finalSprintPushCount); err != nil {
		return err
	}

	currentCount := getCommentCount(cl.page)
	hasEnd := checkEndContainer(cl.page)
	logrus.Infof("✓ 加载结束: %d 条评论, 点击: %d, 跳过: %d, 到达底部: %v",
		currentCount, cl.stats.totalClicked, cl.stats.totalSkipped, hasEnd)
	return nil
}

// ========== 工具函数 ==========

func sleepRandom(page *hrod.Page, minMs, maxMs int) error {
	return page.SleepRandom(time.Duration(minMs)*time.Millisecond, time.Duration(maxMs)*time.Millisecond)
}

func getScrollInterval(speed string) time.Duration {
	switch speed {
	case "slow":
		return time.Duration(1200+rand.Intn(300)) * time.Millisecond
	case "fast":
		return time.Duration(300+rand.Intn(100)) * time.Millisecond
	default: // normal
		return time.Duration(600+rand.Intn(200)) * time.Millisecond
	}
}

// ========== 按钮点击 ==========

func clickShowMoreButtonsSmart(page *hrod.Page, maxRepliesThreshold int) (int, int, error) {
	clicked, skipped := 0, 0
	buttons, err := page.Timeout(3 * time.Second).Elements(".show-more,.folding-btn,.comment-more,.load-more-reply")
	if err != nil {
		return 0, 0, nil
	}

	for _, btn := range buttons {
		btn := hrod.NewElement(btn, page.Actor())
		if err := btn.ScrollIntoView(); err != nil {
			continue
		}
		parentComment := findParentCommentElement(btn)
		replyCount := 0
		if parentComment != nil {
			replyCount = countChildReplies(parentComment)
		}
		if maxRepliesThreshold > 0 && replyCount > maxRepliesThreshold {
			skipped++
			continue
		}
		if err := btn.Click(); err == nil {
			clicked++
		}
	}
	return clicked, skipped, nil
}

func findParentCommentElement(btn *hrod.Element) *hrod.Element {
	parent := btn
	for i := 0; i < 10; i++ {
		if parent == nil {
			return nil
		}
		if strings.Contains(parent.Attribute("class"), "parent-comment") {
			return parent
		}
		parent = parent.Parent()
	}
	return nil
}

func countChildReplies(parent *hrod.Element) int {
	replyElements, err := parent.Elements(".reply,.child-comment,.sub-comment")
	if err != nil {
		return 0
	}
	return len(replyElements)
}

// ========== 滚动相关 ==========

func commentScrollDistance(viewportHeight int, speed string) float64 {
	switch speed {
	case "slow":
		return float64(viewportHeight) * 0.2
	case "fast":
		return float64(viewportHeight) * 0.8
	default:
		return float64(viewportHeight) * 0.4
	}
}

func getViewportHeight(page *hrod.Page) (int, error) {
	result, err := page.Eval(`() => window.innerHeight`)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, fmt.Errorf("获取视口高度未返回结果")
	}
	height := result.Value.Int()
	if height <= 0 {
		return 1080, nil
	}
	return height, nil
}

type scrollResult struct {
	scrollTop    float64
	scrollDelta  float64
	scrollHeight float64
}

func humanScroll(page *hrod.Page, speed string, largeMode bool, pushCount int) (float64, float64, int, error) {
	maxScrollDelta := 300.0
	if largeMode {
		maxScrollDelta = 1000.0
	}
	delta := maxScrollDelta*0.5 + maxScrollDelta*0.5*rand.Float64()
	delta = delta * float64(pushCount)
	result, err := page.Eval(fmt.Sprintf(`() => {
		const st = window.pageYOffset || document.documentElement.scrollTop || document.body.scrollTop || 0;
		window.scrollBy(0, %f);
		return JSON.stringify({scrollTop: st, scrollDelta: %f, scrollHeight: document.body.scrollHeight});
	}`, delta, delta))
	if err != nil {
		return 0, 0, 0, err
	}
	if result == nil {
		return 0, 0, 0, fmt.Errorf("humanScroll 未返回结果")
	}
	var sr scrollResult
	if err := json.Unmarshal([]byte(result.Value.Str()), &sr); err != nil {
		return 0, 0, 0, err
	}
	return sr.scrollTop, sr.scrollDelta, int(sr.scrollTop), nil
}

func scrollToCommentsArea(page *hrod.Page) error {
	commentsContainer, err := page.Timeout(5 * time.Second).Element(".comments-container")
	if err == nil {
		container := hrod.NewElement(commentsContainer, page.Actor())
		return container.ScrollIntoView()
	}
	commentArea, err := page.Timeout(3 * time.Second).Element(".comment-area,.comment-list")
	if err == nil {
		area := hrod.NewElement(commentArea, page.Actor())
		return area.ScrollIntoView()
	}
	logrus.Warnf("未找到评论区，尝试滚动到页面底部")
	if err := page.Actor().Mouse.Scroll(0, 500); err != nil {
		return err
	}
	return nil
}

func scrollToLastComment(page *hrod.Page) {
	// 获取所有主评论元素
	elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment")
	if err != nil || len(elements) == 0 {
		return
	}
	// 滚动到最后一个评论
	lastComment := elements[len(elements)-1]
	if err := lastComment.ScrollIntoView(); err != nil {
		logrus.Warnf("滚动到最后一条评论失败: %v", err)
	}
}

// ========== DOM 查询 ==========

func getCommentProgress(page *hrod.Page) (commentProgress, error) {
	var progress commentProgress

	result, err := page.Eval(`() => {
		const totalEl = document.querySelector(".comments-container .total") ||
		                document.querySelector(".comment-total") ||
		                document.querySelector(".total");
		const totalText = totalEl?.innerText || "";
		const totalMatch = totalText.match(/共\s*(\d+)\s*条评论/);
		const endText = document.querySelector(".end-container")?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";

		return JSON.stringify({
			count: document.querySelectorAll(".parent-comment").length,
			total: totalMatch ? Number(totalMatch[1]) : 0,
			atEnd: /THE\s*END/i.test(endText),
			noComments: noCommentsText.includes("这是一片荒地"),
		});
	}`)
	if err != nil {
		return progress, err
	}
	if result == nil {
		return progress, fmt.Errorf("读取评论加载状态未返回结果")
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &progress); err != nil {
		return progress, fmt.Errorf("解析评论加载状态: %w", err)
	}
	return progress, nil
}

func getCommentCount(page *hrod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 使用 Go 获取评论元素
			elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment")
			if err != nil {
				return err
			}
			result = len(elements)
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取评论计数重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取评论计数失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

func getTotalCommentCount(page *hrod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 尝试多个选择器获取总评论数元素
			totalEl, err := page.Timeout(2 * time.Second).Element(".comments-container .total")
			if err != nil {
				// 备用选择器
				totalEl, err = page.Timeout(1 * time.Second).Element(".comment-total")
				if err != nil {
					totalEl, err = page.Timeout(1 * time.Second).Element(".total")
				}
			}
			if err != nil {
				return err
			}

			// 获取文本内容
			text, err := totalEl.Text()
			if err != nil {
				return err
			}

			// 使用正则提取数字
			re := regexp.MustCompile(`共(\d+)条评论`)
			matches := re.FindStringSubmatch(text)
			if len(matches) > 1 {
				count, err := strconv.Atoi(matches[1])
				if err != nil {
					return err
				}
				result = count
			} else {
				result = 0
			}

			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取总评论计数重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取总评论计数失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

func getScrollTop(page *hrod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			evalResult, err := page.Eval(`() => {
				return window.pageYOffset || document.documentElement.scrollTop || document.body.scrollTop || 0;
			}`)
			if err != nil {
				return err
			}
			if evalResult == nil {
				return fmt.Errorf("读取滚动位置未返回结果")
			}

			result = evalResult.Value.Int()
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("读取滚动位置重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("读取滚动位置失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

// ========== 页面检查 ==========

func checkEndContainer(page *hrod.Page) bool {
	_, err := page.Timeout(1 * time.Second).Element(".end-container")
	return err == nil
}

func checkNoCommentsArea(page *hrod.Page) bool {
	_, err := page.Timeout(1 * time.Second).Element(".no-comments-text")
	return err == nil
}

func checkPageAccessible(page *hrod.Page) error {
	title, err := page.Rod.Timeout(5 * time.Second).Eval(`() => document.title`)
	if err != nil {
		return fmt.Errorf("页面不可访问: %w", err)
	}
	if title.Value.Str() == "" || title.Value.Str() == "页面不存在" {
		return errors.New(errors.ErrNoteNotFound, "笔记不存在或已被删除")
	}
	return nil
}

func waitForPage(page *hrod.Page) error {
	// 使用 retry-go 实现页面等待逻辑
	return retry.Do(
		func() error {
			ready, err := page.Rod.Timeout(3 * time.Second).HasR("body")
			if err != nil {
				return err
			}
			if !ready {
				return fmt.Errorf("页面尚未就绪")
			}
			return nil
		},
		retry.Attempts(5),
		retry.Delay(500*time.Millisecond),
		retry.MaxJitter(1000*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("等待页面重试 #%d: %v", n, err)
		}),
	)
}

// ========== 数据提取 ==========

func (f *FeedDetailAction) extractFeedDetail(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	return extractFeedDetailFromPage(page, feedID)
}

// extractFeedDetailFromPage 从页面提取Feed详情
func extractFeedDetailFromPage(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	result, err := page.Eval(feedDetailExtractScript)
	if err != nil {
		return nil, fmt.Errorf("执行提取脚本失败: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("提取脚本未返回结果")
	}

	var response FeedDetailResponse
	if err := json.Unmarshal([]byte(result.Value.Str()), &response); err != nil {
		return nil, fmt.Errorf("解析提取结果失败: %w", err)
	}

	// 处理图片 URL
	for i := range response.Images {
		if response.Images[i] == "" {
			response.Images = append(response.Images[:i], response.Images[i+1:]...)
			i--
		}
	}

	if feedID != "" {
		response.FeedID = feedID
	}

	return &response, nil
}

func shouldUseInitialCommentSnapshot(initial, detail *FeedDetailResponse) bool {
	if initial == nil || detail == nil {
		return false
	}
	// 如果加载评论后的详情没有评论数据，但加载前有，则使用加载前的
	if len(initial.Comments) > 0 && len(detail.Comments) == 0 {
		return true
	}
	// 如果加载后评论数明显减少（异常情况），也使用加载前的
	if len(initial.Comments) > 0 && len(detail.Comments) < len(initial.Comments)/2 {
		return true
	}
	return false
}

// 导航到Feed详情页
func navigateToFeedDetail(page *hrod.Page, feedID, xsecToken string) error {
	return retry.Do(
		func() error {
			return page.Navigate(fmt.Sprintf("https://www.xiaohongshu.com/explore/%s?xsec_token=%s", feedID, xsecToken))
		},
		retry.Attempts(3),
		retry.Delay(500*time.Millisecond),
		retry.MaxJitter(1000*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("页面导航重试 #%d: %v", n, err)
		}),
	)
}

// 提取脚本
const feedDetailExtractScript = `() => {
	// 提取笔记标题
	const titleEl = document.querySelector("#detail-title,.note-title,.title,.article-title");
	const title = titleEl ? titleEl.textContent.trim() : "";

	// 提取笔记内容
	const descEl = document.querySelector(".desc,.content,.note-text,.article-content");
	const desc = descEl ? descEl.textContent.trim() : "";

	// 提取作者信息
	const authorEl = document.querySelector(".author,.username,.user-name,.article-author");
	const authorName = authorEl ? authorEl.textContent.trim() : "";

	// 提取图片
	const images = [];
	document.querySelectorAll("img.note-image,.carousel img,.swiper img,.article-image").forEach(img => {
		const src = img.getAttribute("src") || img.getAttribute("data-src") || "";
		if (src && !images.includes(src)) {
			images.push(src);
		}
	});

	// 提取互动数据
	const likeEl = document.querySelector(".like-wrapper .count,.like-count,.interact-count");
	const likeCount = likeEl ? likeEl.textContent.trim() : "0";
	const collectEl = document.querySelector(".collect-wrapper .count,.collect-count,.fav-count");
	const collectCount = collectEl ? collectEl.textContent.trim() : "0";
	const shareEl = document.querySelector(".share-wrapper .count,.share-count");
	const shareCount = shareEl ? shareEl.textContent.trim() : "0";

	// 提取评论
	const comments = [];
	document.querySelectorAll(".parent-comment").forEach(comment => {
		const userEl = comment.querySelector(".comment-user,.username,.author");
		const contentEl = comment.querySelector(".comment-content,.content,.text");
		const timeEl = comment.querySelector(".comment-time,.time,.date");
		comments.push({
			user: userEl ? userEl.textContent.trim() : "",
			content: contentEl ? contentEl.textContent.trim() : "",
			time: timeEl ? timeEl.textContent.trim() : "",
		});
	});

	return JSON.stringify({
		feedID: "",
		title: title,
		content: desc,
		author: authorName,
		authorID: "",
		images: images,
		likeCount: likeCount,
		collectCount: collectCount,
		shareCount: shareCount,
		comments: comments,
	});
}`
