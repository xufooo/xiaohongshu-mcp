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
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// ========== 配置常量 ==========
const (
	defaultMaxAttempts     = 500
	stagnantLimit          = 20
	minScrollDelta         = 10
	maxClickPerRound       = 3
	stagnantCheckThreshold = 2 // 达到目标后需要停滞几次才确认
	largeScrollTrigger     = 5 // 停滞多少次后触发大滚动
	buttonClickInterval    = 3 // 每隔多少次尝试点击一次按钮
	finalSprintPushCount   = 15
	commentPollInterval    = 100 * time.Millisecond
	commentProgressTimeout = 3 * time.Second
	maxNoProgressRounds    = 3
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
		ScrollSpeed:         "fast",
	}
}

type FeedDetailAction struct {
	page *hrod.Page
}

func NewFeedDetailAction(page *hrod.Page) *FeedDetailAction {
	return &FeedDetailAction{page: page}
}

// ========== 主要业务逻辑 ==========

func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	return f.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
}

func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	config = normalizeCommentLoadConfig(config)
	page := f.page.Context(ctx).Timeout(feedDetailPageTimeout)
	url := makeFeedDetailURL(feedID, xsecToken)

	logrus.Infof("打开 feed 详情页: %s", url)
	logrus.Infof("配置: 点击更多=%v, 回复阈值=%d, 最大评论数=%d, 滚动速度=%s",
		config.ClickMoreReplies, config.MaxRepliesThreshold, config.MaxCommentItems, config.ScrollSpeed)

	// XHS continuously mutates the document after navigation. Waiting for DOM
	// stability can therefore consume the full request deadline even though the
	// note state is already available.
	err := retry.Do(
		func() error {
			if err := page.Navigate(url); err != nil {
				return fmt.Errorf("navigate feed detail: %w", err)
			}
			if err := page.WaitLoad(); err != nil {
				return fmt.Errorf("wait for feed detail load: %w", err)
			}
			return nil
		},
		retry.Attempts(3),
		retry.Delay(500*time.Millisecond),
		retry.MaxJitter(1000*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("页面导航重试 #%d: %v", n, err)
		}),
	)
	if err != nil {
		logrus.Errorf("页面导航失败: %v", err)
		return nil, err
	}
	if err := sleepRandom(page, 1000, 1000); err != nil {
		return nil, err
	}

	if err := checkPageAccessible(page); err != nil {
		return nil, err
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

	// Bound the scroll loop independently so the main page context remains
	// usable for extracting a partial result after comment loading times out.
	commentPage := page.Timeout(commentLoadTimeout)
	if err := f.loadAllCommentsWithConfig(commentPage, config); err != nil {
		logrus.Warnf("加载全部评论失败: %v", err)
	}

	detail, err := f.extractFeedDetail(page, feedID)
	if err != nil {
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

	logrus.Info("开始加载评论...")
	if err := scrollToCommentsArea(cl.page); err != nil {
		return err
	}

	progress, err := getCommentProgress(cl.page)
	if err != nil {
		return err
	}
	if progress.NoComments {
		logrus.Infof("✓ 检测到无评论区域（这是一片荒地），跳过加载")
		return nil
	}
	if cl.shouldStop(progress) {
		cl.logComplete(progress)
		return nil
	}

	noProgressRounds := 0

	for cl.stats.attempts = 0; cl.stats.attempts < maxAttempts; cl.stats.attempts++ {
		logrus.Debugf("=== 尝试 %d/%d ===", cl.stats.attempts+1, maxAttempts)

		if cl.shouldClickButtons() {
			if err := cl.clickButtonsWithRetry(); err != nil {
				return err
			}
		}

		if err := cl.scrollForMoreComments(); err != nil {
			return err
		}

		next, advanced, err := cl.waitForCommentProgress(progress)
		if err != nil {
			return err
		}
		progress = next
		if cl.shouldStop(progress) {
			cl.logComplete(progress)
			return nil
		}
		if advanced {
			noProgressRounds = 0
			continue
		}

		noProgressRounds++
		if noProgressRounds >= maxNoProgressRounds {
			return fmt.Errorf("加载评论停滞: 已加载 %d 条，页面未报告结束", progress.Count)
		}
	}

	return fmt.Errorf("达到评论加载最大尝试次数: 已加载 %d 条", progress.Count)
}

func (cl *commentLoader) shouldStop(progress commentProgress) bool {
	if progress.AtEnd {
		return true
	}
	if cl.config.MaxCommentItems > 0 && progress.Count >= cl.config.MaxCommentItems {
		logrus.Infof("✓ 已达到目标评论数: %d/%d, 停止加载",
			progress.Count, cl.config.MaxCommentItems)
		return true
	}
	return progress.Total > 0 && progress.Count >= progress.Total
}

func (cl *commentLoader) logComplete(progress commentProgress) {
	logrus.Infof("✓ 加载完成: %d/%d 条评论, 尝试次数: %d, 点击: %d, 跳过: %d",
		progress.Count, progress.Total, cl.stats.attempts, cl.stats.totalClicked, cl.stats.totalSkipped)
}

// scrollForMoreComments uses a direct browser scroll instead of the humanized
// scrolling path. Comment loading is a read-only operation; the old path added
// 300--800ms of artificial delay plus several browser round trips per attempt.
func (cl *commentLoader) scrollForMoreComments() error {
	multiplier := commentScrollMultiplier(cl.config.ScrollSpeed)

	_, err := cl.page.Eval(fmt.Sprintf(`() => {
		// Keep the last loaded comment in view before advancing. This makes the
		// page's intersection-based lazy loader reliable.
		const comments = document.querySelectorAll(".parent-comment");
		comments[comments.length - 1]?.scrollIntoView({ block: "end", behavior: "auto" });
		const distance = Math.max(window.innerHeight * %.1f, 900);
		const target = document.querySelector(".note-scroller") ||
			document.querySelector(".interaction-container") ||
			document.scrollingElement;
		if (target && target !== document.scrollingElement) {
			target.scrollBy({ top: distance, left: 0, behavior: "auto" });
			target.dispatchEvent(new WheelEvent("wheel", {
				deltaY: distance, deltaMode: WheelEvent.DOM_DELTA_PIXEL,
				bubbles: true, cancelable: true, view: window,
			}));
		} else {
			window.scrollBy({ top: distance, left: 0, behavior: "auto" });
		}
		return true;
	}`, multiplier))
	return err
}

func commentScrollMultiplier(speed string) float64 {
	switch speed {
	case "slow":
		return 1
	case "fast":
		return 3
	default:
		return 1.5
	}
}

// waitForCommentProgress waits only for a real page update. The observer runs
// in the browser so slow devices do not pay for a CDP round trip every poll.
// It returns as soon as a new comment or the end marker appears.
func (cl *commentLoader) waitForCommentProgress(previous commentProgress) (commentProgress, bool, error) {
	result, err := cl.page.Eval(fmt.Sprintf(`() => new Promise((resolve) => {
		const getProgress = () => {
			const totalText = document.querySelector(".comments-container .total")?.textContent || "";
			const totalMatch = totalText.match(/共\s*(\d+)\s*条评论/);
			const endText = document.querySelector(".end-container")?.textContent || "";
			const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";
			return {
				count: document.querySelectorAll(".parent-comment").length,
				total: totalMatch ? Number(totalMatch[1]) : 0,
				atEnd: /THE\s*END/i.test(endText),
				noComments: noCommentsText.includes("这是一片荒地"),
			};
		};

		let done = false;
		let observer;
		let timer;
		const finish = () => {
			if (done) return;
			done = true;
			observer?.disconnect();
			clearTimeout(timer);
			resolve(JSON.stringify(getProgress()));
		};
		const advanced = () => {
			const progress = getProgress();
			return progress.count > %d || progress.atEnd || progress.noComments;
		};
		if (advanced()) {
			finish();
			return;
		}
		observer = new MutationObserver(() => {
			const progress = getProgress();
			if (progress.count > %d || progress.atEnd || progress.noComments) finish();
		});
		observer.observe(document.body, { childList: true, subtree: true });
		if (advanced()) {
			finish();
			return;
		}
		timer = setTimeout(finish, %d);
	})`, previous.Count, previous.Count, commentProgressTimeout.Milliseconds()))
	if err != nil {
		return previous, false, err
	}
	if result == nil {
		return previous, false, fmt.Errorf("等待评论加载状态未返回结果")
	}

	var next commentProgress
	if err := json.Unmarshal([]byte(result.Value.Str()), &next); err != nil {
		return previous, false, fmt.Errorf("解析评论加载状态: %w", err)
	}
	return next, next.Count > previous.Count, nil
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

func clickShowMoreButtonsSmart(page *hrod.Page, maxRepliesThreshold int) (clicked, skipped int, err error) {
	elements, err := page.Elements(".show-more")
	if err != nil {
		return 0, 0, page.Err()
	}

	replyCountRegex := regexp.MustCompile(`展开\s*(\d+)\s*条回复`)
	maxClick := maxClickPerRound + rand.Intn(maxClickPerRound)
	clickedInRound := 0

	for _, el := range elements {
		if clickedInRound >= maxClick {
			break
		}

		if !isElementClickable(el) {
			continue
		}

		text, err := el.Text()
		if err != nil {
			continue
		}

		if shouldSkipButton(text, maxRepliesThreshold, replyCountRegex) {
			skipped++
			continue
		}

		clickSuccess, err := clickElementWithHumanBehavior(page, el, text)
		if err != nil {
			return clicked, skipped, err
		}
		if clickSuccess {
			clicked++
			clickedInRound++
		}
	}

	return clicked, skipped, nil
}

func isElementClickable(el *hrod.Element) bool {
	visible, err := el.Visible()
	if err != nil || !visible {
		return false
	}

	box, err := el.Shape()
	return err == nil && len(box.Quads) > 0
}

func shouldSkipButton(text string, threshold int, regex *regexp.Regexp) bool {
	if threshold <= 0 {
		return false
	}

	matches := regex.FindStringSubmatch(text)
	if len(matches) > 1 {
		if replyCount, err := strconv.Atoi(matches[1]); err == nil && replyCount > threshold {
			logrus.Debugf("跳过'%s'（回复数 %d > 阈值 %d）", text, replyCount, threshold)
			return true
		}
	}
	return false
}

func clickElementWithHumanBehavior(page *hrod.Page, el *hrod.Element, text string) (bool, error) {
	var clickSuccess bool

	// 使用retry-go进行点击操作重试
	err := retry.Do(
		func() error {
			// 滚动到元素
			el.MustScrollIntoView()

			if err := sleepRandom(page, reactionTimeRange.min, reactionTimeRange.max); err != nil {
				return err
			}

			// 鼠标悬停
			if box, err := el.Shape(); err == nil && len(box.Quads) > 0 {
				x := float64(box.Quads[0][0]+box.Quads[0][4]) / 2
				y := float64(box.Quads[0][1]+box.Quads[0][5]) / 2
				if err := page.MovePoint(proto.Point{X: x, Y: y}); err != nil {
					return err
				}
				if err := sleepRandom(page, hoverTimeRange.min, hoverTimeRange.max); err != nil {
					return err
				}
			}

			// 点击
			if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return err // 返回错误以触发重试
			}

			// 模拟人类阅读时间
			if err := sleepRandom(page, readTimeRange.min, readTimeRange.max); err != nil {
				return err
			}
			clickSuccess = true
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("点击重试 #%d: %s, 错误: %v", n, text, err)
		}),
	)

	if err != nil {
		logrus.Debugf("点击失败 '%s': %v", text, err)
		return false, page.Err()
	}

	if clickSuccess {
		logrus.Debugf("点击了'%s'", text)
	}

	return clickSuccess, nil
}

