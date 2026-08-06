# P2 唯一实施单（MINIMAL PLAN）

基线：`fixup-test@d2fdc10`  
目标：新增 4 个 session 通知工具，注册数 `18 → 22`。禁止 `acquirePageFor`、禁止直接导航 `/notification`、禁止修改 `pkg/humanize/rod`。

P0 依据：[p0-dom-evidence.md](/home/dietpi/p0-dom-evidence.md)

## 一、公开工具与参数定稿

| 工具 | 参数 | 语义 |
|---|---|---|
| `get_unread_count` | `session_id` | 从当前保留页面读取 `__INITIAL_STATE__.notification.notificationCount`；不点击通知入口，不清未读 |
| `list_notifications` | `session_id`, `tab`, `max_items`, `cursor` | 通过侧栏真实点击进入通知页、切换 tab、拟人滚动分页；明确披露查看会清未读 |
| `like_notification` | `session_id`, `notification_ref`, `unlike`, `confirm_token` | 对当前通知 surface 中的评论通知点赞/取消，幂等 |
| `reply_notification` | `session_id`, `notification_ref`, `content`, `confirm_token` | 回复当前通知 surface 中的评论通知 |

约束：

- `tab` 只允许 `mentions | likes | connections`，空值默认 `mentions`。
- `max_items` 默认 10、最大 20。
- `cursor` 只允许续读当前 session、当前 generation、当前 tab。
- `notification_ref` 只在当前 notification generation 有效。
- `list_notifications` 不标记为 MCP `ReadOnlyHint=true`，因为进入通知页/tab 会清未读；工具描述和结果都必须披露这一副作用。
- `get_unread_count` 标记 `ReadOnlyHint=true`。
- `like_notification`、`reply_notification` 标记 `DestructiveHint=true`。

## 二、文件白名单与精确改动

### 1. 新增 `xiaohongshu/notification.go`

新增通知读取、解析和 DOM/state 对齐逻辑：

- 定义：

  - `NotificationTab`
  - `TabMentions`、`TabLikes`、`TabConnections`
  - `ParseNotificationTab`
  - `NotificationCount`
  - `NotificationUser`
  - `NotificationItem`
  - `NotificationList`
  - `NotificationCursor`
  - 内部 `notificationTarget`、`notificationDOMSnapshot`

- `NotificationItem` 至少返回：

  - `notification_ref`
  - `id`
  - `type`
  - `title`
  - `time`
  - `from.user_id/nickname/xsec_token`
  - `comment_id/comment_text/liked`
  - `feed_id/feed_xsec_token/feed_title`
  - `actionable`

- 借鉴上游 main：

  - `notificationCount`、`notificationMap[tab]` 的状态解析。
  - `NORMAL` 可见性过滤。
  - `mentions/likes/connections` 三种 tab 映射。
  - `userInfo` 与 `user` 的兼容读取。

- 不照搬上游：

  - 不 `MustNavigate`。
  - 不创建全局页面。
  - 不使用数组下标直接定位写操作目标。
  - 不使用 raw `*rod.Page`、根目录 `humanize`、`time.Sleep` 或 `page.Timeout(...)`。

新增核心函数：

- `readNotificationCount(page *hrod.Page)`

  - 单次 `Eval` 读取 `__INITIAL_STATE__.notification.notificationCount`。
  - 兼容 Vue 包装字段 `value/_value`。
  - 同时读取通知入口 badge 作为交叉信息。
  - state 缺失时返回结构漂移错误，禁止把缺失误报为 0。
  - 全程不点击、不导航、不改变 notification generation。

- `enterNotificationPage(ctx, page)`

  - 只通过 `SelectorNotificationEntry` 找到侧栏入口。
  - 点击前 `SleepRandom`，使用 hrod 真实 `Click`。
  - 点击后等待 `XHSReadyNotification`，超时上限 15 秒。
  - 如果当前是详情弹层且入口不可交互，fail-closed，并提示先 `go_back`；不机械关闭或直接导航。

