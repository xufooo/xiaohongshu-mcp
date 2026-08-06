package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// CommentFeedAction 表示 Feed 评论动作
type CommentFeedAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

// sleepForCommentStep adds a small human-like delay while preserving cancellation.
// Page.SleepRandom delegates to the project's humanize sleep implementation.
func sleepForCommentStep(page *hrod.Page, min, max time.Duration) error {
	return page.SleepRandom(min, max)
}

// NewCommentFeedAction 创建 Feed 评论动作
func NewCommentFeedAction(page *hrod.Page) *CommentFeedAction {
	return &CommentFeedAction{page: page}
}

func NewCommentFeedActionWithState(page *hrod.Page, state *ActionStateStore) *CommentFeedAction {
	return &CommentFeedAction{page: page, state: state}
}

// PostComment 发表评论到 Feed
func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content string) error {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("评论内容不能为空")
	}

	var page *hrod.Page
	var err error
	if f.state != nil {
		page = f.page.Context(ctx).Timeout(120 * time.Second)
		if err := checkPageAccessible(page); err != nil {
			return err
		}
		reader := NewReadStageAction(page, f.state)
		if err := reader.ReadMin(ctx, feedID, 20*time.Second); err != nil {
			return fmt.Errorf("评论前阅读阶段失败: %w", err)
		}
		page, err = f.preparePage(ctx, feedID, xsecToken, "comment", 120*time.Second)
	} else {
		page, err = f.preparePage(ctx, feedID, xsecToken, "comment", 120*time.Second)
	}
	if err != nil {
		return err
	}

	// 检测页面是否可访问
	if err := checkPageAccessible(page); err != nil {
		return err
	}
	if f.state == nil {
		if err := browseBeforeComment(page); err != nil {
			return fmt.Errorf("评论前浏览页面失败: %w", err)
		}
	}

	editor, err := page.Element("div.input-box div.content-edit")
	if err != nil {
		logrus.Warnf("Failed to find comment editor area: %v", err)
		return fmt.Errorf("未找到评论输入区域: %w", err)
	}
	if err := editor.Click(proto.InputMouseButtonLeft, 1); err != nil {
		logrus.Warnf("Failed to focus comment editor: %v", err)
		return fmt.Errorf("无法聚焦评论输入框: %w", err)
	}
	humanize.Delay(ctx, humanize.AfterClick)

	elem, err := page.Element(SelectorCommentBox)
	if err != nil {
		logrus.Warnf("Failed to find comment input box: %v", err)
		return fmt.Errorf("未找到评论输入框，该帖子可能不支持评论或网页端不可访问: %w", err)
	}

	if err := elem.Input(content); err != nil {
		logrus.Warnf("Failed to input comment content: %v", err)
		return fmt.Errorf("无法输入评论内容: %w", err)
	}

	humanize.Delay(ctx, humanize.AfterType)
	entered, err := elem.Text()
	if err != nil {
		return fmt.Errorf("无法确认评论内容是否写入: %w", err)
	}
	if strings.Join(strings.Fields(entered), " ") != strings.Join(strings.Fields(content), " ") {
		return fmt.Errorf("评论内容未成功写入输入框")
	}
	initialMatchCount, err := countCommentContent(page, content)
	if err != nil {
		return fmt.Errorf("提交前检查评论区失败: %w", err)
	}

	submitButton, err := page.Element(SelectorCommentSubmitButton)
	if err != nil {
		logrus.Warnf("Failed to find submit button: %v", err)
		return fmt.Errorf("未找到提交按钮: %w", err)
	}

	if err := submitButton.Click(proto.InputMouseButtonLeft, 1); err != nil {
		logrus.Warnf("Failed to click submit button: %v", err)
		return fmt.Errorf("无法点击提交按钮: %w", err)
	}
	humanize.Delay(ctx, humanize.AfterClick)

	if err := verifyCommentSubmission(page, content, initialMatchCount); err != nil {
		return fmt.Errorf("评论提交未成功: %w", err)
	}

	if f.state != nil {
		_ = f.state.RecordInteraction(feedID, "comment")
	}
	logrus.Infof("Comment posted successfully to feed: %s", feedID)
	return nil
}

