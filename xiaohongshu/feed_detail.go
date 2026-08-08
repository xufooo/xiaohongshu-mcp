package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	xerrors "github.com/xpzouying/xiaohongshu-mcp/errors"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
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

const (
	feedDetailPageTimeout = 10 * time.Minute
	commentLoadTimeout    = 9 * time.Minute
)

// ========== 数据结构 ==========

type CommentLoadConfig struct {
	ClickMoreReplies    bool
	MaxRepliesThreshold int
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
		ScrollSpeed:         "normal",
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

func (f *FeedDetailAction) GetFeedDetailCommentsBatch(ctx context.Context, feedID, xsecToken string, cursor *CommentCursor, maxItems int, config CommentLoadConfig) (*FeedDetailResponse, *CommentCursor, bool, error) {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return nil, nil, false, err
	}

	config = normalizeCommentLoadConfig(config)
	page := f.page.Context(ctx).Timeout(feedDetailPageTimeout)
	counter := &evalTimeoutCounter{}

	logrus.Infof("从卡片打开 feed 详情页(评论分批): %s", feedID)
	source, err := inferOpenSource(ctx, page, counter)
	if err != nil {
		return nil, nil, false, fmt.Errorf("推断打开来源失败: %w", err)
	}
	opener := NewNoteOpenActionWithState(page, f.state)
	if err := opener.OpenFromCards(ctx, counter, feedID, xsecToken); err != nil {
		return nil, nil, false, fmt.Errorf("从卡片打开笔记失败，请重新搜索或滚动后重试: %w", err)
	}
	if f.state != nil {
		_ = f.state.RecordOpen(feedID, source)
	}
	humanize.Delay(ctx, humanize.AfterNavigate)
	if err := checkPageAccessible(ctx, page, counter); err != nil {
		return nil, nil, false, err
	}

	reader := NewReadStageAction(page, f.state)
	if err := reader.read(ctx, counter, feedID, 5*time.Second); err != nil {
		return nil, nil, false, fmt.Errorf("阅读阶段失败: %w", err)
	}

	detail, err := f.extractFeedDetail(ctx, page, counter, feedID)
	if err != nil {
		return nil, nil, false, err
	}

	commentPage := page.Timeout(commentLoadTimeout)
	commentStart := time.Now()
	comments, nextCursor, hasMore, err := loadCommentsBatch(ctx, commentPage, config, cursor, maxItems)
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
	if totalItems, totalErr := knownCommentTotal(ctx, commentPage); totalErr != nil {
		return nil, nil, false, totalErr
	} else if totalItems > 0 {
		detail.Comments.TotalItems = totalItems
	}
	return detail, nextCursor, hasMore, nil
}

