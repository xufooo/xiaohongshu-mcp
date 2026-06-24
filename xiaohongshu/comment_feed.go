package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
)

// CommentFeedAction 表示 Feed 评论动作
type CommentFeedAction struct {
	page *hrod.Page
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

// PostComment 发表评论到 Feed
func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content string) error {
	page := f.page.Context(ctx).Timeout(60 * time.Second)

	url := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("打开 feed 详情页: %s", url)

	// 导航到详情页
	page.MustNavigate(url)
	page.MustWaitLoad()
	if err := sleepForCommentStep(page, 1500*time.Millisecond, 3*time.Second); err != nil {
		return err
	}

	// 检测页面是否可访问
	if err := checkPageAccessible(page); err != nil {
		return err
	}
	if err := browseBeforeComment(page); err != nil {
		return fmt.Errorf("评论前浏览页面失败: %w", err)
	}

	elem, err := page.Element("div.input-box div.content-edit span")
	if err != nil {
		logrus.Warnf("Failed to find comment input box: %v", err)
		return fmt.Errorf("未找到评论输入框，该帖子可能不支持评论或网页端不可访问: %w", err)
	}

	if err := elem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		logrus.Warnf("Failed to click comment input box: %v", err)
		return fmt.Errorf("无法点击评论输入框: %w", err)
	}

	elem2, err := page.Element("div.input-box div.content-edit p.content-input")
	if err != nil {
		logrus.Warnf("Failed to find comment input field: %v", err)
		return fmt.Errorf("未找到评论输入区域: %w", err)
	}

	if err := elem2.Input(content); err != nil {
		logrus.Warnf("Failed to input comment content: %v", err)
		return fmt.Errorf("无法输入评论内容: %w", err)
	}

	if err := sleepForCommentStep(page, 500*time.Millisecond, 1500*time.Millisecond); err != nil {
		return err
	}
	initialMatchCount, err := countCommentContent(page, content)
	if err != nil {
		return fmt.Errorf("提交前检查评论区失败: %w", err)
	}

	submitButton, err := page.Element("div.bottom button.submit")
	if err != nil {
		logrus.Warnf("Failed to find submit button: %v", err)
		return fmt.Errorf("未找到提交按钮: %w", err)
	}

	if err := submitButton.Click(proto.InputMouseButtonLeft, 1); err != nil {
		logrus.Warnf("Failed to click submit button: %v", err)
		return fmt.Errorf("无法点击提交按钮: %w", err)
	}

	if err := verifyCommentSubmission(page, content, initialMatchCount); err != nil {
		return fmt.Errorf("评论提交未成功: %w", err)
	}

	logrus.Infof("Comment posted successfully to feed: %s", feedID)
	return nil
}

