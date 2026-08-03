package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// NotificationReplyResult reply_notification 结果。
type NotificationReplyResult struct {
	Ref       string `json:"notification_ref"`
	CommentID string `json:"comment_id,omitempty"`
	Sent      bool   `json:"sent"`
	Message   string `json:"message,omitempty"`
}

// replyNotificationOnPage 回复通知行中的评论。
// 三层校验：引用（ref/generation/mentions）+ 通知行（state 同 commentID + DOM 指纹唯一匹配）+ 编辑器（placeholder 含 回复 <昵称>）。
// 输入后用 textarea value 核对；提交按钮校验文本"发送"且可见可交互；发送后以 textarea 消失/隐藏作为确认。
func replyNotificationOnPage(ctx context.Context, page *hrod.Page, target notificationTarget, content string) (*NotificationReplyResult, error) {
	if err := verifyNotificationTargetInState(page, target); err != nil {
		return nil, err
	}
	row, _, err := findNotificationRowElement(page, target)
	if err != nil {
		return nil, err
	}
	if target.CommentID == "" {
		return nil, fmt.Errorf("回复失败: 通知条目缺少 comment_id，无法定位评论")
	}
	replyBtn, err := row.Element(SelectorNotificationReplyButton)
	if err != nil {
		return nil, fmt.Errorf("通知行回复按钮缺失: %w", err)
	}
	if err := page.SleepRandom(800*time.Millisecond, 1500*time.Millisecond); err != nil {
		return nil, err
	}
	if err := replyBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return nil, fmt.Errorf("点击回复按钮失败: %w", err)
	}

	inputEl, err := waitNotificationReplyInput(ctx, page, row)
	if err != nil {
		return nil, err
	}
	if err := verifyNotificationReplyPlaceholder(inputEl, target.Nickname); err != nil {
		return nil, err
	}
	if err := page.SleepRandom(300*time.Millisecond, 600*time.Millisecond); err != nil {
		return nil, err
	}
	if err := inputEl.Input(content); err != nil {
		return nil, fmt.Errorf("输入回复内容失败: %w", err)
	}
	gotValue, err := notificationReplyInputValue(inputEl)
	if err != nil {
		return nil, err
	}
	if normalizeNotificationText(gotValue) != normalizeNotificationText(content) {
		return nil, fmt.Errorf("输入核对失败: textarea 内容与预期不一致，不发送")
	}

	initialCount, err := countNotificationReplyText(page, content)
	if err != nil {
		return nil, err
	}
	submitBtn, err := findNotificationSubmitButton(row)
	if err != nil {
		return nil, err
	}
	if err := page.SleepRandom(400*time.Millisecond, 800*time.Millisecond); err != nil {
		return nil, err
	}
	if err := submitBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return nil, fmt.Errorf("点击发送按钮失败: %w", err)
	}
	if err := verifyNotificationReplySubmission(ctx, page, content, initialCount); err != nil {
		return nil, err
	}
	return &NotificationReplyResult{Ref: target.Ref, CommentID: target.CommentID, Sent: true, Message: "回复已发送"}, nil
}

// verifyNotificationReplyPlaceholder 校验回复输入框 placeholder 包含 回复 <昵称>。
func verifyNotificationReplyPlaceholder(inputEl *hrod.Element, nickname string) error {
	placeholder, err := inputEl.Attribute("placeholder")
	if err != nil {
		return fmt.Errorf("读取回复输入框 placeholder 失败: %w", err)
	}
	if placeholder == nil {
		return fmt.Errorf("回复输入框缺失 placeholder，禁止发送")
	}
	text := normalizeNotificationText(*placeholder)
	if !strings.Contains(text, "回复") {
		return fmt.Errorf("回复输入框 placeholder 异常: %q", text)
	}
	if nickname != "" && !strings.Contains(text, normalizeNotificationText(nickname)) {
		return fmt.Errorf("回复输入框 placeholder 与目标昵称不一致: %q", text)
	}
	return nil
}

// notificationReplyInputValue 读取 textarea 当前值。
func notificationReplyInputValue(inputEl *hrod.Element) (string, error) {
	obj, err := inputEl.Eval(`() => this.value`)
	if err != nil {
		return "", err
	}
	if obj == nil {
		return "", fmt.Errorf("textarea 无返回")
	}
	return obj.Value.Str(), nil
}

// findNotificationSubmitButton 仅在目标通知行内查找发送按钮，找不到即报错（fail-closed）。
func findNotificationSubmitButton(row *hrod.Element) (*hrod.Element, error) {
	if row == nil {
		return nil, fmt.Errorf("找不到发送按钮: 目标通知行缺失")
	}
	btn, err := row.Element(SelectorNotificationReplySubmit)
	if err != nil || btn == nil {
		return nil, fmt.Errorf("目标通知行内找不到发送按钮，禁止页面级退路: %w", err)
	}
	text, err := btn.Text()
	if err != nil {
		return nil, fmt.Errorf("读取发送按钮文本失败: %w", err)
	}
	if normalizeNotificationText(text) != "发送" {
		return nil, fmt.Errorf("发送按钮文本必须为「发送」，实际: %q", normalizeNotificationText(text))
	}
	obj, err := btn.Eval(`() => {
		const el = this;
		return JSON.stringify({
			visible: el.offsetParent !== null && el.offsetWidth > 0 && el.offsetHeight > 0,
			disabled: Boolean(el.disabled) || el.getAttribute("aria-disabled") === "true" || el.classList.contains("disabled"),
		});
	}`)
	if err != nil {
		return nil, fmt.Errorf("检查发送按钮状态失败: %w", err)
	}
	if obj == nil {
		return nil, fmt.Errorf("发送按钮状态无返回")
	}
	var state struct {
		Visible  bool `json:"visible"`
		Disabled bool `json:"disabled"`
	}
	if err := json.Unmarshal([]byte(obj.Value.Str()), &state); err != nil {
		return nil, fmt.Errorf("解析发送按钮状态失败: %w", err)
	}
	if !state.Visible || state.Disabled {
		return nil, fmt.Errorf("发送按钮不可交互（visible=%t disabled=%t）", state.Visible, state.Disabled)
	}
	return btn, nil
}