- `switchNotificationTab(ctx, page, tab)`

  - 精确文本匹配三个 tab。
  - 已 active 时不点击。
  - 非 active 时拟人停留、点击，并等待目标 tab 获得 `.active` 且对应 state 出现。
  - 每次切 tab视为新 generation，会清旧 ref/cursor。

- `readNotificationTab(page, tab)`、`convertNotifications(...)`

  - 读取 state 列表、`hasMore`，过滤非法内容。
  - 与当前 DOM item 建立唯一对应关系。
  - mentions 行按用户 ID、规范化昵称、规范化评论内容形成 fingerprint。
  - 只有 DOM 唯一匹配且存在 `.action-like/.action-reply` 的 mentions item 才设置 `actionable=true` 并生成 ref。
  - 匹配缺失或歧义时仍可返回只读信息，但不签发可写 ref。

- `scrollNotificationPage(...)`

  - 真实鼠标滚动，每轮约 350–700px。
  - 每轮 `SleepRandom(1.5s, 3.5s)`。
  - 单次调用最多 6 轮，随时检查 `ctx`。
  - 达到 `max_items`、`hasMore=false`、连续无新增或预算不足即停止。

### 2. 新增 `xiaohongshu/notification_like.go`

定义 `NotificationLikeResult` 和点赞实现：

- 输入只能是 session 内解析出的 `notificationTarget`，不能接收裸 `comment_id`。
- 操作前重新验证：

  - ref 属于当前 generation 和 `mentions` tab。
  - state 中仍存在相同 comment ID/user ID。
  - DOM fingerprint 唯一匹配。

- 点赞状态只读：

  - `.action-like svg use`
  - 优先 `xlink:href`，兼容 `href`
  - `#liked → true`
  - `#like → false`
  - use 缺失或未知值立即报错，fail-closed
  - 禁止读取 `like-active`

- 幂等逻辑：

  - 当前状态已等于目标状态则 `Skipped=true`，不点击。
  - 否则拟人停留后点击 `.action-like` wrapper 一次。
  - 最长 15 秒轮询同一 DOM 行的 href。
  - 不自动二次点击；未确认时返回“可能已生效，先重新 list”的错误。

- 成功后更新 session target 的 `Liked`，记录 timeline 和 `ActionStateStore.RecordInteraction("", "like_notification")`。

### 3. 新增 `xiaohongshu/notification_reply.go`

定义 `NotificationReplyResult` 和回复实现。

回复前执行三层目标校验：

1. **引用校验**：ref 属于当前 session、generation、`mentions` tab，且 target 有非空 comment ID。
2. **通知行校验**：当前 state 仍有相同 comment ID/user ID；DOM 行的头像链接 user ID、昵称、评论文本 fingerprint 与 target 一致且唯一。
3. **编辑器校验**：点击该行 `.action-reply` 后，行内出现 `textarea.comment-input`，其 placeholder 必须匹配 `回复 <昵称>`。

输入与发送：

- 点击 `.action-reply` 后使用 hrod `Input(content)` 输入 textarea。
- 读取 textarea `value`，做空白归一化后必须与输入一致；否则不发送。
- 提交按钮必须从目标 item 内相对查找 `.input-buttons .submit`。
- 校验按钮文本为“发送”、可见、可交互，再点击一次。
- 最长等待 8 秒，以目标行 textarea 消失或隐藏作为提交确认。
- 未确认时不得重试发送。
- 成功记录 timeline 和 `RecordInteraction("", "reply_notification")`。

### 4. `xiaohongshu/ui_selectors.go`

在当前常量区（基线约第 9–21 行）新增：