// ReplyToComment 回复指定评论
func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content string) error {
	// 增加超时时间，因为需要滚动查找评论，同时保留调用方取消语义。
	page := f.page.Context(ctx).Timeout(5 * time.Minute)
	url := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("打开 feed 详情页进行回复: %s", url)

	// 导航到详情页
	page.MustNavigate(url)
	page.MustWaitLoad()
	if err := sleepForCommentStep(page, 1500*time.Millisecond, 3*time.Second); err != nil {
		return err
	}

	// 检测页面是否可访问
	if err := checkPageAccessible(page); err != nil {
		return err
	}
	if err := browseBeforeComment(page); err != nil {
		return fmt.Errorf("回复前浏览页面失败: %w", err)
	}

	// 等待评论容器加载
	if err := sleepForCommentStep(page, 1*time.Second, 2*time.Second); err != nil {
		return err
	}

	// 使用 Go 实现的查找逻辑
	commentEl, err := findCommentElement(page, commentID, userID)
	if err != nil {
		return fmt.Errorf("无法找到评论: %w", err)
	}

	// 滚动到评论位置
	logrus.Info("滚动到评论位置...")
	commentEl.MustScrollIntoView()
	if err := sleepForCommentStep(page, 500*time.Millisecond, 1500*time.Millisecond); err != nil {
		return err
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

	if err := sleepForCommentStep(page, 500*time.Millisecond, 1500*time.Millisecond); err != nil {
		return err
	}

	// 查找回复输入框
	inputEl, err := page.Element("div.input-box div.content-edit p.content-input")
	if err != nil {
		return fmt.Errorf("无法找到回复输入框: %w", err)
	}

	// 输入内容
	if err := inputEl.Input(content); err != nil {
		return fmt.Errorf("输入回复内容失败: %w", err)
	}

	if err := sleepForCommentStep(page, 500*time.Millisecond, 1500*time.Millisecond); err != nil {
		return err
	}
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

	if err := verifyCommentSubmission(page, content, initialMatchCount); err != nil {
		return fmt.Errorf("回复提交未成功: %w", err)
	}
	logrus.Infof("回复评论成功")
	return nil
}

// findCommentElement 查找指定评论元素（参考 feed_detail.go 的滚动逻辑）
func findCommentElement(page *hrod.Page, commentID, userID string) (*hrod.Element, error) {
	logrus.Infof("开始查找评论 - commentID: %s, userID: %s", commentID, userID)

	const maxAttempts = 100
	// 先滚动到评论区
	if err := scrollToCommentsArea(page); err != nil {
		return nil, err
	}
	if err := sleepForCommentStep(page, 500*time.Millisecond, 1500*time.Millisecond); err != nil {
		return nil, err
	}

	var lastCommentCount = 0
	stagnantChecks := 0

	logrus.Infof("开始循环查找，最大尝试次数: %d", maxAttempts)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		logrus.Infof("=== 查找尝试 %d/%d ===", attempt+1, maxAttempts)

		// === 1. 检查是否到达底部 ===
		if checkEndContainer(page) {
			logrus.Info("已到达评论底部，未找到目标评论")
			break
		}

		// === 2. 获取当前评论数量 ===
		currentCount := getCommentCount(page)
		logrus.Infof("当前评论数: %d", currentCount)
		
		if currentCount != lastCommentCount {
			logrus.Infof("✓ 评论数增加: %d -> %d", lastCommentCount, currentCount)
			lastCommentCount = currentCount
			stagnantChecks = 0
		} else {
			stagnantChecks++
			if stagnantChecks%5 == 0 {
				logrus.Infof("评论数停滞 %d 次", stagnantChecks)
			}
		}

		// === 3. 停滞检测 ===
		if stagnantChecks >= 10 {
			logrus.Info("评论数量停滞超过10次，可能已加载完所有评论")
			break
		}

		// === 4. 先滚动到最后一个评论（触发懒加载）===
		if currentCount > 0 {
			logrus.Infof("滚动到最后一个评论（共 %d 条）", currentCount)
			
			// 使用 Go 获取所有评论元素
			elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment, .comment-item, .comment")
			if err == nil && len(elements) > 0 {
				// 滚动到最后一个评论
				lastComment := elements[len(elements)-1]
				err := lastComment.ScrollIntoView()
				if err != nil {
					logrus.Warnf("滚动到最后一个评论失败: %v", err)
				}
			} else {
				logrus.Warnf("未找到评论元素: %v", err)
			}
			if err := sleepForCommentStep(page, 300*time.Millisecond, 800*time.Millisecond); err != nil {
				return nil, err
			}
		}

		// === 5. 继续向下滚动 ===
		logrus.Infof("继续向下滚动...")
		viewportHeight := page.MustEval(`() => window.innerHeight`).Int()
		if err := page.Actor().Mouse.Scroll(0, float64(viewportHeight)*0.8); err != nil {
			logrus.Warnf("滚动失败: %v", err)
		}
		if err := sleepForCommentStep(page, 500*time.Millisecond, 1200*time.Millisecond); err != nil {
			return nil, err
		}

		// === 6. 滚动后立即查找（边滚动边查找）===
		// 优先通过 commentID 查找（使用 Timeout 避免长时间等待）
		if commentID != "" {
			selector := fmt.Sprintf("#comment-%s", commentID)
			logrus.Infof("尝试通过 commentID 查找: %s", selector)
			
			// 使用 Timeout 避免长时间等待
			el, err := page.Timeout(2 * time.Second).Element(selector)
			if err == nil && el != nil {
				logrus.Infof("✓ 通过 commentID 找到评论: %s (尝试 %d 次)", commentID, attempt+1)
				return el, nil
			}
			logrus.Infof("未找到 commentID (2秒超时)")
		}

		// 通过 userID 查找
		if userID != "" {
			logrus.Infof("尝试通过 userID 查找: %s", userID)
			
			// 使用 Timeout 避免长时间等待
			elements, err := page.Timeout(2 * time.Second).Elements(".comment-item, .comment, .parent-comment")
			if err == nil && len(elements) > 0 {
				logrus.Infof("找到 %d 个评论元素", len(elements))
				for i, el := range elements {
					// 快速检查，不等待
					userEl, err := el.Timeout(500 * time.Millisecond).Element(fmt.Sprintf(`[data-user-id="%s"]`, userID))
					if err == nil && userEl != nil {
						logrus.Infof("✓ 通过 userID 在第 %d 个元素中找到评论: %s (尝试 %d 次)", i+1, userID, attempt+1)
						return el, nil
					}
				}
				logrus.Infof("在 %d 个元素中未找到匹配的 userID", len(elements))
			} else {
				logrus.Infof("获取评论元素失败或超时: %v", err)
			}
		}
		
		logrus.Infof("本次尝试未找到目标评论，继续下一轮...")

		// === 7. 等待内容加载 ===
		if err := sleepForCommentStep(page, 600*time.Millisecond, 1200*time.Millisecond); err != nil {
			return nil, err
		}
	}

	return nil, fmt.Errorf("未找到评论 (commentID: %s, userID: %s), 尝试次数: %d", commentID, userID, maxAttempts)
}

// browseBeforeComment triggers the post's lazy-loaded content before interacting
// with the comment box.
func browseBeforeComment(page *hrod.Page) error {
	if err := page.Actor().Mouse.Scroll(0, 400); err != nil {
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
