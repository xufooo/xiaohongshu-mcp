## 一、过度设计清单

### 1. [notification.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/notification.go)

可直接删除：

- `scrollNotificationPage`：没有任何调用，是明确死代码；同时删除该文件的 `math/rand` import。
- `notificationDOMItem.Index`、`Time`：没有消费者。
- `readNotificationDOMSnapshot` 中的 `hintSel/timeSel`、`hint/timeEl`：采集后没有参与返回、指纹或写操作验证。
- `notificationStateResult.Tab`：只赋值，从未读取。

可简化：

- `readNotificationTabState` 可直接返回 `*notificationPayload`：
  - notification state 缺失直接报错。
  - tab 分区缺失直接报错。
  - 保留 `value/_value` unwrap。
  - 删除 `notificationStateResult`、调用方重复的 `HasState` 判断。
- `notificationTarget` 同时保存完整 `Item` 和 `Liked/CommentID/UserID/Nickname/CommentText`，属于重复状态。缩为：
  - `Ref`
  - `Tab`
  - `Generation`
  - `Item`
  
  所有校验从 `target.Item` 读取。

不建议简化：

- `readNotificationDOMSnapshot` 本身不能删除。它是 state 条目与真实 DOM 行安全对齐的基础。
- fingerprint 唯一匹配、SVG href 检测、严格 user ID 复核必须保留。
- `decodeNotificationCount` 是有效测试边界，不值得为了十几行内联。
- 不要抽取全仓库通用 JSON/Eval 框架；收益小、影响面大。

### 2. [notification_reply.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/notification_reply.go)

这里是最明显的过度设计点。

当前提交确认执行了多套重叠检测：

- 提交前扫描回复文本计数。
- 提交后每轮扫描全通知区域的叶子文本。
- 每轮额外 Eval 检查全页面 textarea 是否消失。
- 独立扫描全页面错误关键词。
- 发送按钮又自行实现一套 visible/disabled 判定，而 hrod `Click` 已执行 interactable 检查。

唯一精简方案：

- 保留三层目标校验：
  - 当前 ref/generation/tab。
  - state 的 comment ID/user ID。
  - DOM fingerprint 与 placeholder。
- 保留输入后 textarea value 核对。
- 保留目标行内查找提交按钮和文本必须等于“发送”。
- 删除手写按钮 `visible/disabled` Eval，交给 hrod `Click` 的 interactable 检查。
- 删除：
  - `countNotificationReplyText`
  - `notificationReplyMatchCount`
  - `notificationReplyInputGone`
  - 当前 `notificationReplySubmissionState`
- 新增一个约 30–40 行的 `waitNotificationReplyAccepted(ctx, page, row)`：
  - 只检查目标行内 `textarea.comment-input` 是否消失或隐藏。
  - 同一次 Eval 顺带检查明确的风控/发送失败提示。
  - 最长 8 秒，使用 `SleepRandom`，不重试发送。

这基本采用上游 `waitReplyAccepted` 的简洁模型，但保留 fixup-test 的行内绑定、错误提示和 ctx/hrod 语义。

### 3. [notification_like.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/notification_like.go)

整体不过度设计，接近上游体量。

可做一个小优化：

- 让 `findNotificationRowElement` 同时返回已匹配的 `notificationDOMItem`。
- 点赞前的初始状态直接解析该快照的 `LikeUseHref`，避免紧接着再次读取完整 DOM snapshot。
- 点击后的轮询继续重新读取 DOM snapshot，以兼容前端重新渲染，不改成持有旧 row 元素盲读。

必须保留：

- `#like/#liked` fail-closed。
- 幂等判断。
- 点击一次、不自动二次点击。
- 点击后状态确认。
- 写前 state 复核和 DOM 唯一匹配。

### 4. [browse_session.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/browse_session.go)

事务式分页不是过度设计。它解决了真实问题：

- cursor 重试不能丢条目。
- 跨轮加载不能把未返回条目提前标记 returned。
- generation 内 ref 必须稳定。
- tab/fresh 导航后旧 ref 必须失效。

不要改成上游的“加载到 limit 后直接截数组”，也不要使用裸 offset cursor；通知列表头部可能动态插入，offset 会重复或漏项。

可精简点：

1. 合并重复响应组装：

   - 将 `finishNotificationListLocked` 改成通用的 `commitNotificationListLocked`。
   - fresh 和 continuation 都调用它。
   - 删除 `refreshNotificationListLocked` 中重复的 commit、cursor、timeline、结果构造代码。

2. surface 只保存最近一次成功返回的 batch：

   - `notification.items = fresh`，不要无限累计全部历史分页。
   - `targets` 仍保留当前 generation 已签发的全部 ref，旧 ref 继续有效。
   - `returnedIDs` 继续保留完整去重状态。
   - PageState 和 semantic actions 只暴露当前仍最可能位于 DOM 中的批次，避免状态与语义动作无限膨胀。
   - 删除 `mergeNotificationItems` 及对应测试。

3. 删除重复拟人停留：

   - `LikeNotification` 和 `ReplyNotification` 在 session 层各有一次 `SleepRandom(800ms–1500ms)`。
   - 实际操作函数在目标按钮点击前已经执行同样的停留。
   - 删除 session 层这两次等待，保留距离真实点击最近的等待。风控检查不变。

4. 精简写操作重复段：

   - 可增加一个窄 helper，只负责“在 operation 已取得后解析当前 target、验证 notification surface、检查冷却并执行 `ClassifyRisk`”。
   - `beginLockedOperation/defer finishOperation` 仍留在两个公开方法里，避免 helper 隐式持有或释放 session operation。
   - 不抽象成通用 action pipeline。