```go
SelectorNotificationEntry
SelectorNotificationBadge
SelectorNotificationPage
SelectorNotificationTab
SelectorNotificationItem
SelectorNotificationUserAvatar
SelectorNotificationNickname
SelectorNotificationHint
SelectorNotificationTime
SelectorNotificationContent
SelectorNotificationReplyButton
SelectorNotificationLikeButton
SelectorNotificationLikeUse
SelectorNotificationReplyInput
SelectorNotificationReplySubmit
```

定稿值：

```text
a[href="/notification"]
a[href="/notification"] .badge-container
.notification-page
.notification-page .reds-tab-item.tab-item
.notification-page .tabs-content-container .container
.user-avatar
.user-info a
.interaction-hint span
.interaction-time
.interaction-content
.action-reply
.action-like
.action-like svg use
textarea.comment-input
.input-buttons .submit
```

相对 item 使用的选择器不得再次带全局 `.container` 前缀。

在 selector spec 区（基线约第 41–79 行）新增：

- `NotificationEntrySpec`
- `NotificationPageSpec`
- `NotificationTabSpec`
- `NotificationItemSpec`

规则：

- page、tab 为 required。
- item 允许 0 条，不设 required。
- tab `MaxMatches=3`。
- 本期不加入主页 tab 常量；P0 虽已取证，但 P2 不实现 profile tab，避免未使用范围扩张。

### 5. `xiaohongshu/selector_watchdog.go`

基线位置：

- `RegisterAll()`：约第 81–87 行。
- `selectorsForKind()`：约第 90–107 行。
- 可见性判断：约第 130 行及状态判定段。

改动：

- 注册 notification entry/page/tab/item 四个 spec。
- 新增 `XHSReadyNotification` 分支，探测 page/tab/item。
- notification page、tab 按可见命中判断。
- item 为空只能是 suspicious，不得使空通知列表判为 degraded。

### 6. `xiaohongshu/ready.go`

基线位置：

- ready kind：第 15–23 行。
- `xhsReadyProbe`：第 32–54 行。
- `probeXHSReady`：约第 124–223 行。
- `isXHSReady`：约第 235–274 行。
- `isHomeURL`：约第 283–289 行。
- `inferXHSReadyKindFromURL`：约第 308–319 行。

改动：

- 新增 `XHSReadyNotification = "notification"`。
- probe 增加 `NotificationPageCount`、`NotificationTabCount`。
- JS probe 接收 notification page/tab 选择器并返回命中数。
- `isXHSReady` 新增 notification 判定：

  - `.notification-page` 存在；
  - 三个 tab 至少命中 3 个；
  - item 数不作为 ready 必要条件，允许空通知列表。

- `isHomeURL` 明确排除 `/notification`。
- URL 推断中在 home fallback 之前识别 `/notification`。
- `formatXHSReadyProbe` 自动携带新增字段。

### 7. `xiaohongshu/browse_session.go`

#### 数据结构区

基线约第 82–180 行，最小新增：

```go
type BrowseSessionNotificationSurface struct {
    Tab          NotificationTab
    Generation   uint64
    EnteredAt    time.Time
    ScrollCount  int
    ResultCount  int
    Items        []NotificationItem
    Cursor       string
    HasMore      bool
}

type browseNotificationState struct {
    active       bool
    tab          NotificationTab
    generation   uint64
    enteredAt    time.Time
    scrollCount  int
    items        []NotificationItem
    targets      map[string]notificationTarget
    cursor       string
    returnedIDs  map[string]bool
}
```

在 `BrowseSessionPageState` 增加可选 `Notification *BrowseSessionNotificationSurface`；在 `BrowseSession` 增加一个 `notification browseNotificationState` 字段。`Create` 初始化内部 maps。

不改变现有 feed `results/seenNotes/currentFeedID` 的数据结构。

#### 新增 session 方法

新增：

- `UnreadNotificationCount(ctx)`
- `ListNotifications(ctx, tab, cursor, maxItems)`
- `LikeNotification(ctx, notificationRef, unlike)`
- `ReplyNotification(ctx, notificationRef, content)`
- `resolveNotificationTargetLocked(ref)`
- `resetNotificationSurfaceLocked(reason)`
- `notificationSurfaceLocked()`