// ReplyToComment 回复指定评论。
// 累计评论阅读由 get_note_detail(max_items/cursor) 等调用记录到 ActionState，
// 本流程不再固定等待 45s/60s；定位目标后按实际耗时/滚动累计，点击回复按钮前做完整校验。
func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content string) error {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("回复内容不能为空")
	}
	if strings.TrimSpace(commentID) == "" && strings.TrimSpace(userID) == "" {
		return fmt.Errorf("缺少comment_id或user_id参数")
	}

	// 增加超时时间，因为需要滚动查找评论，同时保留调用方取消语义。
	var page *hrod.Page
	var err error
	if f.state != nil {
		// 开始时只确认页面和基本风控状态（完整 reply 门槛在定位后校验）。
		page = f.page.Context(ctx).Timeout(5 * time.Minute)
		if err := checkPageAccessible(page); err != nil {
			return err
		}
		page, err = f.preparePage(ctx, feedID, xsecToken, "reply", 5*time.Minute)
	} else {
		page, err = f.preparePage(ctx, feedID, xsecToken, "reply", 5*time.Minute)
	}
	if err != nil {
		return err
	}

	// 检测页面是否可访问
	if err := checkPageAccessible(page); err != nil {
		return err
	}
	if f.state == nil {
		if err := browseBeforeComment(page); err != nil {
			return fmt.Errorf("回复前浏览页面失败: %w", err)
		}
	}

	// 等待评论容器加载
	if err := sleepForCommentStep(page, 1*time.Second, 2*time.Second); err != nil {
		return err
	}

	// 查找评论前记录开始时间，用于累计本次真实查找耗时
	var searchStart time.Time
	if f.state != nil {
		searchStart = time.Now()
	}

	// 使用 Go 实现的查找逻辑
	commentEl, scrolled, err := findCommentElement(ctx, page, commentID, userID)
	if err != nil {
		return fmt.Errorf("无法找到评论: %w", err)
	}

	// 滚动到评论位置
	logrus.Info("滚动到评论位置...")
	if err := commentEl.ScrollIntoView(); err != nil {
		return fmt.Errorf("滚动到评论位置失败: %w", err)
	}
	humanize.Delay(ctx, humanize.BetweenScroll)

	if f.state != nil {
		// 目标查找和最终 ScrollIntoView 完成后，将实际 elapsed/scrolled 累计到 ActionState。
		_ = f.state.RecordCommentDwell(feedID, time.Since(searchStart), scrolled)
		// 点击回复按钮前再次完整校验 reply 门槛（60 秒 + 确认滚动）；
		// 未累计满则快速失败且不得点击回复按钮。
		if err := f.state.ValidateInteraction(feedID, "reply"); err != nil {
			return fmt.Errorf("回复前累计评论阅读不足，请先通过 get_note_detail(max_items/cursor) 阅读并滚动评论区: %w", err)
		}
	}

	logrus.Info("准备点击回复按钮")

	// 查找并点击回复按钮
	replyBtn, err := commentEl.Element(".right .interactions .reply")
	if err != nil {
		return fmt.Errorf("无法找到回复按钮: %w", err)
	}

	if err := replyBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击回复按钮失败: %w", err)
	}
	humanize.Delay(ctx, humanize.AfterClick)

	// 查找回复输入框
	inputEl, err := page.Element("div.input-box div.content-edit p.content-input")
	if err != nil {
		return fmt.Errorf("无法找到回复输入框: %w", err)
	}

	// 输入内容
	if err := inputEl.Input(content); err != nil {
		return fmt.Errorf("输入回复内容失败: %w", err)
	}
	humanize.Delay(ctx, humanize.AfterType)
	initialMatchCount, err := countCommentContent(page, content)
	if err != nil {
		return fmt.Errorf("提交前检查回复区失败: %w", err)
	}

	// 查找并点击提交按钮
	submitBtn, err := page.Element("div.bottom button.submit")
	if err != nil {
		return fmt.Errorf("无法找到提交按钮: %w", err)
	}

	if err := submitBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击提交按钮失败: %w", err)
	}
	humanize.Delay(ctx, humanize.AfterClick)

	if err := verifyCommentSubmission(page, content, initialMatchCount); err != nil {
		return fmt.Errorf("回复提交未成功: %w", err)
	}
	if f.state != nil {
		_ = f.state.RecordInteraction(feedID, "reply")
	}
	logrus.Infof("回复评论成功")
	return nil
}

