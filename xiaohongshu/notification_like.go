package xiaohongshu

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// NotificationLikeResult like_notification 结果。
type NotificationLikeResult struct {
	Ref     string `json:"notification_ref"`
	Liked   bool   `json:"liked"`
	Skipped bool   `json:"skipped"`
	Message string `json:"message,omitempty"`
}

// likeNotificationOnPage 对通知行点赞/取消点赞（幂等）。
// 状态只以 svg use 的 href 判定（#liked/#like），禁止读取 like-active 类；
// 初始状态直接复用 findNotificationRowElement 返回的 DOM 快照，避免重复读取完整 snapshot；
// 点击使用 hrod 真实 Click；点击后轮询重新读取 snapshot 确认翻转，未确认报 state_unknown。
func likeNotificationOnPage(ctx context.Context, page *hrod.Page, target notificationTarget, unlike bool) (*NotificationLikeResult, error) {
	if err := verifyNotificationTargetInState(page, target); err != nil {
		return nil, err
	}
	row, matched, err := findNotificationRowElement(page, target)
	if err != nil {
		return nil, err
	}
	current, err := parseNotificationLikeHref(matched.LikeUseHref)
	if err != nil {
		return nil, fmt.Errorf("读取点赞状态失败，取消点击: %w", err)
	}
	if unlike && !current {
		return &NotificationLikeResult{Ref: target.Ref, Liked: false, Skipped: true, Message: "已处于未点赞状态，无需操作"}, nil
	}
	if !unlike && current {
		return &NotificationLikeResult{Ref: target.Ref, Liked: true, Skipped: true, Message: "已处于点赞状态，无需操作"}, nil
	}

	likeBtn, err := row.Element(SelectorNotificationLikeButton)
	if err != nil {
		return nil, fmt.Errorf("通知行点赞按钮缺失: %w", err)
	}
	if err := page.SleepRandom(800*time.Millisecond, 1500*time.Millisecond); err != nil {
		return nil, err
	}
	if err := likeBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return nil, fmt.Errorf("点击点赞按钮失败: %w", err)
	}

	want := !unlike
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := page.SleepRandom(400*time.Millisecond, 800*time.Millisecond); err != nil {
			return nil, err
		}
		got, readErr := readNotificationLikeState(page, target)
		if readErr != nil {
			continue
		}
		if got == want {
			return &NotificationLikeResult{Ref: target.Ref, Liked: want, Message: "操作成功"}, nil
		}
	}
	return nil, fmt.Errorf("state_unknown: 点击后点赞状态未确认，不自动二次点击，请重新 list_notifications 后核对")
}

// matchNotificationDOMItem 在 DOM 快照中按指纹唯一匹配通知写操作目标。
// readNotificationLikeState 与 findNotificationRowElement 共用，保证匹配逻辑单一来源。
// 保留指纹唯一性、歧义拒绝；未匹配或歧义均报错，禁止据此操作。
// 返回匹配项与在 Items 中的索引（供元素重定位）。
func matchNotificationDOMItem(dom *notificationDOMSnapshot, target notificationTarget) (*notificationDOMItem, int, error) {
	fp := notificationItemFingerprint(target.Item.From.UserID, target.Item.From.Nickname, target.Item.CommentText)
	index := -1
	var matched *notificationDOMItem
	for i := range dom.Items {
		item := &dom.Items[i]
		if notificationItemFingerprint(item.UserID, item.Nickname, item.Content) != fp {
			continue
		}
		if index >= 0 {
			return nil, -1, fmt.Errorf("通知行指纹歧义，无法唯一匹配")
		}
		index = i
		matched = item
	}
	if index < 0 {
		return nil, -1, fmt.Errorf("未找到匹配的通知行（可能已翻页或失效）")
	}
	return matched, index, nil
}

// readNotificationLikeState 重新读取 DOM 快照，按指纹唯一匹配通知行并解析 svg use href。
// 点击后的轮询继续使用，以兼容前端重新渲染。
func readNotificationLikeState(page *hrod.Page, target notificationTarget) (bool, error) {
	dom, err := readNotificationDOMSnapshot(page)
	if err != nil {
		return false, err
	}
	matched, _, err := matchNotificationDOMItem(&dom, target)
	if err != nil {
		return false, err
	}
	return parseNotificationLikeHref(matched.LikeUseHref)
}

// findNotificationRowElement 按指纹唯一匹配当前通知列表中的 DOM 行元素，并返回该行的快照条目。
// 返回行元素、匹配快照；歧义或未匹配报错。
func findNotificationRowElement(page *hrod.Page, target notificationTarget) (*hrod.Element, *notificationDOMItem, error) {
	dom, err := readNotificationDOMSnapshot(page)
	if err != nil {
		return nil, nil, err
	}
	matched, index, err := matchNotificationDOMItem(&dom, target)
	if err != nil {
		return nil, nil, err
	}
	rows, err := page.Elements(SelectorNotificationItem)
	if err != nil {
		return nil, nil, err
	}
	if index >= len(rows) {
		return nil, nil, fmt.Errorf("通知行索引越界: %d/%d", index, len(rows))
	}
	return rows[index], matched, nil
}
