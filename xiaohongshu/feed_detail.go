package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// ========== 配置常量 ==========
const (
	commentPollInterval       = 100 * time.Millisecond
	replyExpansionRetryDelay  = time.Second
	// The note is available before the asynchronously populated comment ref on
	// some versions of the web client. Keep this short: it is only used when
	// the note reports comments but the state snapshot has none.
	initialCommentStateTimeout = 5 * time.Second
)

// showMoreButtonSelector 是"展开子评论"按钮的全局选择器，必须与 JS flatMap 选择器一致，
// 保证 Eval 返回的 btnIndex 与 page.Elements 返回的顺序一一对应。
const showMoreButtonSelector = ".parent-comment > .children-comments .show-more, .parent-comment > .reply-container .show-more"

const (
	feedDetailPageTimeout = 10 * time.Minute
	commentLoadTimeout    = 9 * time.Minute
)

// ========== 数据结构 ==========

type CommentLoadConfig struct {
	ClickMoreReplies    bool
	MaxRepliesThreshold int
	MaxCommentItems     int
	ScrollSpeed         string
}

type CommentCursor struct {
	FeedID      string    `json:"feed_id"`
	Round       int       `json:"round"`        // 已完成的滚动轮次
	ReturnedIDs []string  `json:"returned_ids"` // 已返回的评论ID
	ExpandRound int       `json:"expand_round"` // 已完成的展开轮次
	CreatedAt   time.Time `json:"created_at"`
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

func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	return f.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
}