所有公开操作统一使用：

```go
opCtx, err := s.beginLockedOperation(ctx, true)
defer s.finishOperation()
page := s.page.Context(opCtx)
```

禁止使用 `page.Timeout`，避免污染 hrod 共享 actor context。

#### surface/generation/ref 规则

- 首次 `list_notifications`：

  - 保存进入前 URL 到现有 `sourceURL`。
  - 真实点击通知入口。
  - 清除 `opened/currentFeedID/currentXsecToken/read`。
  - generation 加一，清空 target/ref/cursor。
  - ref 格式使用 session 内部生成的不可推导 token，如 `nr_<random>`；ref 表只存内存。

- 同 tab 续页：

  - cursor 必须等于当前 surface 最新 cursor。
  - generation 不变，旧 ref 继续有效。
  - 成功后轮换 cursor，旧 cursor 失效。

- 切 tab、fresh list、`go_back`、`list_feeds` 首批、`search_feeds` 首批、`open_note`：

  - generation 加一并清空 targets/cursor。
  - 旧 `notification_ref` 立即报“引用已过期，请重新 list_notifications”。

- like 成功不换 generation，只更新当前 item 状态。
- reply 成功不自动移除 ref；后续写操作仍须重新通过三层校验。

#### PageState 语义

基线 `PageState()` 第 335–409 行和语义函数第 1545–1735 行：

- notification URL 必须报告 `kind=notification`。
- 页面在 notification surface 时：

  - 不展示 feed `open_note/like_feed/comment_feed` 动作。
  - `Current.NextHint` 指向 `list_notifications`、`like_notification`、`reply_notification` 或 `go_back`。
  - `Notification` 字段返回 tab、generation、items、cursor、has_more。
  - mentions 且有 actionable ref 时才生成 like/reply semantic actions。
  - likes/connections 只提供继续列出、切 tab、未读数、后退、关闭。

- `recommendedActionLocked` 在 notification 页面优先推荐：

  - 有 actionable item：相应通知动作；
  - 否则有更多：继续 `list_notifications`；
  - 否则 `go_back`。

#### 风控

写操作前：

- 从 `ActionStateStore.Load()` 检查 `RiskCooldownUntil`。
- 调用当前页面 `ClassifyRisk`，发现登录失效、验证码、操作频繁等立即中止。
- ref 必须来自当前 list，保证已有真实进入、停留和阅读行为。
- 点击前再 `SleepRandom(800ms, 1500ms)`。
- 成功调用 `RecordInteraction("", action)`。
- 错误继续交由 service 的 `recordRiskFromSession` 记录风险。

不修改 `action_state.go`，不把 feed 专用 `ValidateInteraction` 强套到通知。

#### 导航失效点

在以下基线函数首批导航分支中调用 `resetNotificationSurfaceLocked`：

- `ListFeedsBatch`：第 418 行起，cursor 为空分支。
- `searchBatch`：第 497 行起，cursor 为空分支。
- `OpenNote`：第 571 行起。
- `Back`：第 1094–1134 行。
- session close 无需额外持久化，随结构销毁。

### 8. `service.go`

在 session facade 区（基线第 1301–1615 行）新增：

- `SessionUnreadNotificationCount`
- `SessionListNotifications`
- `SessionLikeNotification`
- `SessionReplyNotification`

每个函数：

1. `browseSessions.Get(sessionID)`。
2. 调用对应 `BrowseSession` 方法。
3. 错误时执行 `recordRiskFromSession(session, err)`。
4. 原样返回 xiaohongshu 层的结构化结果。

cursor 与 ref 完全由 BrowseSession 持有和校验；不在 `XiaohongshuService` 增加第二套 cursor/ref map。

不修改 `acquirePageFor`，不增加任何全局通知方法或 HTTP endpoint。

