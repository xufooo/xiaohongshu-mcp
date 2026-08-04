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

// NotificationTab 通知 tab 类型
type NotificationTab string

const (
	TabMentions    NotificationTab = "mentions"
	TabLikes       NotificationTab = "likes"
	TabConnections NotificationTab = "connections"
)

// ParseNotificationTab 解析 tab 参数；空值默认 mentions，非法值报错。
func ParseNotificationTab(raw string) (NotificationTab, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return TabMentions, nil
	}
	switch NotificationTab(raw) {
	case TabMentions, TabLikes, TabConnections:
		return NotificationTab(raw), nil
	default:
		return "", fmt.Errorf("非法通知 tab: %q（仅支持 mentions|likes|connections）", raw)
	}
}

// notificationTabLabel 返回 tab 在通知页中的 DOM 文本。
func notificationTabLabel(tab NotificationTab) string {
	switch tab {
	case TabLikes:
		return "赞和收藏"
	case TabConnections:
		return "新增关注"
	default:
		return "评论和@"
	}
}

// statusNormal 内容正常可见状态；非此取值视为不可见。
const statusNormal = "NORMAL"

// itemTypeNote 关联对象是一篇笔记；仅此类型 id 与笔记 id 通用。
const itemTypeNote = "note_info"

// NotificationCount get_unread_count 结果：对象型，三个分区明细 + 未读总数。
type NotificationCount struct {
	Mentions    int  `json:"mentions"`
	Likes       int  `json:"likes"`
	Connections int  `json:"connections"`
	Unread      int  `json:"unread"`
	HasState    bool `json:"has_state"`
}

// NotificationUser 通知发起用户
type NotificationUser struct {
	UserID    string `json:"user_id"`
	Nickname  string `json:"nickname"`
	XsecToken string `json:"xsec_token,omitempty"`
}

// NotificationItem 通知条目（只读信息 + 写操作引用）
type NotificationItem struct {
	NotificationRef string           `json:"notification_ref"`
	ID             string           `json:"id"`
	Type           string           `json:"type"`
	Title          string           `json:"title"`
	Time           int64            `json:"time"`
	From           NotificationUser `json:"from"`
	CommentID      string           `json:"comment_id,omitempty"`
	CommentText    string           `json:"comment_text,omitempty"`
	Liked          bool             `json:"liked"`
	FeedID         string           `json:"feed_id,omitempty"`
	FeedXsecToken  string           `json:"feed_xsec_token,omitempty"`
	FeedTitle      string           `json:"feed_title,omitempty"`
	Actionable     bool             `json:"actionable"`
}

// NotificationList list_notifications 结果。
// Filtered 是被过滤的不可见条目数；ClearsUnread 恒为 true：进入通知页/切 tab 会使对应未读被清除。
type NotificationList struct {
	Tab          NotificationTab    `json:"tab"`
	Items        []NotificationItem `json:"items"`
	Filtered     int                `json:"filtered"`
	Cursor       string             `json:"cursor,omitempty"`
	HasMore      bool               `json:"has_more"`
	ClearsUnread bool               `json:"clears_unread"`
	ResultCount  int                `json:"result_count"`
}

// notificationTarget 会话内解析出的通知写操作目标。校验统一从 Item 读取。
type notificationTarget struct {
	Ref        string
	Tab        NotificationTab
	Generation uint64
	Item       NotificationItem
}

// notificationPayload 通知分区在 __INITIAL_STATE__.notification.notificationMap[tab] 中的真实结构。
type notificationPayload struct {
	HasMore     bool              `json:"hasMore"`
	MessageList []rawNotification `json:"messageList"`
}

// rawNotification 真实通知条目：commentInfo/itemInfo/illegalInfo 嵌套 + userid 字段。
type rawNotification struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Title    string     `json:"title"`
	Time     int64      `json:"time"`
	UserInfo rawUser    `json:"userInfo"`
	User     rawUser    `json:"user"`
	Comment  rawComment `json:"commentInfo"`
	Item     rawItem    `json:"itemInfo"`
}