// ========== 滚动相关 ==========

func humanScroll(page *hrod.Page, speed string, largeMode bool, pushCount int) (bool, int, int, error) {
	beforeTop := getScrollTop(page)
	viewportHeight := page.MustEval(`() => window.innerHeight`).Int()

	baseRatio := getScrollRatio(speed)
	if largeMode {
		baseRatio *= 2.0
	}

	scrolled := false
	actualDelta := 0
	currentScrollTop := beforeTop

	for i := 0; i < max(1, pushCount); i++ {
		scrollDelta := calculateScrollDelta(viewportHeight, baseRatio)
		if err := page.Actor().Mouse.Scroll(0, scrollDelta); err != nil {
			logrus.Warnf("人化滚动失败: %v", err)
		}

		if err := sleepRandom(page, scrollWaitRange.min, scrollWaitRange.max); err != nil {
			return false, 0, 0, err
		}

		currentScrollTop = getScrollTop(page)
		deltaThisTime := currentScrollTop - beforeTop
		actualDelta += deltaThisTime

		if deltaThisTime > 5 {
			scrolled = true
		}

		beforeTop = currentScrollTop

		if i < pushCount-1 {
			if err := sleepRandom(page, humanDelayRange.min, humanDelayRange.max); err != nil {
				return false, 0, 0, err
			}
		}
	}

	if !scrolled && pushCount > 0 {
		scrollHeight := page.MustEval(`() => document.body.scrollHeight`).Int()
		currentScrollTop := getScrollTop(page)
		if err := page.Actor().Mouse.Scroll(0, float64(scrollHeight-currentScrollTop)); err != nil {
			logrus.Warnf("滚动到底部失败: %v", err)
		}
		if err := sleepRandom(page, postScrollRange.min, postScrollRange.max); err != nil {
			return false, 0, 0, err
		}
		currentScrollTop = getScrollTop(page)
		actualDelta = currentScrollTop - beforeTop + actualDelta
		scrolled = actualDelta > 5
	}

	if scrolled {
		logrus.Debugf("滚动: %d -> %d (Δ%d, large=%v, push=%d)",
			beforeTop-actualDelta, currentScrollTop, actualDelta, largeMode, pushCount)
	}

	return scrolled, actualDelta, currentScrollTop, nil
}