### 9. `mcp_server.go`

在参数结构区（基线第 43–121 行）新增四个 Args：

```go
GetUnreadCountArgs {
    SessionID string
}

ListNotificationsArgs {
    SessionID string
    Tab       string
    MaxItems  int
    Cursor    string
}

LikeNotificationArgs {
    SessionID       string
    NotificationRef string
    Unlike          bool
    ConfirmToken    string
}

ReplyNotificationArgs {
    SessionID       string
    NotificationRef string
    Content         string
    ConfirmToken    string
}
```

在 `registerTools` 尾部、`close_page` 前注册 4 工具：

- `get_unread_count`
- `list_notifications`
- `like_notification`
- `reply_notification`

要求：

- 所有 handler 都使用 `withPanicRecovery`。
- `list_notifications` 描述明确：“查看通知页/tab 会使对应未读被小红书清除”。
- 工具编号重新连续整理。
- 末尾日志第 490 行从 18 改为 22。

### 10. `mcp_handlers.go`

#### available tools

基线第 15–43 行新增：

- `notificationReadTools`
- `afterNotificationTools`
- `afterNotificationMentionsTools`

调整：

- `afterCreateTools`、`afterFeedsTools`、`afterSearchTools`、`afterBackTools` 加入 `get_unread_count`、`list_notifications`。
- `afterOpenTools` 只加入 `get_unread_count`；不推荐在详情弹层直接进入通知。
- notification 页面提供：

  - `get_page_state`
  - `get_unread_count`
  - `list_notifications`
  - `go_back`
  - `close_page`

- mentions 有 actionable ref 时再加入 `like_notification`、`reply_notification`。

#### 新增 handlers

- `handleGetUnreadCount`
- `handleListNotifications`
- `handleLikeNotification`
- `handleReplyNotification`

写确认 key 必须绑定完整目标：

```text
like_notification:
session_id + notification_ref + unlike

reply_notification:
session_id + notification_ref + normalized content
```

禁止只按 comment ID 或昵称生成 token。

错误 next-step：

- ref/cursor 过期：`list_notifications`
- 页面已离开 notification：`list_notifications`
- notification 入口被详情弹层挡住：`go_back`
- DOM/state 漂移或写结果不确定：`get_page_state`，随后重新 `list_notifications`
- 风控：不推荐重试点击，直接返回风险冷却信息

`list_notifications` 响应显式包含：

```json
{
  "clears_unread": true
}
```

### 11. 文档

#### `docs/API.md`

基线第 11–52 行：

- “18 个工具”改为“22 个工具”。
- 在工具目录加入四个通知工具。
- 删除“P2 再注册”说明。
- 增加通知调用链：

```text
start_page
→ get_unread_count
→ list_notifications
→ like_notification / reply_notification
→ go_back
```

- 写操作说明加入通知点赞/回复。
- 明确 `get_unread_count` 不清未读；`list_notifications` 会清对应 tab 未读。
- 记录全部参数、tab 枚举、cursor/ref 生命周期和失败后禁止盲重试。

#### `README.md`

基线第 866–884 行：

- 工具列表加入四个通知工具及参数。
- 明确 session 前置要求和清未读副作用。

#### `README_EN.md`

基线第 767–785 行同步英文说明，不增加另一套命名。

### 12. 新增 `xiaohongshu/notification_test.go`

使用静态 state/DOM fixture 覆盖纯逻辑：

- tab 默认值、合法值、非法值。
- Vue `value/_value` unwrap。
- illegal status 过滤。
- `#like/#liked` 判定及 unknown fail-closed。
- DOM fingerprint 唯一匹配与歧义拒绝。
- generation 变化使旧 ref/cursor 失效。
- 同 generation 续页保留旧 ref。
- 非 mentions 不生成 actionable ref。
- 回复 placeholder 目标校验。

不在单元测试中模拟真实鼠标；真实行为由浏览器验收覆盖。