func knownCommentTotal(ctx context.Context, page *hrod.Page) (int, error) {
	progress, err := getCommentProgress(ctx, page)
	if err != nil {
		return 0, nil
	}
	if progress.Total <= 0 {
		return 0, nil
	}
	return progress.Total, nil
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

// commentProgress is collected in one browser evaluation. Keeping the check in
// the browser avoids several round trips per scroll on slower devices.
type commentProgress struct {
	Total      int  `json:"total"`
	AtEnd      bool `json:"atEnd"`
	NoComments bool `json:"noComments"`
}

// LoadCommentsBatch 分批加载评论：每轮滚动 + 展开子回复，收集到 maxItems 条或到底返回，
// 携带 cursor 供调用方续页（MCP get_note_detail 分批读取）。
func LoadCommentsBatch(ctx context.Context, page *hrod.Page, config CommentLoadConfig, cursor *CommentCursor, maxItems int) ([]Comment, *CommentCursor, bool, error) {
	return loadCommentsBatch(ctx, page, config, cursor, maxItems)
}

func loadCommentsBatch(ctx context.Context, page *hrod.Page, config CommentLoadConfig, cursor *CommentCursor, maxItems int) ([]Comment, *CommentCursor, bool, error) {
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
		id, err := currentFeedIDFromPage(ctx, page)
		if IsFatalRendererError(err) {
			return nil, nil, false, err
		}
		if err == nil {
			feedID = id
			batchCursor.FeedID = id
		}
	}

	if batchCursor.Round == 0 {
		if err := scrollToCommentsArea(ctx, page); err != nil {
			return nil, nil, false, fmt.Errorf("定位评论区失败: %w", err)
		}
		moved, err := scrollNoteScrollerMoved(ctx, page, 160)
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

	collect := func(limit int) ([]Comment, bool, commentProgress, error) {
		if limit <= 0 {
			progress, err := getCommentProgress(ctx, page)
			return nil, true, progress, err
		}
		snapshot, err := extractCommentsWithProgressFromDOM(ctx, page, feedID)
		if err != nil {
			return nil, false, commentProgress{}, err
		}
		flat := flattenComments(snapshot.Comments)
		snapshot.Comments = nil
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
				return batch, true, snapshot.Progress, nil
			}
			returned[key] = struct{}{}
			batchCursor.ReturnedIDs = append(batchCursor.ReturnedIDs, key)
			batch = append(batch, comment)
		}
		return batch, false, snapshot.Progress, nil
	}

	var batch []Comment
	replyClicksTotal := 0
	replyStall := false

	inputBase := len(batchCursor.ReturnedIDs)

	partialOrError := func(err error) ([]Comment, *CommentCursor, bool, error) {
		if IsFatalRendererError(err) {
			return nil, nil, false, err
		}
		if len(batchCursor.ReturnedIDs) > inputBase && ctx.Err() == nil {
			return batch, batchCursor, true, nil
		}
		return nil, nil, false, err
	}

	more, moreVisible, progress, collectErr := collect(maxItems)
	if collectErr != nil {
		return nil, nil, false, collectErr
	}
	batch = append(batch, more...)

	// 无评论（荒地）时直接返回空，避免空转。
	if progress.NoComments {
		logrus.Info("✓ 笔记无评论（荒地），跳过加载")
		return batch, batchCursor, false, nil
	}

	for i := 0; i < 500; i++ {
		if rem := remaining(); rem < 15*time.Second {
			logrus.Warnf("评论分批加载剩余时间不足(%s)，停止新操作", rem.Round(time.Second))
			break
		}

		if config.ClickMoreReplies {
			button, err := nextShowMoreButton(ctx, page, config.MaxRepliesThreshold)
			if err != nil {
				if isEvalTimeout(err) {
					logrus.Warnf("查询展开按钮超时，跳过本轮: %v", err)
				} else {
					return partialOrError(fmt.Errorf("查询展开按钮失败: %w", err))
				}
			}
			if button != nil {
				if len(batch) >= maxItems {
					return batch, batchCursor, true, nil
				}
				// 每次只处理一个按钮：点击后 DOM 重排，其余按钮坐标漂移失效；
				// 点完 collect 后 continue 下一轮重新查询，保证坐标最新（对齐 pre 语义）。
				// 点击条件不满足（replyStall/8次上限）时**不 continue**，落到滚动路径打破空转。
				if canClickReplies(replyStall, replyClicksTotal, config.MaxRepliesThreshold) {
					before, countErr := countReplyItems(ctx, page, button.ParentIndex)
					if countErr != nil {
						if isEvalTimeout(countErr) {
							logrus.Warnf("回复计数超时，继续: %v", countErr)
							before = 0
						} else {
							return partialOrError(fmt.Errorf("回复计数失败: %w", countErr))
						}
					}
					if err := clickShowMoreButton(page, button); err != nil {
						return partialOrError(fmt.Errorf("回复展开点击失败: %w", err))
					}
					replyClicksTotal++
					batchCursor.ExpandRound++
					if waitErr := waitReplyItemsChanged(ctx, page, button.ParentIndex, before, 3*time.Second); waitErr != nil {
						// 对齐 pre：wait 失败（含增长停滞）只标记 replyStall，不 break，
						// collect 后 continue 下一轮；下一轮受限时走 pre 式 return 路径。
						logrus.Warnf("等待回复增长: %v", waitErr)
						replyStall = true
					}
					more, moreVisible, progress, collectErr = collect(maxItems - len(batch))
					if collectErr != nil {
						return partialOrError(collectErr)
					}
					batch = append(batch, more...)
					continue
				}
				// 点击条件不满足（replyStall/8次上限）时**直接返回**（对齐 pre）：
				// 有新评论 → hasMore=true 留给下批 cursor 续页；无新评论 → 报无进展。
				// 不 break 绕收尾、不继续滚动（按钮不随滚动改变，会死循环滚动）。
				if len(batchCursor.ReturnedIDs) > inputBase && ctx.Err() == nil {
					logrus.Infof("子评论展开受限(replyStall=%v, 已展开%d)，返回续页", replyStall, replyClicksTotal)
					return batch, batchCursor, true, nil
				}
				logrus.Infof("子评论展开受限(replyStall=%v, 已展开%d)，无新评论返回无进展", replyStall, replyClicksTotal)
				return nil, nil, false, fmt.Errorf("评论滚动无进展，请重试")
			}
			// 无更多展开按钮：不报"无进展"，继续滚动加载
			logrus.Infof("子评论展开停止(replyStall=%v, 已展开%d)，继续滚动", replyStall, replyClicksTotal)
		}

		// 滚动到底且无更多可见新评论：break 走收尾段（clickMoreReplies 兜底展开剩余按钮），
		// 否则剩余 showMore 按钮会被跳过；batch 已满或仍有可见评论时返回续页。
		if progress.AtEnd && !moreVisible {
			break
		}
		if len(batch) >= maxItems {
			return batch, batchCursor, true, nil
		}
		if progress.AtEnd {
			return batch, batchCursor, true, nil
		}

		moved, scrollErr := scrollNoteScrollerMoved(ctx, page, scrollDelta)
		if scrollErr != nil {
			if IsFatalRendererError(scrollErr) {
				return partialOrError(scrollErr)
			}
			if isEvalTimeout(scrollErr) {
				continue
			}
			return partialOrError(fmt.Errorf("评论容器滚动失败: %w", scrollErr))
		}
		if moved {
			batchCursor.Round++
		}
		if err := page.Sleep(await); err != nil {
			return partialOrError(err)
		}

		more, moreVisible, progress, collectErr = collect(maxItems - len(batch))
		if collectErr != nil {
			return partialOrError(collectErr)
		}
		batch = append(batch, more...)

		if !moved && len(more) == 0 {
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			if progress.AtEnd {
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

	m, moreVis, progress, collectErr := collect(maxItems - len(batch))
	if collectErr != nil {
		return partialOrError(collectErr)
	}
	batch = append(batch, m...)

	idsGrew := len(batchCursor.ReturnedIDs) > inputBase

	if config.ClickMoreReplies && !replyStall {
		// 主循环可能因 AtEnd/!moreVisible break 提前结束，剩余 showMore 按钮不会被点
		// （idsGrew=false 时旧逻辑还会误报"评论滚动无进展"）。收尾调用 clickMoreReplies
		// 兜底展开剩余按钮，再 collect 收新评论，返回续页或读完——否则大帖子回复
		// （如 434 评帖 144 父+290 子）只能读到滚动碰到的部分，漏掉未展开的子评论。
		// replyStall 时跳过：已确认展开停滞，重试同类按钮只会重复失败。
		if err := clickMoreReplies(ctx, page, config.MaxRepliesThreshold, replyClicksTotal, remaining); err != nil {
			return partialOrError(fmt.Errorf("全量展开子评论失败: %w", err))
		}
		m, moreVis2, progress2, collectErr2 := collect(maxItems - len(batch))
		if collectErr2 != nil {
			return partialOrError(collectErr2)
		}
		batch = append(batch, m...)
		idsGrew = len(batchCursor.ReturnedIDs) > inputBase
		if moreVis2 {
			// 展开后仍有更多可见新评论（或 batch 未满），更新可见状态，
			// 让下方 AtEnd/idsGrew 判定基于展开后的真实状态。
			moreVis = true
		}
		if progress2.AtEnd {
			progress = progress2
		}
	}

	if len(batch) == 0 && !progress.AtEnd {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		return partialOrError(fmt.Errorf("评论滚动无进展，请重试"))
	}

	if progress.AtEnd && !moreVis {
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

func currentFeedIDFromPage(ctx context.Context, page *hrod.Page) (string, error) {
	result, err := evalQuick(ctx, page, `() => {
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

func scrollNoteScroller(ctx context.Context, page *hrod.Page, delta float64) error {
	_, err := scrollNoteScrollerMoved(ctx, page, delta)
	return err
}

// scrollNoteScrollerMoved 单次 Eval 滚动评论容器并返回是否发生滚动（moved）。
// 对齐 pre 的单次 Eval（容器查找+祖先遍历+读取 before+scrollBy 一次完成），
// 候选扩展 .comments-container/.note-container 以兼容 AI 搜索模式。
func scrollNoteScrollerMoved(ctx context.Context, page *hrod.Page, delta float64) (bool, error) {
	result, err := evalQuick(ctx, page, `(delta) => {
		const candidates = [".note-scroller", ".comments-container", ".note-container"];
		let scroller = null;
		for (const selector of candidates) {
			const el = document.querySelector(selector);
			if (!el) continue;
			let node = el;
			while (node) {
				const style = getComputedStyle(node);
				const canScroll = node.scrollHeight > node.clientHeight &&
					(style.overflowY === "auto" || style.overflowY === "scroll");
				if (canScroll) { scroller = node; break; }
				node = node.parentElement;
			}
			if (scroller) break;
		}
		if (!scroller) return JSON.stringify({found: false, moved: false});
		const before = scroller.scrollTop;
		scroller.scrollBy(0, delta);
		return JSON.stringify({found: true, moved: scroller.scrollTop > before});
	}`, delta)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, fmt.Errorf("评论容器滚动未返回结果")
	}
	var state struct {
		Found bool `json:"found"`
		Moved bool `json:"moved"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &state); err != nil {
		return false, fmt.Errorf("解析评论容器滚动状态: %w", err)
	}
	if !state.Found {
		return false, fmt.Errorf("评论容器不存在")
	}
	return state.Moved, nil
}

func countReplyItems(ctx context.Context, page *hrod.Page, parentIndex int) (int, error) {
	val, err := evalQuick(ctx, page, `(parentIndex) => {
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

func waitReplyItemsChanged(ctx context.Context, page *hrod.Page, parentIndex, before int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	var cur int
	for time.Now().Before(deadline) {
		var err error
		cur, err = countReplyItems(ctx, page, parentIndex)
		if err != nil {
			lastErr = err
		} else if cur > before {
			return nil
		}
		if err := page.Sleep(replyExpansionRetryDelay); err != nil {
			return err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("子评论数量未增长: before=%d cur=%d", before, cur)
}

type showMoreButtonSnapshot struct {
	Text        string  `json:"text"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Count       int     `json:"count"`
	ParentIndex int     `json:"parentIndex"`
}

// canClickReplies 判定本轮是否允许继续点击"展开子评论"按钮。
// replyStall（展开停滞）或正数阈值下累计点击已达 8 次上限时返回 false，
// 调用方应转入滚动/收尾路径而非对同一按钮继续空转。
func canClickReplies(replyStall bool, clicked int, maxRepliesThreshold int) bool {
	if replyStall {
		return false
	}
	return clicked < 8 || maxRepliesThreshold == 0
}

func clickMoreReplies(ctx context.Context, page *hrod.Page, maxRepliesThreshold int, clickedSoFar int, remainingDeadline func() time.Duration) error {
	const maxRounds = 20
	clicked := 0
	for i := 0; i < maxRounds; i++ {
		if remainingDeadline != nil {
			if remaining := remainingDeadline(); remaining < 15*time.Second {
				logrus.Warnf("评论加载剩余时间不足(%s)，停止展开子评论", remaining.Round(time.Second))
				break
			}
		}
		button, err := nextShowMoreButton(ctx, page, maxRepliesThreshold)
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
		// 正数阈值下严格执行累计 8 次上限；reply_limit=0 才允许不限次数。
		if maxRepliesThreshold > 0 && clickedSoFar+clicked >= 8 {
			logrus.Infof("子评论展开达到 8 次上限，停止")
			return nil
		}
		logrus.Infof("点击展开子评论: %s", button.Text)
		before, err := countReplyItems(ctx, page, button.ParentIndex)
		if err != nil {
			logrus.Warnf("获取展开前子评论数量失败: %v", err)
			before = 0
		}
		if err := clickShowMoreButton(page, button); err != nil {
			return err
		}
		waitErr := waitReplyItemsChanged(ctx, page, button.ParentIndex, before, 7*time.Second)
		if waitErr != nil {
			// 对齐 pre：wait 失败只记录，继续下一轮（20 轮上限兜底）。
			logrus.Debugf("等待子评论增长超时，继续下一轮: %v", waitErr)
		}
		if err := page.Sleep(4 * time.Second); err != nil {
			return err
		}
		clicked++
	}
	logrus.Infof("展开子评论达到最大轮数(%d)，停止", maxRounds)
	return nil
}

// nextShowMoreButton 单次 Eval 返回第一个合格"展开子评论"按钮的坐标/父评论索引/文本/数量，
// 并在 Eval 内将按钮滚动到评论区容器可见区域（pre 验证过的定位方式）。
func nextShowMoreButton(ctx context.Context, page *hrod.Page, maxRepliesThreshold int) (*showMoreButtonSnapshot, error) {
	result, err := evalQuick(ctx, page, `(maxRepliesThreshold) => {
		const clean = (value) => (value || "").replace(/\s+/g, " ").trim();
		// reply_limit=-1：一个子评论都不展开（跳过所有展开按钮）。
		if (maxRepliesThreshold === -1) return "";
		const scroller = document.querySelector(".note-scroller");
		const parents = Array.from(document.querySelectorAll(".parent-comment"));
		const buttons = parents
			.flatMap((parent) => Array.from(parent.querySelectorAll(":scope > .children-comments .show-more, :scope > .reply-container .show-more")));
		for (const btn of buttons) {
			const text = clean(btn.innerText || btn.textContent);
			if (!text) continue;
			if (!text.includes("展开") || text.includes("收起")) continue;
			const parent = btn.closest(".parent-comment");
			const parentIndex = parents.indexOf(parent);
			if (parentIndex < 0) continue;
			let rect = btn.getBoundingClientRect();
			if (rect.width <= 0 || rect.height <= 0) continue;
			const match = text.match(/(\d+(?:\.\d+)?)\s*([万千])?/);
			let count = match ? Number(match[1]) : 0;
			if (match?.[2] === "万") count *= 10000;
			if (match?.[2] === "千") count *= 1000;
			count = Math.floor(count);
			if (maxRepliesThreshold > 0 && count > maxRepliesThreshold) continue;
			btn.scrollIntoView({ block: "center", inline: "nearest" });
			rect = btn.getBoundingClientRect();
			if (scroller) {
				const sRect = scroller.getBoundingClientRect();
				const visibleTop = Math.max(0, sRect.top);
				const visibleBottom = Math.min(window.innerHeight, sRect.bottom);
				if (rect.top < visibleTop || rect.bottom > visibleBottom) {
					scroller.scrollBy(0, rect.top - sRect.top - sRect.height / 2 + rect.height / 2);
					rect = btn.getBoundingClientRect();
				}
			}
			if (rect.width <= 0 || rect.height <= 0) continue;
			return JSON.stringify({
				text,
				x: rect.left + rect.width / 2,
				y: rect.top + rect.height / 2,
				count,
				parentIndex,
			});
		}
		return "";
	}`, maxRepliesThreshold)
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

// clickShowMoreButton 按坐标真实点击展开按钮（pre 验证过的点击方式）。
func clickShowMoreButton(page *hrod.Page, button *showMoreButtonSnapshot) error {
	return page.ClickPoint(proto.Point{X: button.X, Y: button.Y})
}

func sleepRandom(page *hrod.Page, minMs, maxMs int) error {
	return page.SleepRandom(time.Duration(minMs)*time.Millisecond, time.Duration(maxMs)*time.Millisecond)
}

func scrollToCommentsArea(ctx context.Context, page *hrod.Page) error {
	_, err := evalQuick(ctx, page, `() => {
		const cc = document.querySelector('.comments-container, .comments-el');
		let scroller = cc;
		while (scroller) {
			const style = getComputedStyle(scroller);
			const canScroll = scroller.scrollHeight > scroller.clientHeight &&
				(style.overflowY === 'auto' || style.overflowY === 'scroll');
			if (canScroll) break;
			scroller = scroller.parentElement;
		}
		if (cc && scroller) {
			const top = cc.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
			scroller.scrollTo(0, Math.max(0, top - 80));
			return;
		}
		if (cc) { cc.scrollIntoView({block:'center'}); }
	}`)
	if err != nil && isEvalTimeout(err) {
		return nil
	}
	return err
}

func isEvalTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

var ErrFatalRendererError = errors.New("fatal renderer error: repeated eval timeout")

func IsFatalRendererError(err error) bool {
	return errors.Is(err, ErrFatalRendererError)
}

type evalTimeoutCounter struct {
	timeouts int
}

func (c *evalTimeoutCounter) reset() {
	c.timeouts = 0
}

func (c *evalTimeoutCounter) add(ctx context.Context, err error, probe func() error) error {
	if ctx.Err() != nil {
		c.reset()
		return err
	}
	if err == nil || !isEvalTimeout(err) {
		c.reset()
		return err
	}
	c.timeouts++
	if c.timeouts < 3 {
		return err
	}
	probeErr := probe()
	if probeErr == nil {
		c.reset()
		return err
	}
	if isEvalTimeout(probeErr) {
		return fmt.Errorf("%w: business eval: %v; renderer probe: %v", ErrFatalRendererError, err, probeErr)
	}
	return fmt.Errorf("%w: business eval: %v; renderer probe failed: %v", ErrFatalRendererError, err, probeErr)
}

func evalJS(ctx context.Context, counter *evalTimeoutCounter, page *hrod.Page, fn string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	evalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := page.Context(evalCtx).Eval(fn, args...)
	if counter != nil {
		err = counter.add(ctx, err, func() error {
			probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
			defer probeCancel()
			_, probeErr := page.Context(probeCtx).Eval(`() => 1`)
			if ctx.Err() != nil {
				return nil
			}
			return probeErr
		})
	}
	return result, err
}

// evalQuick 执行轻量高频 Eval（评论展开/滚动/计数等），独立 2s 边界且不累计
// counter/probe：评论加载是高频小操作，慢 Eval 直接容忍跳过，不应触发 renderer 熔断。
func evalQuick(ctx context.Context, page *hrod.Page, fn string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	evalCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return page.Context(evalCtx).Eval(fn, args...)
}

// evalJSNoCounter 执行重型单次 Eval（如评论全量提取），5s 边界但不累计
// counter/probe：重型提取单次完成，超时返回错误即可，无需 renderer 熔断。
func evalJSNoCounter(ctx context.Context, page *hrod.Page, fn string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	evalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return page.Context(evalCtx).Eval(fn, args...)
}

func evalElementJS(ctx context.Context, counter *evalTimeoutCounter, element *hrod.Element, fn string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	evalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := element.Context(evalCtx).Eval(fn, args...)
	if counter != nil {
		page := element.Page()
		err = counter.add(ctx, err, func() error {
			probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
			defer probeCancel()
			_, probeErr := page.Context(probeCtx).Eval(`() => 1`)
			if ctx.Err() != nil {
				return nil
			}
			return probeErr
		})
	}
	return result, err
}

// ========== DOM 查询 ==========

func getCommentProgress(ctx context.Context, page *hrod.Page) (commentProgress, error) {
	var progress commentProgress

	result, err := evalQuick(ctx, page, `() => {
		const totalEl = document.querySelector(".comments-container .total") ||
			document.querySelector(".comment-total") ||
			document.querySelector(".total");
		const totalText = totalEl?.innerText || "";
		const totalMatch = totalText.match(/共\s*(\d+)\s*条评论/);
		const endText = document.querySelector(".end-container")?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";
		return JSON.stringify({
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

func checkPageAccessible(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) error {
	result, err := evalJS(ctx, counter, page, `() => {
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

func (f *FeedDetailAction) extractFeedDetail(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (*FeedDetailResponse, error) {
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
		response, err := ExtractFeedDetailFromDOM(ctx, page, counter, feedID)
		if err != nil {
			lastErr = err
			if IsFatalRendererError(err) {
				return nil, err
			}
			response, err = readFeedDetailState(ctx, page, counter, feedID)
			if err != nil {
				if IsFatalRendererError(err) {
					return nil, err
				}
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
func readFeedDetailState(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (*FeedDetailResponse, error) {
	var response *FeedDetailResponse
	err := retry.Do(
		func() error {
			var err error
			response, err = readFeedDetailStateOnce(ctx, page, counter, feedID)
			return err
		},
		retry.Attempts(3),
		retry.Delay(200*time.Millisecond),
		retry.MaxJitter(300*time.Millisecond),
		retry.RetryIf(func(err error) bool { return !IsFatalRendererError(err) }),
	)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func readFeedDetailStateOnce(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (*FeedDetailResponse, error) {
	result, err := evalJS(ctx, counter, page, `(feedID) => {
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
		return nil, xerrors.ErrNoFeedDetail
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