func (f *CommentFeedAction) preparePage(ctx context.Context, feedID, xsecToken, action string, timeout time.Duration) (*hrod.Page, error) {
	page := f.page.Context(ctx).Timeout(timeout)
	if f.state != nil {
		// reply 门槛依赖累计评论阅读，定位/滚动后再做完整校验；这里只确认基础风控与目标。
		if action == "reply" {
			if err := f.state.ValidateInteractionTarget(feedID); err != nil {
				return nil, fmt.Errorf("%s前置校验失败: %w", commentActionName(action), err)
			}
		} else if err := f.state.ValidateInteraction(feedID, action); err != nil {
			return nil, fmt.Errorf("%s前置校验失败: %w", commentActionName(action), err)
		}
		ok, err := isCurrentFeedDetail(page, feedID)
		if err != nil {
			return nil, fmt.Errorf("%s前置校验失败: 检查当前笔记失败: %w", commentActionName(action), err)
		}
		if !ok {
			return nil, fmt.Errorf("%s前置校验失败: 当前页面不是最近打开的笔记 %s", commentActionName(action), feedID)
		}
		return page, nil
	}

	url := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("打开 feed 详情页: %s", redactSensitiveURL(url))
	if err := page.Navigate(url); err != nil {
		return nil, fmt.Errorf("打开 feed 详情页失败: %w", err)
	}
	kind := XHSReadyDetail
	if action == "comment" {
		kind = XHSReadyCommentBox
	}
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: kind, FeedID: feedID}); err != nil {
		return nil, err
	}
	humanize.Delay(ctx, humanize.AfterNavigate)
	return page, nil
}

func commentActionName(action string) string {
	if action == "reply" {
		return "回复"
	}
	return "评论"
}

// findCommentElement 查找指定评论元素，返回 (*hrod.Element, 是否发生滚动, error)。
// 每轮一次 Eval 返回 .comment-item 数量、atEnd、commentID 匹配索引、userID 匹配索引数组；
// commentID 唯一定位优先，userID 只接受唯一匹配（多匹配直接报歧义，禁止选择第一条）。
// 未找到时才执行物理滚动；ctx 取消、到底或连续停滞停止。
func findCommentElement(ctx context.Context, page *hrod.Page, commentID, userID string) (*hrod.Element, bool, error) {
	logrus.Infof("开始查找评论 - commentID: %s, userID: %s", commentID, userID)

	// 先滚动到评论区（物理滚轮）
	if err := scrollToCommentsArea(page); err != nil {
		return nil, false, err
	}
	if err := sleepForCommentStep(page, 500*time.Millisecond, 1500*time.Millisecond); err != nil {
		return nil, false, err
	}

	scrolled := false
	const maxStagnant = 10
	lastCount := -1
	stagnantChecks := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, scrolled, err
		}

		// 每轮一次 Eval 返回匹配状态
		result, err := page.Eval(`(commentID, userID) => {
			const items = Array.from(document.querySelectorAll(".comment-item"));
			let commentIndex = -1;
			if (commentID) {
				commentIndex = items.findIndex((el) => {
					const id = el.getAttribute("id") || el.dataset?.id || el.getAttribute("data-comment-id") || "";
					return id === commentID;
				});
			}
			const userIndices = [];
			if (userID) {
				items.forEach((el, index) => {
					const uidEl = el.querySelector("[data-user-id]");
					const uid = uidEl ? uidEl.getAttribute("data-user-id") : "";
					if (uid === userID) userIndices.push(index);
				});
			}
			const endText = document.querySelector(".end-container")?.textContent || "";
			return JSON.stringify({
				count: items.length,
				atEnd: /THE\s*END/i.test(endText),
				commentIndex,
				userIndices,
			});
		}`, commentID, userID)
		if err != nil {
			return nil, scrolled, err
		}
		if result == nil {
			return nil, scrolled, fmt.Errorf("查找评论状态未返回结果")
		}
		var state struct {
			Count        int   `json:"count"`
			AtEnd        bool  `json:"atEnd"`
			CommentIndex int   `json:"commentIndex"`
			UserIndices  []int `json:"userIndices"`
		}
		if err := json.Unmarshal([]byte(result.Value.Str()), &state); err != nil {
			return nil, scrolled, fmt.Errorf("解析查找评论状态失败: %w", err)
		}

		// commentID 唯一定位优先于 userID
		if commentID != "" && state.CommentIndex >= 0 {
			if el := commentElementAt(page, state.CommentIndex); el != nil {
				logrus.Infof("✓ 通过 commentID 找到评论: %s", commentID)
				return el, scrolled, nil
			}
			continue // DOM 重排导致索引失效，下一轮重新匹配
		}
		if userID != "" {
			switch len(state.UserIndices) {
			case 1:
				if el := commentElementAt(page, state.UserIndices[0]); el != nil {
					logrus.Infof("✓ 通过 userID 唯一匹配到评论: %s", userID)
					return el, scrolled, nil
				}
				continue
			case 0:
				// 未匹配，继续滚动
			default:
				return nil, scrolled, fmt.Errorf("userID %s 匹配到 %d 条评论，存在歧义，禁止选择第一条", userID, len(state.UserIndices))
			}
		}

		if state.AtEnd {
			logrus.Info("已到达评论底部，未找到目标评论")
			break
		}
		if state.Count == lastCount {
			stagnantChecks++
		} else {
			lastCount = state.Count
			stagnantChecks = 0
		}
		if stagnantChecks >= maxStagnant {
			logrus.Infof("评论数量连续%d轮无增长(%d)，停止查找", stagnantChecks, state.Count)
			break
		}

		// 未找到时才执行物理滚动
		moved, err := scrollNoteScrollerMoved(page, 600)
		if err != nil {
			return nil, scrolled, fmt.Errorf("滚动查找评论失败: %w", err)
		}
		if moved {
			scrolled = true
		}
		if err := sleepForCommentStep(page, 500*time.Millisecond, 1200*time.Millisecond); err != nil {
			return nil, scrolled, err
		}
	}

	return nil, scrolled, fmt.Errorf("未找到评论 (commentID: %s, userID: %s)", commentID, userID)
}