## 三、实施顺序

1. `ui_selectors.go`、`ready.go`、`selector_watchdog.go`
2. `notification.go` 只读模型、count、list、分页
3. `browse_session.go` surface/generation/ref 和只读入口
4. `notification_like.go`、`notification_reply.go`
5. `browse_session.go` 写操作接线、风险/timeline
6. `service.go`
7. `mcp_handlers.go`
8. `mcp_server.go`
9. `notification_test.go`
10. `docs/API.md`、`README.md`、`README_EN.md`
11. 静态、CI、真实浏览器验收

## 四、分步验收

### 选择器与 ready

- `/notification` 报 `kind=notification`，不再误判 home。
- 3 个 tab 命中且 page ready。
- 空通知列表仍 ready。
- watchdog 能报告 notification page/tab 失效。

### 未读数

- 调用前后 URL、active tab、notification generation 均不变。
- 调用前后 badge/state 数值一致。
- 没有 state 时返回错误，不返回伪造 0。
- CDP 记录中无通知入口 click。

### 三 tab 与分页

- 三个 tab 都通过真实点击切换，active 文本正确。
- 返回内容分别对应 mentions/likes/connections。
- 每页不超过 20。
- cursor 续页不重复；旧 cursor 拒绝。
- 切 tab 后旧 cursor/ref 拒绝。
- 响应和工具描述都披露清未读。

### 点赞幂等

针对同一 `notification_ref`：

1. 初始 `#like`，点赞后变 `#liked`。
2. 再次点赞返回 `Skipped=true`，计数/DOM 不再切换。
3. 取消后回到 `#like`。
4. 再次取消返回 `Skipped=true`。
5. `like-active` 不参与任何判断。
6. use 缺失或未知 href 时不点击。

### 回复三重校验

- ref/state 的 comment ID、user ID 匹配。
- DOM 行的 user ID、昵称、评论内容 fingerprint 匹配且唯一。
- 展开后的 placeholder 精确指向目标昵称。
- textarea 写入值验证通过后才点击发送。
- 发送按钮必须来自同一 item。
- 任一校验失败：不输入或不发送。
- 成功后编辑器收起，timeline 记录目标 ref/comment ID。

## 五、最终验收

- MCP 工具目录恰好 22 个。
- 四个通知工具全部要求 `session_id`。
- 仓库无通知路径调用 `acquirePageFor`、`MustNavigate("/notification")` 或新建页面。
- 无 raw `*rod.Page`、根目录 `humanize`、`time.Sleep`、通知代码中的 `page.Timeout`。
- `rg "like-active" xiaohongshu/notification*.go` 零命中。
- `go test ./...` 通过；CI build 通过。
- `git diff --check` 通过。
- 改动只限上述白名单文件。
- `pkg/humanize/rod`、现有点赞/收藏/评论实现、HTTP API、BrowseSession feed 数据结构不改。

## 六、主要风险与止损

- **DOM 漂移**：所有写操作同时依赖 state、DOM fingerprint、目标控件；任何未知状态均 fail-closed。
- **清未读不可逆**：只有 `list_notifications` 点击入口/tab；`get_unread_count` 绝不导航。工具描述、返回值双重披露。
- **虚拟列表/同名同文**：禁止按数组 index 写操作；匹配不唯一时不给 actionable ref。
- **deadline**：ready 最长 15 秒、点赞确认 15 秒、回复确认 8 秒、滚动最多 6 轮；所有等待响应调用 ctx。
- **hrod context 污染**：通知路径只用 `page.Context(opCtx)`，禁止 `page.Timeout`。
- **动作结果不确定**：写操作最多点击一次；确认失败提示重新 list，禁止自动补点。
- **回滚**：P2 应按上述顺序分提交；需要回滚时先撤 MCP 注册/handler，再撤 service/session 接线，最后删除通知文件和 ready/selector 扩展，不影响 P1 的 18 工具体系。