func getScrollRatio(speed string) float64 {
	switch speed {
	case "slow":
		return 0.5
	case "fast":
		return 0.9
	default: // normal
		return 0.7
	}
}

func calculateScrollDelta(viewportHeight int, baseRatio float64) float64 {
	scrollDelta := float64(viewportHeight) * (baseRatio + rand.Float64()*0.2)
	if scrollDelta < 400 {
		scrollDelta = 400
	}
	return scrollDelta + float64(rand.Intn(100)-50)
}

func scrollToCommentsArea(page *hrod.Page) error {
	logrus.Info("滚动到评论区...")

	// 先定位到评论区
	if el, err := page.Timeout(2 * time.Second).Element(".comments-container"); err == nil {
		el.MustScrollIntoView()
	}
	// Give the browser a short opportunity to activate the comment lazy loader.
	// This is synchronization, not a humanization delay.
	if err := page.Sleep(commentPollInterval); err != nil {
		return err
	}

	// 触发一次小滚动，激活懒加载机制。评论读取无需鼠标的人化轨迹。
	_, err := page.Eval(`() => window.scrollBy({ top: 100, left: 0, behavior: "auto" })`)
	return err
}

func scrollToLastComment(page *hrod.Page) {
	// 获取所有主评论元素
	elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment")
	if err != nil || len(elements) == 0 {
		return
	}
	// 滚动到最后一个评论
	lastComment := elements[len(elements)-1]
	lastComment.MustScrollIntoView()
}