// waitNotificationReplyInput 等待回复输入框展开：仅在目标通知行内查找，找不到即 fail-closed。
func waitNotificationReplyInput(ctx context.Context, page *hrod.Page, row *hrod.Element) (*hrod.Element, error) {
	if row == nil {
		return nil, fmt.Errorf("等待回复输入框失败: 目标通知行缺失")
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if el, err := row.Element(SelectorNotificationReplyInput); err == nil {
			return el, nil
		}
		if err := page.SleepRandom(300*time.Millisecond, 600*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("等待回复输入框展开超时: 输入框未出现在目标行内（禁止页面级退路）")
}

// countNotificationReplyText 提交前统计列表中出现回复内容的叶子节点数，识别页面错误提示。
func countNotificationReplyText(page *hrod.Page, content string) (int, error) {
	_, _, err := notificationReplySubmissionState(page, content)
	if err != nil {
		return 0, err
	}
	return notificationReplyMatchCount(page, content)
}

// verifyNotificationReplySubmission 轮询等待提交确认：目标行 textarea 消失/隐藏或回复内容出现在列表中；
// 或有明确错误提示则终止。最长等待 8 秒，不重试发送。
func verifyNotificationReplySubmission(ctx context.Context, page *hrod.Page, content string, initialCount int) error {
	const maxChecks = 12
	for check := 0; check < maxChecks; check++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, errText, err := notificationReplySubmissionState(page, content)
		if err != nil {
			return fmt.Errorf("检查回复结果失败: %w", err)
		}
		if errText != "" {
			return fmt.Errorf("页面提示: %s", errText)
		}
		if count > initialCount {
			return nil
		}
		gone, goneErr := notificationReplyInputGone(page)
		if goneErr == nil && gone {
			return nil
		}
		if err := page.SleepRandom(500*time.Millisecond, 1200*time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("回复提交未确认（textarea 仍在或内容未出现），不重试发送")
}

// notificationReplyInputGone 判断回复输入框是否已消失或隐藏。
func notificationReplyInputGone(page *hrod.Page) (bool, error) {
	obj, err := page.Eval(`() => {
		const els = Array.from(document.querySelectorAll('textarea.comment-input'));
		if (els.length === 0) return JSON.stringify(true);
		return JSON.stringify(els.every((el) => el.offsetParent === null || el.offsetWidth === 0 || el.offsetHeight === 0));
	}`)
	if err != nil {
		return false, err
	}
	if obj == nil {
		return false, fmt.Errorf("textarea 状态无返回")
	}
	var gone bool
	if err := json.Unmarshal([]byte(obj.Value.Str()), &gone); err != nil {
		return false, fmt.Errorf("解析 textarea 状态失败: %w", err)
	}
	return gone, nil
}

// notificationReplyMatchCount 统计通知列表中包含回复内容的叶子元素数量。
func notificationReplyMatchCount(page *hrod.Page, content string) (int, error) {
	obj, err := page.Eval(`(content) => {
		const container = document.querySelector('.tabs-content-container');
		if (!container) return 0;
		const leaf = Array.from(container.querySelectorAll('*')).filter((el) => el.children.length === 0);
		return leaf.filter((el) => (el.textContent || "").includes(content)).length;
	}`, content)
	if err != nil {
		return 0, err
	}
	if obj == nil {
		return 0, fmt.Errorf("页面未返回回复计数")
	}
	return obj.Value.Int(), nil
}

// notificationReplySubmissionState 一次性读取提交后的计数与错误提示。
func notificationReplySubmissionState(page *hrod.Page, content string) (int, string, error) {
	obj, err := page.Eval(`(content) => {
		const container = document.querySelector('.tabs-content-container');
		const base = container ? Array.from(container.querySelectorAll('*')).filter((el) => el.children.length === 0).filter((el) => (el.textContent || "").includes(content)).length : 0;
		const errorKeywords = ["操作频繁", "回复过于频繁", "请验证", "滑块验证", "安全验证", "回复失败", "发送失败", "提交失败", "禁止回复"];
		const pageText = document.body?.innerText || "";
		const error = errorKeywords.find((keyword) => pageText.includes(keyword)) || "";
		return JSON.stringify({ matchCount: base, error });
	}`, content)
	if err != nil {
		return 0, "", err
	}
	if obj == nil {
		return 0, "", fmt.Errorf("页面未返回提交状态")
	}
	var state struct {
		MatchCount int    `json:"matchCount"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal([]byte(obj.Value.Str()), &state); err != nil {
		return 0, "", fmt.Errorf("解析回复提交状态失败: %w", err)
	}
	return state.MatchCount, state.Error, nil
}