type rawUser struct {
	UserID    string `json:"userid"`
	Nickname  string `json:"nickname"`
	XsecToken string `json:"xsecToken"`
}

type rawComment struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Liked   bool       `json:"liked"`
	Illegal rawIllegal `json:"illegalInfo"`
}

type rawItem struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Content   string     `json:"content"`
	XsecToken string     `json:"xsecToken"`
	Illegal   rawIllegal `json:"illegalInfo"`
}

type rawIllegal struct {
	Status string `json:"illegalStatus"`
}

// from 返回发起这条通知的用户，userInfo 优先。
func (r rawNotification) from() rawUser {
	if r.UserInfo.UserID != "" || r.UserInfo.Nickname != "" {
		return r.UserInfo
	}
	return r.User
}

// visible 判断评论/笔记是否仍正常可见；非法状态视为不可见。
func (r rawNotification) visible() bool {
	if r.Comment.ID != "" && r.Comment.Illegal.Status != "" && r.Comment.Illegal.Status != statusNormal {
		return false
	}
	if r.Item.ID != "" && r.Item.Illegal.Status != "" && r.Item.Illegal.Status != statusNormal {
		return false
	}
	return true
}

// notificationDOMItem 通知 DOM 行的快照（readNotificationDOMSnapshot 产出）。
type notificationDOMItem struct {
	UserID      string `json:"user_id"`
	Nickname    string `json:"nickname"`
	Content     string `json:"content"`
	XsecToken   string `json:"xsec_token"`
	HasLike     bool   `json:"has_like"`
	HasReply    bool   `json:"has_reply"`
	LikeUseHref string `json:"like_use"`
}

// notificationDOMSnapshot 当前通知 surface 的 DOM 快照。
type notificationDOMSnapshot struct {
	Items []notificationDOMItem
}

// normalizeNotificationText 规范化昵称/评论文本：去空白、统一为单空格。
func normalizeNotificationText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// notificationItemFingerprint 通知行指纹：用户 ID + 规范化昵称 + 规范化评论内容。
func notificationItemFingerprint(userID, nickname, content string) string {
	return strings.Join([]string{
		strings.TrimSpace(userID),
		normalizeNotificationText(nickname),
		normalizeNotificationText(content),
	}, "|")
}

// parseNotificationLikeHref 解析点赞 svg use 的 href：#liked=true / #like=false。
// use 缺失或未知值报错，fail-closed；禁止读取 like-active 类。
func parseNotificationLikeHref(href string) (bool, error) {
	switch strings.TrimSpace(href) {
	case "#liked":
		return true, nil
	case "#like":
		return false, nil
	default:
		return false, fmt.Errorf("点赞状态 use href 未知或缺失: %q（禁止据此点击）", href)
	}
}

// readNotificationCount 读取 __INITIAL_STATE__.notification.notificationCount 对象，
// 解析出 mentions/likes/connections 三个分区明细与 unreadCount（真实结构为对象而非数字）。
// 兼容 Vue value/_value 包装；state 或 count 缺失返回结构漂移错误，禁止误报为 0。
// 全程不点击、不导航。
func readNotificationCount(page *hrod.Page) (*NotificationCount, error) {
	obj, err := page.Eval(`() => {
		const unwrap = (v) => {
			if (v && typeof v === "object") {
				if ("value" in v) return v.value;
				if ("_value" in v) return v._value;
			}
			return v;
		};
		const noti = window.__INITIAL_STATE__ && window.__INITIAL_STATE__.notification;
		if (!noti) return JSON.stringify({ has_state: false });
		const c = unwrap(noti.notificationCount);
		if (!c || typeof c !== "object") return JSON.stringify({ has_state: true, count_missing: true });
		const num = (v) => { const x = unwrap(v); return typeof x === "number" ? x : Number(x) || 0; };
		return JSON.stringify({
			has_state: true,
			mentions: num(c.mentions),
			likes: num(c.likes),
			connections: num(c.connections),
			unread: num(c.unreadCount !== undefined ? c.unreadCount : c.unread),
		});
	}`)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("未读数探测无返回")
	}
	return decodeNotificationCount(obj.Value.Str())
}