func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return nil, err
	}

	config = normalizeCommentLoadConfig(config)
	page := f.page.Context(ctx).Timeout(feedDetailPageTimeout)

	logrus.Infof("从卡片打开 feed 详情页: %s", feedID)
	logrus.Infof("配置: 点击更多=%v, 回复阈值=%d, 最大评论数=%d, 滚动速度=%s",
		config.ClickMoreReplies, config.MaxRepliesThreshold, config.MaxCommentItems, config.ScrollSpeed)

	opener := NewNoteOpenActionWithState(page, f.state)
	if err := opener.OpenFromCards(ctx, feedID, xsecToken, ""); err != nil {
		return nil, fmt.Errorf("从卡片打开笔记失败，请重新搜索或滚动后重试: %w", err)
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
	commentLoadErr := loadCommentsByJS(commentPage, config)
	reader.RecordCommentDwell(feedID, time.Since(commentStart), true)
	if commentLoadErr != nil {
		if strings.Contains(commentLoadErr.Error(), "context deadline exceeded") ||
			strings.Contains(commentLoadErr.Error(), "timeout") {
			logrus.Warnf("评论加载超时(%s)，返回已加载数据: %v", time.Since(commentStart).Round(time.Second), commentLoadErr)
		} else {
			logrus.Warnf("加载全部评论失败，返回已加载数据: %v", commentLoadErr)
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

func (f *FeedDetailAction) GetFeedDetailCommentsBatch(ctx context.Context, feedID, xsecToken string, cursor *CommentCursor, maxItems int, config CommentLoadConfig) (*FeedDetailResponse, *CommentCursor, bool, error) {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return nil, nil, false, err
	}

	config = normalizeCommentLoadConfig(config)
	page := f.page.Context(ctx).Timeout(feedDetailPageTimeout)

	logrus.Infof("从卡片打开 feed 详情页(评论分批): %s", feedID)
	opener := NewNoteOpenActionWithState(page, f.state)
	if err := opener.OpenFromCards(ctx, feedID, xsecToken, ""); err != nil {
		return nil, nil, false, fmt.Errorf("从卡片打开笔记失败，请重新搜索或滚动后重试: %w", err)
	}
	if err := sleepRandom(page, 1000, 1000); err != nil {
		return nil, nil, false, err
	}
	if err := checkPageAccessible(page); err != nil {
		return nil, nil, false, err
	}

	reader := NewReadStageAction(page, f.state)
	if err := reader.Read(ctx, feedID, 5*time.Second); err != nil {
		return nil, nil, false, fmt.Errorf("阅读阶段失败: %w", err)
	}

	detail, err := f.extractFeedDetail(page, feedID)
	if err != nil {
		return nil, nil, false, err
	}

	commentPage := page.Timeout(commentLoadTimeout)
	commentStart := time.Now()
	comments, nextCursor, hasMore, err := LoadCommentsBatch(ctx, commentPage, config, cursor, maxItems)
	reader.RecordCommentDwell(feedID, time.Since(commentStart), true)
	if err != nil {
		return nil, nil, false, err
	}
	if nextCursor != nil && nextCursor.FeedID == "" {
		nextCursor.FeedID = feedID
	}

	detail.Comments = CommentList{
		List:    comments,
		HasMore: hasMore,
	}
	if totalItems := knownCommentTotal(commentPage); totalItems > 0 {
		detail.Comments.TotalItems = totalItems
	}
	return detail, nextCursor, hasMore, nil
}

func knownCommentTotal(page *hrod.Page) int {
	progress, err := getCommentProgress(page)
	if err != nil || progress.Total <= 0 {
		return 0
	}
	return progress.Total
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
	config.ClickMoreReplies = true
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

// commentProgress is collected in one browser evaluation. Keeping the check in
// the browser avoids several round trips per scroll on slower devices.
type commentProgress struct {
	Count      int  `json:"count"`
	Total      int  `json:"total"`
	AtEnd      bool `json:"atEnd"`
	NoComments bool `json:"noComments"`
}

func loadCommentsByJS(page *hrod.Page, config CommentLoadConfig) error {
	logrus.Info("开始加载评论(note-scroller JS scrollBy)...")
	logrus.Infof("配置: maxItems=%d, speed=%s", config.MaxCommentItems, config.ScrollSpeed)

	await, scrollDelta := commentScrollSettings(config.ScrollSpeed)
	maxRounds := 500
	commentDeadline := time.Now().Add(commentLoadTimeout)
	remainingDeadline := func() time.Duration {
		return time.Until(commentDeadline)
	}

	// 先确保评论区可见
	if err := scrollToCommentsArea(page); err != nil {
		logrus.Warnf("定位评论区失败: %v", err)
	}
	if err := page.Sleep(time.Second); err != nil {
		return err
	}

	lastCount := -1
	staleChecks := 0
	const maxStaleChecks = 20 // 连续20次无增长即退出

	progressFunc := func() (int, bool, bool) {
		result, err := page.Timeout(2*time.Second).Eval(commentProgressScript())
		if err != nil {
			logrus.Warnf("评论进度检查失败: %v", err)
			return lastCount, false, false
		}
		if result == nil {
			return lastCount, false, false
		}
		var p struct {
			Count      int  `json:"count"`
			AtEnd      bool `json:"atEnd"`
			NoComments bool `json:"noComments"`
		}
		if err := json.Unmarshal([]byte(result.Value.Str()), &p); err != nil {
			return lastCount, false, false
		}
		return p.Count, p.AtEnd, p.NoComments
	}

	for i := 0; i < maxRounds; i++ {
		if remaining := remainingDeadline(); remaining < 30*time.Second {
			logrus.Warnf("评论加载剩余时间不足(%s)，停止新滚动", remaining.Round(time.Second))
			break
		}
		moved, scrollErr := scrollNoteScrollerMoved(page, scrollDelta)
		if scrollErr != nil {
			return fmt.Errorf("评论容器滚动失败: %w", scrollErr)
		}
		if err := page.Sleep(await); err != nil {
			return err
		}
		if !moved {
			count, atEnd, noComments := progressFunc()
			if noComments || atEnd {
				logrus.Infof("评论容器到达末尾: count=%d", count)
				break
			}
			return fmt.Errorf("评论容器未推进且尚未出现 end 标识")
		}

		// 每5轮检查一次进度（非致命，超时继续）
		if i%5 == 0 {
			if config.ClickMoreReplies {
				button, err := nextShowMoreButton(page, config.MaxRepliesThreshold, true)
				if err != nil {
					logrus.Warnf("检查可见子评论展开按钮失败: %v", err)
				} else if button != nil {
					logrus.Infof("滚动中点击展开子评论: %s", button.Text)
					if err := clickShowMoreButton(page, button); err != nil {
						logrus.Warnf("滚动中展开子评论失败: %v", err)
					}
				}
			}

			count, atEnd, noComments := progressFunc()
			if noComments {
				logrus.Info("✓ 笔记无评论（荒地），跳过加载")
				return nil
			}
			if atEnd {
				logrus.Infof("✓ 检测到评论已到底: count=%d", count)
				break
			}
			logrus.Debugf("评论进度: %d/%d, atEnd=%v", count, config.MaxCommentItems, atEnd)

			if config.MaxCommentItems > 0 && count >= config.MaxCommentItems {
				logrus.Infof("✓ 已达到目标评论数: %d", count)
				break
			}

			if count > lastCount {
				lastCount = count
				staleChecks = 0
			} else {
				staleChecks++
				if staleChecks >= maxStaleChecks {
					logrus.Infof("✓ 评论数连续%d轮无增长(%d)，停止", staleChecks*5, count)
					break
				}
			}
		}
	}

	if config.ClickMoreReplies {
		if remaining := remainingDeadline(); remaining < 15*time.Second {
			logrus.Warnf("评论加载剩余时间不足(%s)，跳过末尾子评论展开", remaining.Round(time.Second))
			logrus.Info("✓ 评论加载流程结束")
			return nil
		}
		if err := clickMoreReplies(page, config.MaxRepliesThreshold, remainingDeadline); err != nil {
			return err
		}
	}

	logrus.Info("✓ 评论加载流程结束")
	return nil
}

func LoadCommentsBatch(ctx context.Context, page *hrod.Page, config CommentLoadConfig, cursor *CommentCursor, maxItems int) ([]Comment, *CommentCursor, bool, error) {
	config = normalizeCommentLoadConfig(config)
	if maxItems <= 0 {
		maxItems = 20
	}

	logrus.Infof("开始分批加载评论: maxItems=%d", maxItems)
	await, scrollDelta := commentScrollSettings(config.ScrollSpeed)
	deadline := commentLoadDeadline(ctx)
	remaining := func() time.Duration { return time.Until(deadline) }

	feedID := ""
	batchCursor := &CommentCursor{CreatedAt: time.Now()}
	returned := make(map[string]struct{})
	if cursor != nil {
		feedID = cursor.FeedID
		batchCursor.FeedID = cursor.FeedID
		batchCursor.Round = cursor.Round
		batchCursor.ExpandRound = cursor.ExpandRound
		batchCursor.CreatedAt = cursor.CreatedAt
		if batchCursor.CreatedAt.IsZero() {
			batchCursor.CreatedAt = time.Now()
		}
		for _, id := range cursor.ReturnedIDs {
			if id == "" || strings.HasPrefix(id, "idx_") {
				continue
			}
			if _, ok := returned[id]; ok {
				continue
			}
			returned[id] = struct{}{}
			batchCursor.ReturnedIDs = append(batchCursor.ReturnedIDs, id)
		}
	}
	if feedID == "" {
		if id, err := currentFeedIDFromPage(page); err == nil {
			feedID = id
			batchCursor.FeedID = id
		}
	}

	if batchCursor.Round == 0 {
		if err := scrollToCommentsArea(page); err != nil {
			return nil, nil, false, fmt.Errorf("定位评论区失败: %w", err)
		}
		moved, err := scrollNoteScrollerMoved(page, 160)
		if err != nil {
			return nil, nil, false, fmt.Errorf("初始滚动触发评论懒加载失败: %w", err)
		}
		if moved {
			batchCursor.Round++
		}
		if err := page.Sleep(await); err != nil {
			return nil, nil, false, err
		}
		if err := page.Sleep(time.Second); err != nil {
			return nil, nil, false, err
		}
	}

	collect := func(limit int) ([]Comment, bool, error) {
		if limit <= 0 {
			return nil, true, nil
		}
		comments, err := ExtractCommentsFromDOM(page, feedID)
		if err != nil {
			return nil, false, err
		}
		flat := flattenComments(comments)
		var batch []Comment
		for i, comment := range flat {
			key := commentBatchKey(i, comment)
			if key == "" {
				continue
			}
			if _, ok := returned[key]; ok {
				continue
			}
			if len(batch) >= limit {
				return batch, true, nil
			}
			returned[key] = struct{}{}
			batchCursor.ReturnedIDs = append(batchCursor.ReturnedIDs, key)
			batch = append(batch, comment)
		}
		return batch, false, nil
	}

	var batch []Comment
	replyClicksTotal := 0
	replyStall := false

	inputBase := len(batchCursor.ReturnedIDs)

	// 本次 cursor 确有增长且 ctx 正常时返回 partial success，否则返回原错误；
	// ctx canceled/deadline exceeded 不得转换为成功。
	partialOrError := func(err error) ([]Comment, *CommentCursor, bool, error) {
		if len(batchCursor.ReturnedIDs) > inputBase && ctx.Err() == nil {
			return batch, batchCursor, true, nil
		}
		return nil, nil, false, err
	}

	more, moreVisible, collectErr := collect(maxItems)
	if collectErr != nil {
		return nil, nil, false, collectErr
	}
	batch = append(batch, more...)

	progress, progressErr := getCommentProgress(page)
	if progressErr != nil {
		return partialOrError(fmt.Errorf("评论进度读取失败: %w", progressErr))
	}

	for i := 0; i < 500; i++ {
		if rem := remaining(); rem < 15*time.Second {
			logrus.Warnf("评论分批加载剩余时间不足(%s)，停止新操作", rem.Round(time.Second))
			break
		}

		if config.ClickMoreReplies {
			button, err := nextShowMoreButton(page, config.MaxRepliesThreshold, false)
			if err != nil {
				return partialOrError(fmt.Errorf("查询展开按钮失败: %w", err))
			}
			if button != nil {
				if len(batch) >= maxItems {
					return batch, batchCursor, true, nil
				}
				if !replyStall && replyClicksTotal < 8 {
					before, countErr := countReplyItems(page, button.ParentIndex)
					if countErr != nil {
						return partialOrError(fmt.Errorf("回复计数失败: %w", countErr))
					}
					if err := clickShowMoreButton(page.Context(ctx), button); err != nil {
						return partialOrError(fmt.Errorf("回复展开点击失败: %w", err))
					}
					replyClicksTotal++
					batchCursor.ExpandRound++
					if waitErr := waitReplyItemsChanged(page, button.ParentIndex, before, 3*time.Second); waitErr != nil {
						replyStall = true
					}
					more, moreVisible, collectErr = collect(maxItems - len(batch))
					if collectErr != nil {
						return partialOrError(collectErr)
					}
					batch = append(batch, more...)
					continue
				}
				return partialOrError(fmt.Errorf("评论滚动无进展，请重试"))
			}
		}

		progress, progressErr = getCommentProgress(page)
		if progressErr != nil {
			return partialOrError(fmt.Errorf("评论进度读取失败: %w", progressErr))
		}

		if len(batch) >= maxItems {
			if progress.AtEnd && !moreVisible {
				return batch, batchCursor, false, nil
			}
			return batch, batchCursor, true, nil
		}

		if progress.AtEnd {
			if !moreVisible {
				return batch, batchCursor, false, nil
			}
			return batch, batchCursor, true, nil
		}

		moved, scrollErr := scrollNoteScrollerMoved(page, scrollDelta)
		if scrollErr != nil {
			return partialOrError(fmt.Errorf("评论容器滚动失败: %w", scrollErr))
		}
		if moved {
			batchCursor.Round++
		}
		if err := page.Sleep(await); err != nil {
			return partialOrError(err)
		}

		more, moreVisible, collectErr = collect(maxItems - len(batch))
		if collectErr != nil {
			return partialOrError(collectErr)
		}
		batch = append(batch, more...)

		if !moved && len(more) == 0 {
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			p, pe := getCommentProgress(page)
			if pe == nil && p.AtEnd {
				if !moreVisible {
					break
				}
				return batch, batchCursor, true, nil
			}
			return partialOrError(fmt.Errorf("评论滚动无进展，请重试"))
		}
	}

	if ctx.Err() != nil {
		return nil, nil, false, ctx.Err()
	}

	m, moreVis, collectErr := collect(maxItems - len(batch))
	if collectErr != nil {
		return partialOrError(collectErr)
	}
	batch = append(batch, m...)

	idsGrew := len(batchCursor.ReturnedIDs) > inputBase

	if config.ClickMoreReplies {
		button, err := nextShowMoreButton(page, config.MaxRepliesThreshold, false)
		if err != nil {
			return partialOrError(fmt.Errorf("查询展开按钮失败: %w", err))
		}
		if button != nil {
			if idsGrew {
				return batch, batchCursor, true, nil
			}
			return partialOrError(fmt.Errorf("评论滚动无进展，请重试"))
		}
	}

	p, e := getCommentProgress(page)
	if e != nil {
		return partialOrError(fmt.Errorf("评论进度读取失败: %w", e))
	}

	if len(batch) == 0 && !p.AtEnd {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		return partialOrError(fmt.Errorf("评论滚动无进展，请重试"))
	}

	if p.AtEnd && !moreVis {
		return batch, batchCursor, false, nil
	}

	if !idsGrew {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		return partialOrError(fmt.Errorf("评论滚动无进展，请重试"))
	}

	return batch, batchCursor, true, nil
}

func commentBatchKey(_ int, comment Comment) string {
	if comment.ID != "" {
		return comment.ID
	}
	return ""
}

func flattenComments(comments []Comment) []Comment {
	flat := make([]Comment, 0, len(comments))
	for _, comment := range comments {
		subComments := comment.SubComments
		comment.SubComments = nil
		flat = append(flat, comment)
		flat = append(flat, subComments...)
	}
	return flat
}

func currentFeedIDFromPage(page *hrod.Page) (string, error) {
	result, err := page.Timeout(2*time.Second).Eval(`() => {
		const fromPath = String(location.pathname || "").match(/\/(?:explore|discovery\/item)\/([^/?#]+)/);
		if (fromPath?.[1]) return decodeURIComponent(fromPath[1]);
		return "";
	}`)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return strings.TrimSpace(result.Value.Str()), nil
}

func commentScrollSettings(speed string) (time.Duration, float64) {
	await := map[string]time.Duration{"slow": 1200 * time.Millisecond, "normal": time.Second, "fast": time.Second}[speed]
	scrollDelta := map[string]float64{"slow": 50, "normal": 150, "fast": 250}[speed]
	if await < time.Second {
		await = time.Second
	}
	if scrollDelta == 0 {
		scrollDelta = 100
	}
	return await, scrollDelta
}

func scrollNoteScroller(page *hrod.Page, delta float64) error {
	_, err := scrollNoteScrollerMoved(page, delta)
	return err
}

// findCommentScroller 按可滚动性查找评论滚动容器：.note-scroller，其次 .comments-container，
// 最后 .note-container（AI 搜索模式详情页评论区随 note-container 滚动）。
// 只选真正可滚动的容器（scrollHeight>clientHeight 且 overflowY=scroll/auto），找不到即错误，不回退 DOM scrollBy。
func findCommentScroller(page *hrod.Page) (*hrod.Element, error) {
	result, err := page.Eval(`() => {
		const candidates = [".note-scroller", ".comments-container", ".note-container"];
		for (const selector of candidates) {
			const el = document.querySelector(selector);
			if (!el) continue;
			const style = getComputedStyle(el);
			if (el.scrollHeight > el.clientHeight && (style.overflowY === "auto" || style.overflowY === "scroll")) {
				return selector;
			}
		}
		return "";
	}`)
	if err != nil {
		return nil, err
	}
	selector := ""
	if result != nil {
		selector = result.Value.Str()
	}
	if selector == "" {
		return nil, fmt.Errorf("找不到评论滚动容器 (.note-scroller/.comments-container/.note-container)")
	}
	el, err := page.Element(selector)
	if err != nil || el == nil {
		return nil, fmt.Errorf("读取评论滚动容器失败: %w", err)
	}
	return el, nil
}

// readCommentScrollerPosition 读取滚动容器位置；元素断开或不可滚动均返回错误。
func readCommentScrollerPosition(el *hrod.Element) (float64, float64, float64, error) {
	result, err := el.Eval(`() => {
		if (!this.isConnected) return "";
		const style = getComputedStyle(this);
		const canScroll = this.scrollHeight > this.clientHeight &&
			(style.overflowY === "auto" || style.overflowY === "scroll");
		if (!canScroll) return "";
		return JSON.stringify({ scrollTop: this.scrollTop, scrollHeight: this.scrollHeight, clientHeight: this.clientHeight });
	}`)
	if err != nil {
		return 0, 0, 0, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return 0, 0, 0, fmt.Errorf("评论滚动容器不存在或不可滚动")
	}
	var pos struct {
		ScrollTop    float64 `json:"scrollTop"`
		ScrollHeight float64 `json:"scrollHeight"`
		ClientHeight float64 `json:"clientHeight"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &pos); err != nil {
		return 0, 0, 0, fmt.Errorf("解析评论滚动容器位置失败: %w", err)
	}
	return pos.ScrollTop, pos.ScrollHeight, pos.ClientHeight, nil
}

// commentScrollerScrollTop 读取评论滚动容器当前位置，用于确认物理滚轮后位置是否变化。
// 读取失败返回 error，调用方必须前后两次都成功才可判定滚动，避免误判。
func commentScrollerScrollTop(page *hrod.Page) (float64, error) {
	el, err := findCommentScroller(page)
	if err != nil {
		return 0, err
	}
	top, _, _, err := readCommentScrollerPosition(el)
	if err != nil {
		return 0, err
	}
	return top, nil
}

// humanScrollCommentScroller 用真实物理滚轮推动评论容器：
// 读取 before -> Hover 移入容器 -> 按 100~140px 分格 Mouse.Scroll -> 格间短暂停顿。
// after==before 返回 moved=false,nil 供"已到底"判断；任何一步无法确认均返回 error。
func humanScrollCommentScroller(page *hrod.Page, delta float64) (bool, error) {
	el, err := findCommentScroller(page)
	if err != nil {
		return false, err
	}
	before, _, _, err := readCommentScrollerPosition(el)
	if err != nil {
		return false, err
	}

	// 真实鼠标移入滚动容器
	if err := el.Hover(); err != nil {
		return false, fmt.Errorf("移入评论滚动容器失败: %w", err)
	}

	// 100~140px 分格物理滚轮，禁止 DOM scrollBy/scrollTo fallback
	remaining := delta
	for remaining != 0 {
		seg := math.Abs(remaining)
		if seg > 140 {
			seg = 100 + rand.Float64()*40
		}
		if seg > math.Abs(remaining) {
			seg = math.Abs(remaining)
		}
		if delta < 0 {
			seg = -seg
		}
		if err := page.Actor().Mouse.Scroll(0, seg); err != nil {
			return false, fmt.Errorf("物理滚轮滚动评论区失败: %w", err)
		}
		remaining -= seg
		if remaining != 0 {
			if err := page.SleepRandom(20*time.Millisecond, 65*time.Millisecond); err != nil {
				return false, err
			}
		}
	}
	if err := page.SleepRandom(120*time.Millisecond, 220*time.Millisecond); err != nil {
		return false, err
	}

	after, _, _, err := readCommentScrollerPosition(el)
	if err != nil {
		return false, err
	}
	return after > before, nil
}

// scrollNoteScrollerMoved 滚动评论容器，返回是否发生滚动（moved）。
func scrollNoteScrollerMoved(page *hrod.Page, delta float64) (bool, error) {
	el, err := findCommentScroller(page)
	if err != nil {
		return false, err
	}
	before, _, _, err := readCommentScrollerPosition(el)
	if err != nil {
		return false, err
	}
	result, err := el.Eval(`(delta) => {
		if (!this.isConnected) return "";
		this.scrollBy(0, delta);
		return JSON.stringify({ top: this.scrollTop });
	}`, delta)
	if err != nil {
		return false, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return false, fmt.Errorf("评论滚动容器不存在或不可滚动")
	}
	var pos struct {
		Top float64 `json:"top"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &pos); err != nil {
		return false, fmt.Errorf("解析评论滚动位置失败: %w", err)
	}
	return pos.Top > before, nil
}

func commentProgressScript() string {
	return `() => {
		const endEl = document.querySelector(".end-container");
		const endText = endEl?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";
		const parentCount = document.querySelectorAll(".parent-comment").length;
		const subCount = document.querySelectorAll(".parent-comment > .children-comments > .comment-item-sub, .parent-comment > .reply-container > .list-container > .comment-item").length;
		return JSON.stringify({
			count: parentCount + subCount,
			atEnd: /THE\s*END/i.test(endText),
			noComments: noCommentsText.includes("这是一片荒地"),
		});
	}`
}

func countReplyItems(page *hrod.Page, parentIndex int) (int, error) {
	val, err := page.Timeout(2*time.Second).Eval(`(parentIndex) => {
		const parent = document.querySelectorAll(".parent-comment")[parentIndex];
		if (!parent) return -1;
		return parent.querySelectorAll(":scope > .children-comments > .comment-item-sub, :scope > .reply-container > .list-container > .comment-item").length;
	}`, parentIndex)
	if err != nil {
		return 0, err
	}
	count := int(val.Value.Int())
	if count < 0 {
		return 0, fmt.Errorf("父评论不存在: index=%d", parentIndex)
	}
	return count, nil
}

func waitReplyItemsChanged(page *hrod.Page, parentIndex, before int, timeout time.Duration) error {
	return retry.Do(
		func() error {
			cur, err := countReplyItems(page, parentIndex)
			if err != nil {
				return err
			}
			if cur > before {
				return nil
			}
			return fmt.Errorf("子评论数量未增长: before=%d cur=%d", before, cur)
		},
		retry.Delay(replyExpansionRetryDelay),
		retry.Attempts(uint(timeout / replyExpansionRetryDelay)),
		retry.LastErrorOnly(true),
	)
}

type showMoreButtonSnapshot struct {
	Text        string  `json:"text"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Count       int     `json:"count"`
	ParentIndex int     `json:"parentIndex"`
	ButtonIndex int     `json:"btnIndex"`
}

func clickMoreReplies(page *hrod.Page, maxRepliesThreshold int, remainingDeadline func() time.Duration) error {
	const maxRounds = 20
	for i := 0; i < maxRounds; i++ {
		if remainingDeadline != nil {
			if remaining := remainingDeadline(); remaining < 15*time.Second {
				logrus.Warnf("评论加载剩余时间不足(%s)，停止展开子评论", remaining.Round(time.Second))
				break
			}
		}
		button, err := nextShowMoreButton(page, maxRepliesThreshold, false)
		if err != nil {
			if isEvalTimeout(err) {
				logrus.Warnf("检查子评论展开按钮超时，跳过本轮: %v", err)
				continue
			}
			return err
		}
		if button == nil {
			return nil
		}
		logrus.Infof("点击展开子评论: %s", button.Text)
		// Scope the growth wait to the clicked parent. This assumes the parent
		// comment DOM order remains stable between the button snapshot and retry.
		before, err := countReplyItems(page, button.ParentIndex)
		if err != nil {
			logrus.Warnf("获取展开前子评论数量失败: %v", err)
			before = 0
		}
		if err := clickShowMoreButton(page, button); err != nil {
			return err
		}
		if err := waitReplyItemsChanged(page, button.ParentIndex, before, 7*time.Second); err != nil {
			logrus.Debugf("等待子评论增长超时，继续下一轮: %v", err)
		}
		if err := page.Sleep(4 * time.Second); err != nil {
			return err
		}
	}
	logrus.Infof("展开子评论达到最大轮数(%d)，停止", maxRounds)
	return nil
}

// nextShowMoreButton 单次 Eval 返回"展开子评论"候选按钮的 btnIndex/parentIndex/文本/数量。
// visibleOnly=true 时只匹配评论滚动容器可见区域内的按钮；否则只要求有尺寸。
func nextShowMoreButton(page *hrod.Page, maxRepliesThreshold int, visibleOnly bool) (*showMoreButtonSnapshot, error) {
	result, err := page.Timeout(2*time.Second).Eval(`(maxRepliesThreshold, visibleOnly) => {
		const clean = (value) => (value || "").replace(/\s+/g, " ").trim();
		const scroller = document.querySelector(".note-scroller");
		const sRect = scroller?.getBoundingClientRect();
		const visibleTop = sRect ? Math.max(0, sRect.top) : 0;
		const visibleBottom = sRect ? Math.min(window.innerHeight, sRect.bottom) : window.innerHeight;
		const all = Array.from(document.querySelectorAll(".parent-comment > .children-comments .show-more, .parent-comment > .reply-container .show-more"));
		for (let btnIndex = 0; btnIndex < all.length; btnIndex++) {
			const btn = all[btnIndex];
			const text = clean(btn.innerText || btn.textContent);
			if (!text) continue;
			if (!text.includes("展开") || text.includes("收起")) continue;
			const parent = btn.closest(".parent-comment");
			const parentIndex = Array.from(document.querySelectorAll(".parent-comment")).indexOf(parent);
			if (parentIndex < 0) continue;
			const rect = btn.getBoundingClientRect();
			if (rect.width <= 0 || rect.height <= 0) continue;
			if (visibleOnly && (rect.top < visibleTop || rect.bottom > visibleBottom)) continue;
			const match = text.match(/(\d+(?:\.\d+)?)\s*([万千])?/);
			let count = match ? Number(match[1]) : 0;
			if (match?.[2] === "万") count *= 10000;
			if (match?.[2] === "千") count *= 1000;
			count = Math.floor(count);
			if (maxRepliesThreshold > 0 && count > maxRepliesThreshold) continue;
			return JSON.stringify({
				text,
				x: rect.left + rect.width / 2,
				y: rect.top + rect.height / 2,
				count,
				parentIndex,
				btnIndex,
			});
		}
		return "";
	}`, maxRepliesThreshold, visibleOnly)
	if err != nil {
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return nil, nil
	}
	var button showMoreButtonSnapshot
	if err := json.Unmarshal([]byte(result.Value.Str()), &button); err != nil {
		return nil, fmt.Errorf("解析展开按钮位置失败: %w", err)
	}
	return &button, nil
}

// clickShowMoreButton 用 hrod 定位展开按钮元素并按真实鼠标点击。
// 点击由 hrod 处理，内部会先物理滚动到可视区域，不再依赖坐标点击与 JS scrollBy/scrollIntoView。
func clickShowMoreButton(page *hrod.Page, button *showMoreButtonSnapshot) error {
	els, err := page.Elements(showMoreButtonSelector)
	if err != nil {
		return fmt.Errorf("定位子评论展开按钮失败: %w", err)
	}
	if button.ButtonIndex < 0 || button.ButtonIndex >= len(els) {
		return fmt.Errorf("子评论展开按钮索引越界: %d>=%d", button.ButtonIndex, len(els))
	}
	if err := els[button.ButtonIndex].Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击子评论展开按钮失败: %w", err)
	}
	return nil
}

func sleepRandom(page *hrod.Page, minMs, maxMs int) error {
	return page.SleepRandom(time.Duration(minMs)*time.Millisecond, time.Duration(maxMs)*time.Millisecond)
}

// scrollToCommentsArea 定位评论区：对评论区容器 .comments-container 调 hrod ScrollIntoView
// （直接滚到评论区而非仅把滚动容器置入视口），拟人停顿后执行一次小幅物理滚轮激活懒加载。
func scrollToCommentsArea(page *hrod.Page) error {
	comments, err := page.Element(".comments-container")
	if err != nil || comments == nil {
		return fmt.Errorf("找不到评论区容器 (.comments-container)")
	}
	if err := comments.ScrollIntoView(); err != nil {
		return fmt.Errorf("滚动到评论区失败: %w", err)
	}
	if err := page.SleepRandom(300*time.Millisecond, 600*time.Millisecond); err != nil {
		return err
	}
	if _, err := humanScrollCommentScroller(page, 100); err != nil {
		return fmt.Errorf("评论区懒加载滚轮失败: %w", err)
	}
	return nil
}

func isEvalTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

// ========== DOM 查询 ==========

func getCommentProgress(page *hrod.Page) (commentProgress, error) {
	var progress commentProgress

	result, err := page.Timeout(2*time.Second).Eval(`() => {
		const totalEl = document.querySelector(".comments-container .total") ||
			document.querySelector(".comment-total") ||
			document.querySelector(".total");
		const totalText = totalEl?.innerText || "";
		const totalMatch = totalText.match(/共\s*(\d+)\s*条评论/);
		const endText = document.querySelector(".end-container")?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";
		const parentCount = document.querySelectorAll(".parent-comment").length;
		const subCount = document.querySelectorAll(".parent-comment > .children-comments > .comment-item-sub, .parent-comment > .reply-container > .list-container > .comment-item").length;

		return JSON.stringify({
			count: parentCount + subCount,
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

// ========== 页面检查 ==========

func checkPageAccessible(page *hrod.Page) error {
	// 单次 Eval 完成错误容器查询，不再固定等待或创建内部 timeout，
	// 由调用方 page/ctx 控制超时；Eval 失败直接返回错误，不把失败解释为可访问。
	result, err := page.Eval(`() => {
		const wrapper = document.querySelector(".access-wrapper, .error-wrapper, .not-found-wrapper, .blocked-wrapper");
		const text = (wrapper?.innerText || wrapper?.textContent || "").replace(/\s+/g, " ").trim();
		return JSON.stringify({ found: !!wrapper, text });
	}`)
	if err != nil {
		return fmt.Errorf("检查页面可访问性失败: %w", err)
	}
	if result == nil {
		return fmt.Errorf("检查页面可访问性未返回结果")
	}
	var state struct {
		Found bool   `json:"found"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &state); err != nil {
		return fmt.Errorf("解析页面可访问性状态失败: %w", err)
	}
	if !state.Found {
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
		if strings.Contains(state.Text, kw) {
			logrus.Warnf("笔记不可访问: %s", kw)
			return fmt.Errorf("笔记不可访问: %s", kw)
		}
	}

	// 有错误容器但文本非空且无已知关键词，继续 fail-closed 返回未知错误
	if state.Text != "" {
		logrus.Warnf("笔记不可访问（未知原因）: %s", state.Text)
		return fmt.Errorf("笔记不可访问: %s", state.Text)
	}

	return nil
}

// ========== 数据提取 ==========

func (f *FeedDetailAction) extractFeedDetail(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	if err := page.Wait(rod.Eval(`(id, selector, deadline) => {
		const s = window.__INITIAL_STATE__;
		const hasState = s?.note?.noteDetailMap?.[id] != null;
		const hasDOM = document.querySelector(selector) !== null;
		return hasDOM || hasState || Date.now() >= deadline;
	}`, feedID, SelectorFeedDetailReady, time.Now().Add(10*time.Second).UnixMilli())); err != nil {
		return nil, fmt.Errorf("等待笔记详情加载失败: %w", err)
	}

	deadline := time.Now().Add(initialCommentStateTimeout)
	var lastErr error

	for {
		response, err := ExtractFeedDetailFromDOM(page, feedID)
		if err != nil {
			lastErr = err
			// DOM 结构变更或虚拟列表未渲染时，降级读取 __INITIAL_STATE__。
			response, err = readFeedDetailState(page, feedID)
			if err != nil {
				lastErr = err
			}
		}

		// A non-zero displayed count with an empty list is a transient state while
		// the web client hydrates its comments ref. Do not return that incomplete
		// snapshot as a successful result. A genuinely empty or unavailable list
		// still returns after the short bounded wait.
		if response != nil && (!shouldWaitForInitialComments(response) || time.Now().After(deadline)) {
			return response, nil
		}
		if time.Now().After(deadline) && lastErr != nil {
			return nil, lastErr
		}

		if response != nil {
			logrus.Debugf("评论 DOM 尚未就绪: note=%s, reported=%s", feedID, response.Note.InteractInfo.CommentCount)
		}
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
	result, err := page.Timeout(2*time.Second).Eval(`(feedID) => {
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

func validateFeedAccessArgs(feedID, xsecToken string) error {
	if strings.TrimSpace(feedID) == "" {
		return fmt.Errorf("缺少feed_id参数")
	}
	if strings.TrimSpace(xsecToken) == "" {
		return fmt.Errorf("缺少xsec_token参数")
	}
	return nil
}