// commentElementAt 按唯一索引取一次 .comment-item 集合中的元素；取不到返回 nil。
func commentElementAt(page *hrod.Page, index int) *hrod.Element {
	els, err := page.Elements(".comment-item")
	if err != nil || index < 0 || index >= len(els) {
		return nil
	}
	return els[index]
}

// browseBeforeComment triggers the post's lazy-loaded content before interacting
// with the comment box.
func browseBeforeComment(page *hrod.Page) error {
	if err := scrollNoteScroller(page, 400); err != nil {
		return err
	}
	return sleepForCommentStep(page, 500*time.Millisecond, 1200*time.Millisecond)
}

type commentSubmissionState struct {
	MatchCount int    `json:"matchCount"`
	Error      string `json:"error"`
}

func countCommentContent(page *hrod.Page, content string) (int, error) {
	state, err := getCommentSubmissionState(page, content)
	if err != nil {
		return 0, err
	}
	if state.Error != "" {
		return 0, fmt.Errorf("页面提示: %s", state.Error)
	}
	return state.MatchCount, nil
}

func verifyCommentSubmission(page *hrod.Page, content string, initialMatchCount int) error {
	const maxChecks = 12

	for check := 0; check < maxChecks; check++ {
		state, err := getCommentSubmissionState(page, content)
		if err != nil {
			return fmt.Errorf("检查提交结果失败: %w", err)
		}
		if state.Error != "" {
			return fmt.Errorf("页面提示: %s", state.Error)
		}
		if state.MatchCount > initialMatchCount {
			return nil
		}
		if err := sleepForCommentStep(page, 500*time.Millisecond, 1200*time.Millisecond); err != nil {
			return err
		}
	}

	return fmt.Errorf("等待评论出现在评论区超时")
}

func getCommentSubmissionState(page *hrod.Page, content string) (commentSubmissionState, error) {
	var state commentSubmissionState
	result, err := page.Eval(`(content) => {
		const commentSelector = ".comments-container .parent-comment, .comments-container .comment-item, .comments-container .comment, .comments-container .sub-comment, .comments-container .reply-item";
		const matchCount = Array.from(document.querySelectorAll(commentSelector))
			.filter((el) => (el.innerText || el.textContent || "").includes(content)).length;
		const errorKeywords = ["操作频繁", "评论过于频繁", "请验证", "滑块验证", "安全验证", "评论失败", "发送失败", "提交失败", "禁止评论"];
		const pageText = document.body?.innerText || "";
		const error = errorKeywords.find((keyword) => pageText.includes(keyword)) || "";
		return JSON.stringify({ matchCount, error });
	}`, content)
	if err != nil {
		return state, err
	}
	if result == nil {
		return state, fmt.Errorf("页面未返回评论提交状态")
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &state); err != nil {
		return state, fmt.Errorf("解析评论提交状态失败: %w", err)
	}
	return state, nil
}