// decodeNotificationCount 解析未读数探测 JSON（对象型三分区明细）为 NotificationCount。
// state 或 count 缺失时返回结构漂移错误，禁止误报为 0。
func decodeNotificationCount(raw string) (*NotificationCount, error) {
	var r struct {
		HasState     bool `json:"has_state"`
		CountMissing bool `json:"count_missing"`
		Mentions     int  `json:"mentions"`
		Likes        int  `json:"likes"`
		Connections  int  `json:"connections"`
		Unread       int  `json:"unread"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("解析未读数失败: %w", err)
	}
	if !r.HasState {
		return nil, fmt.Errorf("notification state 缺失（结构漂移），禁止误报为 0")
	}
	if r.CountMissing {
		return nil, fmt.Errorf("notificationCount 缺失（结构漂移），禁止误报为 0")
	}
	return &NotificationCount{
		Mentions:    r.Mentions,
		Likes:       r.Likes,
		Connections: r.Connections,
		Unread:      r.Unread,
		HasState:    true,
	}, nil
}

// enterNotificationPage 只通过侧栏通知入口真实点击进入通知页；禁止直接导航 /notification。
// 点击前拟人停留，使用真实 Click；点击后等待 XHSReadyNotification，超时上限 15 秒。
// 当前是详情弹层且入口不可交互时 fail-closed，提示先 go_back。
func enterNotificationPage(ctx context.Context, page *hrod.Page) error {
	entry, err := page.Element(SelectorNotificationEntry)
	if err != nil {
		return fmt.Errorf("未找到通知入口（可能在详情弹层中，请先 go_back）: %w", err)
	}
	if err := page.SleepRandom(800*time.Millisecond, 1500*time.Millisecond); err != nil {
		return err
	}
	if err := entry.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击通知入口失败（可能是详情弹层遮挡，请先 go_back）: %w", err)
	}
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: XHSReadyNotification, Timeout: 15 * time.Second}); err != nil {
		return err
	}
	return nil
}

// switchNotificationTab 精确定位匹配三个通知 tab。
// 已 active 不点击；非 active 拟人停留后点击，并等待目标 tab 获得 .active。
func switchNotificationTab(ctx context.Context, page *hrod.Page, tab NotificationTab) error {
	label := notificationTabLabel(tab)
	tabs, err := page.Elements(SelectorNotificationTab)
	if err != nil {
		return fmt.Errorf("读取通知 tab 失败: %w", err)
	}
	var target *hrod.Element
	for _, t := range tabs {
		text, textErr := t.Text()
		if textErr != nil {
			continue
		}
		if normalizeNotificationText(text) == label {
			target = t
			break
		}
	}
	if target == nil {
		return fmt.Errorf("通知页未找到 tab %q", label)
	}
	activeObj, err := target.Eval(`() => this.classList.contains("active")`)
	if err != nil {
		return fmt.Errorf("读取 tab active 状态失败: %w", err)
	}
	if activeObj != nil && activeObj.Value.Bool() {
		return nil
	}
	if err := page.SleepRandom(600*time.Millisecond, 1200*time.Millisecond); err != nil {
		return err
	}
	if err := target.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击 tab %q 失败: %w", label, err)
	}
	return waitNotificationTabActive(ctx, page, tab)
}

// waitNotificationTabActive 轮询等待目标 tab 获得 .active（上限 10 秒）。
func waitNotificationTabActive(ctx context.Context, page *hrod.Page, tab NotificationTab) error {
	label := notificationTabLabel(tab)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		obj, err := page.Eval(`(label) => {
			const active = Array.from(document.querySelectorAll('.notification-page .reds-tab-item.tab-item.active'));
			return active.some((el) => String(el.textContent || "").replace(/\s+/g, " ").trim() === label);
		}`, label)
		if err == nil && obj != nil && obj.Value.Bool() {
			return nil
		}
		if err := page.SleepRandom(300*time.Millisecond, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("等待 tab %q 激活超时", label)
}

// readNotificationTabState 读取指定 tab 的真实 state：notificationMap[tab].messageList。
// 兼容 Vue value/_value 包装；notification state 缺失或分区缺失都直接报错（fail-closed，不掩盖漂移）。
func readNotificationTabState(page *hrod.Page, tab NotificationTab) (*notificationPayload, error) {
	obj, err := page.Eval(`(tab) => {
		const unwrap = (v) => {
			if (v && typeof v === "object") {
				if ("value" in v) return v.value;
				if ("_value" in v) return v._value;
			}
			return v;
		};
		const noti = window.__INITIAL_STATE__ && window.__INITIAL_STATE__.notification;
		if (!noti) return JSON.stringify({ status: "no_state" });
		const m = unwrap(noti.notificationMap) || noti.notificationMap;
		const p = m && m[tab] ? unwrap(m[tab]) : null;
		if (!p) return JSON.stringify({ status: "no_tab" });
		return JSON.stringify({ status: "ok", payload: p });
	}`, string(tab))
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("通知 state 探测无返回")
	}
	var raw struct {
		Status  string               `json:"status"`
		Payload *notificationPayload `json:"payload"`
	}
	if err := json.Unmarshal([]byte(obj.Value.Str()), &raw); err != nil {
		return nil, fmt.Errorf("解析通知 state 失败: %w", err)
	}
	switch raw.Status {
	case "no_state":
		return nil, fmt.Errorf("notification state 缺失（结构漂移）")
	case "ok":
		if raw.Payload == nil {
			return nil, fmt.Errorf("页面状态里没有通知分区 %q，可能未登录或页面结构已变化", tab)
		}
		return raw.Payload, nil
	default:
		return nil, fmt.Errorf("页面状态里没有通知分区 %q，可能未登录或页面结构已变化", tab)
	}
}

// readNotificationDOMSnapshot 读取当前通知 surface 的 DOM 行快照。
// 头像链接解析用户 ID 与 xsec_token；点赞状态仅读取 svg use 的 href。
func readNotificationDOMSnapshot(page *hrod.Page) (notificationDOMSnapshot, error) {
	obj, err := page.Eval(`(itemSelector, avatarSel, nickSel, contentSel, likeSel, replySel, likeUseSel) => {
		const normalize = (s) => String(s || "").replace(/\s+/g, " ").trim();
		const hrefUserID = (a) => {
			const href = a ? String(a.getAttribute("href") || "") : "";
			const m = href.match(/\/user\/profile\/([^?&]+)/);
			return m ? decodeURIComponent(m[1]) : "";
		};
		const hrefXsec = (a) => {
			const href = a ? String(a.getAttribute("href") || "") : "";
			const m = href.match(/[?&]xsec_token=([^&]+)/);
			return m ? decodeURIComponent(m[1]) : "";
		};
		const useHref = (item, sel) => {
			const use = item.querySelector(sel);
			if (!use) return "";
			return String(use.getAttribute("xlink:href") || use.getAttribute("href") || "").trim();
		};
		const items = Array.from(document.querySelectorAll(itemSelector));
		return JSON.stringify(items.map((item) => {
			const avatar = item.querySelector(avatarSel);
			const nick = item.querySelector(nickSel);
			const contentEl = item.querySelector(contentSel);
			return {
				user_id: hrefUserID(avatar),
				nickname: normalize(nick ? nick.textContent : ""),
				content: normalize(contentEl ? contentEl.textContent : ""),
				xsec_token: hrefXsec(avatar),
				has_like: Boolean(item.querySelector(likeSel)),
				has_reply: Boolean(item.querySelector(replySel)),
				like_use: useHref(item, likeUseSel),
			};
		}));
	}`, SelectorNotificationItem, SelectorNotificationUserAvatar, SelectorNotificationNickname,
		SelectorNotificationContent,
		SelectorNotificationLikeButton, SelectorNotificationReplyButton, SelectorNotificationLikeUse)
	if err != nil {
		return notificationDOMSnapshot{}, err
	}
	if obj == nil {
		return notificationDOMSnapshot{}, fmt.Errorf("通知 DOM 快照探测无返回")
	}
	var raw []notificationDOMItem
	if err := json.Unmarshal([]byte(obj.Value.Str()), &raw); err != nil {
		return notificationDOMSnapshot{}, fmt.Errorf("解析通知 DOM 快照失败: %w", err)
	}
	return notificationDOMSnapshot{Items: raw}, nil
}

// convertNotifications 将真实 state 原始条目转换为对外 NotificationItem，并过滤不可见条目。
// 仅 mentions tab 且 DOM 唯一匹配 + 同时存在点赞/回复入口的条目才 actionable；
// refFor 仅为 actionable 条目签发。返回条目与被过滤数。
func convertNotifications(raw []rawNotification, dom notificationDOMSnapshot, tab NotificationTab, refFor func(entry rawNotification, index int) string) ([]NotificationItem, int) {
	items := make([]NotificationItem, 0, len(raw))
	filtered := 0
	for i, r := range raw {
		if !r.visible() {
			filtered++
			continue
		}
		u := r.from()
		item := NotificationItem{
			ID:          r.ID,
			Type:        r.Type,
			Title:       r.Title,
			Time:        r.Time,
			From:        NotificationUser{UserID: u.UserID, Nickname: u.Nickname, XsecToken: u.XsecToken},
			CommentID:   r.Comment.ID,
			CommentText: r.Comment.Content,
			Liked:       r.Comment.Liked,
			FeedTitle:   r.Item.Content,
		}
		if r.Item.Type == itemTypeNote {
			item.FeedID = r.Item.ID
			item.FeedXsecToken = r.Item.XsecToken
		}
		if tab == TabMentions {
			if matched := matchNotificationDOM(r, dom); matched != nil {
				if item.From.XsecToken == "" {
					item.From.XsecToken = matched.XsecToken
				}
				item.Actionable = matched.HasLike && matched.HasReply
			}
		}
		if item.Actionable && refFor != nil {
			item.NotificationRef = refFor(r, i)
		}
		items = append(items, item)
	}
	return items, filtered
}

// matchNotificationDOM 在 DOM 行中查找与 state 条目指纹唯一匹配的行；歧义或未匹配返回 nil。
func matchNotificationDOM(entry rawNotification, dom notificationDOMSnapshot) *notificationDOMItem {
	u := entry.from()
	fp := notificationItemFingerprint(u.UserID, u.Nickname, entry.Comment.Content)
	var matched *notificationDOMItem
	for i := range dom.Items {
		item := &dom.Items[i]
		if notificationItemFingerprint(item.UserID, item.Nickname, item.Content) != fp {
			continue
		}
		if matched != nil {
			return nil // 同名同文歧义，不签发可写 ref
		}
		matched = item
	}
	return matched
}

// verifyNotificationTargetInState 点击前重新读取 tab state，复核 comment ID / user ID 仍存在。
// 匹配真实 messageList 里的 commentInfo.id，确认发起用户一致且仍可见；否则 fail-closed。
func verifyNotificationTargetInState(page *hrod.Page, target notificationTarget) error {
	state, err := readNotificationTabState(page, target.Tab)
	if err != nil {
		return fmt.Errorf("读取通知 state 复核失败: %w", err)
	}
	for _, r := range state.MessageList {
		if r.Comment.ID != target.Item.CommentID {
			continue
		}
		if u := r.from(); target.Item.From.UserID != "" && u.UserID != target.Item.From.UserID {
			continue
		}
		if !r.visible() {
			return fmt.Errorf("通知评论已不可见（已删除或非法），禁止操作")
		}
		return nil
	}
	return fmt.Errorf("通知条目 state 复核失败：comment/user 目标已不在当前列表，请重新 list_notifications")
}