// ========== DOM 查询 ==========

func getCommentProgress(page *hrod.Page) (commentProgress, error) {
	var progress commentProgress

	result, err := page.Eval(`() => {
		const totalText = document.querySelector(".comments-container .total")?.textContent || "";
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

func getScrollTop(page *hrod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			evalResult := page.MustEval(`() => {
				return window.pageYOffset || document.documentElement.scrollTop || document.body.scrollTop || 0;
			}`)

			result = evalResult.Int()
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取滚动位置重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取滚动位置失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
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
			// 使用 Go 获取总评论数元素
			totalEl, err := page.Timeout(2 * time.Second).Element(".comments-container .total")
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

func checkNoCommentsArea(page *hrod.Page) bool {
	// 查找无评论区域
	noCommentsEl, err := page.Timeout(2 * time.Second).Element(".no-comments-text")
	if err != nil {
		// 未找到无评论元素，说明有评论或评论区正常
		return false
	}

	// 获取文本内容
	text, err := noCommentsEl.Text()
	if err != nil {
		return false
	}

	// 检查是否包含"这是一片荒地"等关键词
	text = strings.TrimSpace(text)
	return strings.Contains(text, "这是一片荒地")
}

func checkEndContainer(page *hrod.Page) bool {
	var result bool

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 使用 Go 查找结束容器
			endEl, err := page.Timeout(2 * time.Second).Element(".end-container")
			if err != nil {
				// 未找到元素，说明未到底部
				result = false
				return nil
			}

			// 获取文本内容
			text, err := endEl.Text()
			if err != nil {
				result = false
				return nil
			}

			// 转换为大写并检查
			textUpper := strings.ToUpper(strings.TrimSpace(text))
			result = strings.Contains(textUpper, "THE END") || strings.Contains(textUpper, "THEEND")
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("检查结束容器重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("检查结束容器失败: %v", err)
		return false // 失败时返回false
	}

	return result
}

// ========== 页面检查 ==========

func checkPageAccessible(page *hrod.Page) error {
	if err := page.Sleep(500 * time.Millisecond); err != nil {
		return err
	}

	// 查找错误提示容器
	wrapperEl, err := page.Timeout(2 * time.Second).Element(".access-wrapper, .error-wrapper, .not-found-wrapper, .blocked-wrapper")
	if err != nil {
		// 未找到错误容器，说明页面可访问
		return nil
	}

	// 获取文本内容
	text, err := wrapperEl.Text()
	if err != nil {
		// 无法获取文本，假设页面可访问
		return nil
	}

	// 检查关键词
	keywords := []string{
		"当前笔记暂时无法浏览",
		"该内容因违规已被删除",
		"该笔记已被删除",
		"内容不存在",
		"笔记不存在",
		"已失效",
		"私密笔记",
		"仅作者可见",
		"因用户设置，你无法查看",
		"因违规无法查看",
	}

	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			logrus.Warnf("笔记不可访问: %s", kw)
			return fmt.Errorf("笔记不可访问: %s", kw)
		}
	}

	// 如果有文本但不匹配关键词，返回未知错误
	trimmedText := strings.TrimSpace(text)
	if trimmedText != "" {
		logrus.Warnf("笔记不可访问（未知原因）: %s", trimmedText)
		return fmt.Errorf("笔记不可访问: %s", trimmedText)
	}

	return nil
}

// ========== 数据提取 ==========

func (f *FeedDetailAction) extractFeedDetail(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	deadline := time.Now().Add(initialCommentStateTimeout)

	for {
		response, err := readFeedDetailState(page, feedID)
		if err != nil {
			return nil, err
		}

		// A non-zero displayed count with an empty list is a transient state while
		// the web client hydrates its comments ref. Do not return that incomplete
		// snapshot as a successful result. A genuinely empty or unavailable list
		// still returns after the short bounded wait.
		if !shouldWaitForInitialComments(response) || time.Now().After(deadline) {
			return response, nil
		}

		logrus.Debugf("评论状态尚未就绪: note=%s, reported=%s", feedID, response.Note.InteractInfo.CommentCount)
		if err := page.Sleep(commentPollInterval); err != nil {
			return nil, err
		}
	}
}

// readFeedDetailState normalizes Vue refs before serializing the state. The
// site has used both direct values and ref wrappers (value/_value) for
// noteDetailMap and comments. json.Unmarshal silently turns a wrapped comments
// value into an empty CommentList, so unwrapping must happen in the page.
func readFeedDetailState(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	var response *FeedDetailResponse
	err := retry.Do(
		func() error {
			var err error
			response, err = readFeedDetailStateOnce(page, feedID)
			return err
		},
		retry.Attempts(3),
		retry.Delay(200*time.Millisecond),
		retry.MaxJitter(300*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("提取Feed详情重试 #%d: %v", n, err)
		}),
	)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func readFeedDetailStateOnce(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	result, err := page.Eval(`(feedID) => {
		const hasOwn = (value, key) => Object.prototype.hasOwnProperty.call(value, key);
		const isObject = (value) => value !== null && typeof value === "object";

		const unwrapRef = (value) => {
			const seen = new Set();
			let current = value;
			while (isObject(current) && !seen.has(current)) {
				seen.add(current);
				if ((current.__v_isReactive || current.__v_isReadonly) &&
					isObject(current.__v_raw) && current.__v_raw !== current) {
					current = current.__v_raw;
					continue;
				}
				if (current.__v_isRef === true) {
					const next = current.value;
					if (next === current) break;
					current = next;
					continue;
				}
				if (hasOwn(current, "_value")) {
					const next = current._value;
					if (next === current) break;
					current = next;
					continue;
				}
				if (hasOwn(current, "value")) {
					const next = current.value;
					if (next === current) break;
					current = next;
					continue;
				}
				break;
			}
			return current;
		};

		// JSON.stringify invokes getters and proxy traps. Its replacer also sees
		// nested refs which are not covered by unwrapping just note/comments.
		// Parsing the JSON result makes the evaluated value a plain, deep snapshot
		// before it crosses the Go/CDP boundary.
		const snapshot = (value) => {
			const json = JSON.stringify(unwrapRef(value), (_key, nested) => unwrapRef(nested));
			return json === undefined ? undefined : JSON.parse(json);
		};

		const state = window.__INITIAL_STATE__;
		const noteState = unwrapRef(state?.note);
		const noteDetailMap = unwrapRef(noteState?.noteDetailMap);
		const detail = unwrapRef(noteDetailMap?.[feedID]);
		if (!detail) return "";

		return JSON.stringify(snapshot({
			note: detail.note,
			comments: detail.comments,
		}));
	}`, feedID)
	if err != nil {
		return nil, fmt.Errorf("提取Feed详情失败: %w", err)
	}
	if result == nil || result.Value.Str() == "" {
		return nil, errors.ErrNoFeedDetail
	}

	var noteDetail struct {
		Note     FeedDetail  `json:"note"`
		Comments CommentList `json:"comments"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &noteDetail); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feed detail: %w", err)
	}

	return &FeedDetailResponse{Note: noteDetail.Note, Comments: noteDetail.Comments}, nil
}

func shouldWaitForInitialComments(response *FeedDetailResponse) bool {
	if len(response.Comments.List) != 0 {
		return false
	}
	commentCount, err := strconv.Atoi(strings.TrimSpace(response.Note.InteractInfo.CommentCount))
	return err == nil && commentCount > 0
}

func shouldUseInitialCommentSnapshot(initial, current *FeedDetailResponse) bool {
	return initial != nil && current != nil &&
		len(initial.Comments.List) > 0 && len(current.Comments.List) == 0
}

func makeFeedDetailURL(feedID, xsecToken string) string {
	return fmt.Sprintf("https://www.xiaohongshu.com/explore/%s?xsec_token=%s&xsec_source=pc_feed", feedID, xsecToken)
}
