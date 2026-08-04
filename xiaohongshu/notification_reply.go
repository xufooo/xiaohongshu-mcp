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
// 输入后用 textarea value 核对；提交按钮校验文本"发送"（可见性/禁用交给 hrod Click 的 interactable 检查）；
// 发送后以目标行内 textarea 消失/隐藏作为确认，不重试发送。
func replyNotificationOnPage(ctx context.Context, page *hrod.Page, target notificationTarget, content string) (*NotificationReplyResult, error) {
	if err := verifyNotificationTargetInState(page, target); err != nil {
		return nil, err
	}
	row, _, err := findNotificationRowElement(page, target)
	if err != nil {
		return nil, err
	}
	if target.Item.CommentID == "" {
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
	if err := verifyNotificationReplyPlaceholder(inputEl, target.Item.From.Nickname); err != nil {
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
	if err := waitNotificationReplyAccepted(ctx, page, row); err != nil {
		return nil, err
	}
	return &NotificationReplyResult{Ref: target.Ref, CommentID: target.Item.CommentID, Sent: true, Message: "回复已发送"}, nil
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

// findNotificationSubmitButton 仅在目标通知行内查找发送按钮，校验文本为「发送」且未被禁用；找不到即报错（fail-closed）。
// 按钮可见性/遮挡交给 hrod Click 的 interactable 检查；原生 disabled/aria-disabled/.disabled 在此精简校验。
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
	obj, err := btn.Eval(`() => this.disabled || this.getAttribute("aria-disabled") === "true" || this.classList.contains("disabled")`)
	if err != nil {
		return nil, fmt.Errorf("检查发送按钮禁用状态失败: %w", err)
	}
	if obj != nil && obj.Value.Bool() {
		return nil, fmt.Errorf("发送按钮处于禁用状态，禁止点击")
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

// waitNotificationReplyAccepted 提交后轮询确认：目标行内 textarea.comment-input 消失/隐藏即成功；
// 同一次 Eval 顺带检查明确的风控/发送失败提示。最长等待 8 秒，不重试发送。
func waitNotificationReplyAccepted(ctx context.Context, page *hrod.Page, row *hrod.Element) error {
	if row == nil {
		return fmt.Errorf("等待回复提交确认失败: 目标通知行缺失")
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		obj, err := row.Eval(`() => {
			const input = this.querySelector('textarea.comment-input');
			const gone = !input || input.offsetParent === null || input.offsetWidth === 0 || input.offsetHeight === 0;
			const keywords = ["操作频繁", "回复过于频繁", "请验证", "滑块验证", "安全验证", "回复失败", "发送失败", "提交失败", "禁止回复"];
			const pageText = document.body?.innerText || "";
			const error = keywords.find((keyword) => pageText.includes(keyword)) || "";
			return JSON.stringify({ gone, error });
		}`)
		if err == nil && obj != nil {
			var state struct {
				Gone  bool   `json:"gone"`
				Error string `json:"error"`
			}
			if jsonErr := json.Unmarshal([]byte(obj.Value.Str()), &state); jsonErr == nil {
				if state.Error != "" {
					return fmt.Errorf("页面提示: %s", state.Error)
				}
				if state.Gone {
					return nil
				}
			}
		}
		if err := page.SleepRandom(500*time.Millisecond, 1200*time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("回复提交未确认（输入框仍在），不重试发送")
}