### 5. 其他明确冗余

- [ui_selectors.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/ui_selectors.go)
  - 删除未使用的 `SelectorNotificationBadge`、`SelectorNotificationHint`、`SelectorNotificationTime`。
  - 删除从未被任何页面 kind 探测的 `NotificationEntrySpec`；保留入口 selector 本身。
- [selector_watchdog.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/selector_watchdog.go)
  - 删除 `NotificationEntrySpec` 注册。
- [ready.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/ready.go)
  - `XHSReadyNotification` 的 URL fallback 重复要求 page 和 3 个 tab，和首个判断完全等价；直接返回 `pageCount > 0 && tabCount >= 3`。
- [mcp_handlers.go](/home/dietpi/work/xiaohongshu-mcp/mcp_handlers.go)
  - 删除未引用的 `notificationReadTools`。
- `NotificationList.ResultCount`、`NotificationCount.HasState` 虽然冗余，但删除会改变刚发布的响应 schema，节省极少；本轮不动。
- service facade、四个 handler、ready/watchdog 集成属于现有分层要求，不合并。

## 二、必须保留

- 所有通知操作必须依附 `BrowseSession`，不引入 `acquirePageFor`。
- 通过真实侧栏入口进入，不直接导航 `/notification`。
- tab 真实点击、阅读停留和真实滚动。
- cursor、generation、returned IDs、notification ref 生命周期。
- 事务式“成功响应时才提交分页选择”。
- DOM fingerprint 唯一匹配；歧义时只读、不签发写引用。
- 写前 state comment/user 复核。
- 点赞 SVG href 幂等检测。
- 回复行内输入框、placeholder、输入值、行内发送按钮校验。
- confirm token、ActionStateStore 冷却和页面风险识别。
- 未确认写结果时不自动重试。

## 三、唯一实施顺序

1. 删除死代码、未使用字段/selector/spec，以及冗余 ready fallback。
2. 精简 `notificationStateResult` 和 `notificationTarget`，同步调用方与测试。
3. 合并 fresh/continuation 的统一提交与响应组装；保持现有事务式算法不变。
4. 将 notification surface 改为仅保存最近 batch，targets/returnedIDs 仍覆盖整个 generation。
5. 精简回复提交确认，删除文本叶子节点扫描和重复 Eval。
6. 优化点赞首次 DOM snapshot 重复读取。
7. 删除 session 层重复点击前等待。
8. 执行 `gofmt`、`go test ./...`、`git diff --check`，再做真实浏览器回归。

真实回归必须覆盖：

- 未读数读取不清未读。
- 三个 tab 首批和至少两次 cursor 续页，无重复、无丢失。
- 中途取消后原 cursor 可重试。
- 旧分页 ref 在同 generation 内仍可解析。
- 新 generation/tab 后旧 ref 失效。
- 点赞幂等与 `#like/#liked`。
- 回复目标三重校验、发送成功和发送未确认路径。
- PageState 不随分页无限增长。

## 四、预期规模

| 文件 | 当前 | 精简后目标 |
|---|---:|---:|
| `notification.go` | 594 | 515–535 |
| `notification_like.go` | 127 | 110–120 |
| `notification_reply.go` | 279 | 165–185 |
| `browse_session.go` | 2499 | 2410–2440 |
| 其他通知基础设施 | — | 再减少约 20–30 |

P2 总增量预计由约 2228 行降至约 **1850–1950 行**。不建议继续追求接近上游的 629 行；剩余差异主要是 session 生命周期、分页/ref、安全校验、拟人操作、MCP 接入、文档和测试，而不是无效复杂度。
tokens used
1,719,163
结论：P2 确有可精简部分，但不能按上游 629 行作为目标。上游依赖直接导航、数组下标定位和较弱的写前复核；fixup-test 的 session/ref/cursor、拟人点击和 fail-closed 是真实架构成本。建议只做约 300 行的低风险瘦身，不重写已通过验收的分页状态机。

## 一、过度设计清单

### 1. [notification.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/notification.go)

可直接删除：

- `scrollNotificationPage`：没有任何调用，是明确死代码；同时删除该文件的 `math/rand` import。
- `notificationDOMItem.Index`、`Time`：没有消费者。
- `readNotificationDOMSnapshot` 中的 `hintSel/timeSel`、`hint/timeEl`：采集后没有参与返回、指纹或写操作验证。
- `notificationStateResult.Tab`：只赋值，从未读取。

可简化：

- `readNotificationTabState` 可直接返回 `*notificationPayload`：
  - notification state 缺失直接报错。
  - tab 分区缺失直接报错。
  - 保留 `value/_value` unwrap。
  - 删除 `notificationStateResult`、调用方重复的 `HasState` 判断。
- `notificationTarget` 同时保存完整 `Item` 和 `Liked/CommentID/UserID/Nickname/CommentText`，属于重复状态。缩为：
  - `Ref`
  - `Tab`
  - `Generation`
  - `Item`
  
  所有校验从 `target.Item` 读取。

不建议简化：

- `readNotificationDOMSnapshot` 本身不能删除。它是 state 条目与真实 DOM 行安全对齐的基础。
- fingerprint 唯一匹配、SVG href 检测、严格 user ID 复核必须保留。
- `decodeNotificationCount` 是有效测试边界，不值得为了十几行内联。
- 不要抽取全仓库通用 JSON/Eval 框架；收益小、影响面大。

### 2. [notification_reply.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/notification_reply.go)
