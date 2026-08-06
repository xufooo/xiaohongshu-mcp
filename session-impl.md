# SESSION OPTIMIZATION — UNIQUE IMPLEMENTATION PLAN

基线：`fixup-test @ d70ed2df4547e1f6aca49f5d4ab9cbb99006f845`

只按下述方案实施，不修改工具面，不新增兼容分支，不引入 JS 滚动 fallback。

## 1. 修改白名单

生产代码：

- `pkg/humanize/rod/hrod.go`
- `xiaohongshu/action_state.go`
- `xiaohongshu/browse_session.go`
- `xiaohongshu/comment_feed.go`
- `xiaohongshu/dom_extract.go`
- `xiaohongshu/feed_detail.go`
- `xiaohongshu/like_favorite.go`
- `xiaohongshu/network_capture.go`
- `xiaohongshu/note_open.go`
- `xiaohongshu/notification_like.go`
- `xiaohongshu/read_stage.go`
- `xiaohongshu/ready.go`
- `xiaohongshu/selector_watchdog.go`
- `xiaohongshu/ui_selectors.go`

测试：

- 新增 `pkg/humanize/rod/hrod_test.go`
- 新增 `xiaohongshu/session_optimization_test.go`
- 修改 `xiaohongshu/notification_test.go`

禁止修改：

- `mcp_server.go`
- `mcp_handlers.go`
- `service.go`
- `docs/`、`README*`
- `xiaohongshu/notification.go`
- `xiaohongshu/notification_reply.go`
- `pkg/humanize/` 下除 `rod/hrod.go` 外的核心实现
- 工具名、参数、注册数、confirm token、notification cursor/ref/generation 语义

## 2. P0：正确性与真实拟人操作

### 2.1 隔离 hrod clone context

`pkg/humanize/rod/hrod.go:112-170, 355-437, 374-480, 765-953`

精确改动：

1. `Page.Context`、`Page.Timeout`：

   - clone 仅保存自己的 `ctx`。
   - 删除 clone 创建时对共享 actor 的 `SetContext`。
   - 继续共享原 Mouse、Keyboard、actor，保持鼠标位置和连续输入状态。

2. `Element` 增加私有 `ctx context.Context`：

   - `Page.wrapElement`使用所属 page 的 ctx。
   - 子元素、Parent/Next/Previous 继承父元素 ctx。
   - `Element.Context/Timeout`只更新返回 clone 的 ctx。
   - `NewElement`使用传入 actor 当前 ctx，保持现有公开签名。

3. 新增私有绑定函数：

   - `Page.bindActorContext()`
   - `Element.bindActorContext()`

4. 在真实 actor 操作开始前绑定当前对象 ctx：

   - `Page.Actor/ClickPoint/MovePoint/MustScroll`
   - `Element.Actor/Sleep/Click/ClickNoScroll/Input/Hover/ScrollIntoView`
   - `waitInteractable`进入时先绑定一次

5. 不创建新 actor，不修改 `pkg/humanize.Actor/Mouse/Keyboard`。

验收：

- `base.Context(longCtx)`创建后，再调用`base.Timeout(2s)`，2 秒后 long page/element 的 Sleep、Click 使用 longCtx，不返回 deadline exceeded。
- short clone 被取消不影响 sibling/base clone。
- 同一 actor、Mouse、Keyboard 指针保持一致。
- `go test -race ./pkg/humanize/rod/...`通过。

### 2.2 `checkPageAccessible` 改为单 Eval

`xiaohongshu/feed_detail.go:1214-1262`

精确改动：

- 删除开头固定 `page.Sleep(500ms)`。
- 删除 `page.Timeout(2s).Element`和后续 `Text()`。
- 一次 `page.Eval`完成：

  - 查询 `.access-wrapper, .error-wrapper, .not-found-wrapper, .blocked-wrapper`
  - 返回 JSON `{found, text}`
  - `found=false`返回 nil
  - `found=true`且命中既有关键词，返回对应错误
  - `found=true`且文本非空但无已知关键词，继续 fail-closed 返回未知错误
  - Eval 失败直接返回错误，不把失败解释为“页面可访问”

- 不创建内部 timeout，由调用方 page/ctx 控制。

验收：

- 函数内无 `Sleep`、`.Timeout`、`Element`。
- 无错误容器返回 nil。
- 已知和未知错误文本均阻断。
- PostComment/Reply/FeedDetail 后续 Click 不再继承 2 秒 actor ctx。

### 2.3 评论区全部改为物理滚轮

`xiaohongshu/feed_detail.go:696-746, 953-984`  
调用点：

- `feed_detail.go:250-369, 372-660`
- `read_stage.go:79, 231-244`
- `comment_feed.go:301-424`

精确改动：

1. 定义唯一容器优先级：

   - `.note-scroller`
   - `.comments-container`

2. 新增内部 helper：

   - `findCommentScroller(page) (*hrod.Element, error)`
   - `readCommentScrollerPosition(el) (scrollTop, scrollHeight, clientHeight, error)`
   - `humanScrollCommentScroller(page, delta) (moved bool, err error)`

3. `humanScrollCommentScroller`固定流程：

   - 读取 before。
   - `el.Hover()`将真实鼠标移入滚动容器。
   - 按 100～140px 分格调用 `page.Actor().Mouse.Scroll`。
   - 格间 `SleepRandom(20ms, 65ms)`。
   - 滚完 `SleepRandom(120ms, 220ms)`。
   - 对同一元素读取 after。
   - DOM/元素/位置无法确认返回 error。
   - `after==before`返回 `moved=false,nil`，供“已到底”逻辑判断。
   - 禁止调用 DOM `scrollBy/scrollTo`作为 fallback。

4. `scrollNoteScrollerMoved`改为调用该 helper；`scrollNoteScroller`保留薄 wrapper。

5. `scrollToCommentsArea`：

   - 找到评论区元素。
   - 使用 hrod `ScrollIntoView`。
   - 拟人停顿后执行一次小幅物理滚轮激活懒加载。
   - 任一步无法确认均返回 error。

验收：

- 上述函数中不存在 `scrollBy(`、`scrollTo(`、`window.scroll`。
- CDP 实测鼠标位置在评论容器内，wheel 事件推动同一容器。
- 正常滚动 `after>before`；到底返回 `moved=false`。
- 找不到容器不回退机械 JS 滚动。

### 2.4 互动状态唯一来源及打开笔记单快照

`xiaohongshu/dom_extract.go:139-431`  
`xiaohongshu/browse_session.go:609-676`

精确改动：

1. 在 `dom_extract.go`增加唯一 JS 状态片段：

   - 同时要求 like wrapper、collect wrapper 存在。
   - like `use`读取 `xlink:href`，兼容 `href`。
   - `#liked→true`、`#like→false`。
   - `#collected→true`、`#collect→false`。
   - use 缺失、未知 href、任一 wrapper 缺失均返回 unknown。

2. `ExtractFeedDetailFromDOM`：

   - 删除 `isActive` class/aria/style 判断。
   - 使用唯一 href 状态片段。
   - unknown 返回 `ErrNoFeedDetail`，不得输出零值 false/false。

3. `ExtractOpenedNoteContentFromDOM`同样删除 class 判定，使用 href 状态。

4. `ExtractInteractStateFromDOM`复用同一状态片段，不保留第三套实现。

5. 新增：

   - `OpenedNoteSnapshot`
   - `ExtractOpenedNoteSnapshotFromDOM(page, feedID)`

   单次 Eval 返回：

   - `OpenedNoteContent`
   - 图片
   - href 互动状态
   - 当前首屏 comments

6. `ExtractOpenedNoteContentFromDOM`保留为兼容薄 wrapper，内部调用 snapshot 后只返回 Note。

7. `BrowseSession.OpenNote`：

   - 将 `ExtractOpenedNoteContentFromDOM + ExtractCommentsFromDOM`两次调用替换为一次 snapshot。
   - `initialCommentIDs`继续基于 snapshot comments 建立，规则不变。
   - 响应字段、media 文案、timeline、seenNotes 不变。

验收：

- `dom_extract.go`互动状态区域无 `isActive`、`like-active`、class/style 状态依赖。
- 四种合法 href 组合解析正确。
- like/collect 任一未知均 fail-closed。
- `OpenNote`只执行一次详情 DOM snapshot Eval。
- 首屏评论 cursor 与改前一致。

### 2.5 累计评论阅读与回复目标唯一匹配

`xiaohongshu/action_state.go:111-227`  
`xiaohongshu/browse_session.go:720-897`  
`xiaohongshu/comment_feed.go:135-252, 254-421`

精确改动：

1. `ActionStateStore`新增：

   - `ValidateInteractionTarget(feedID string)`：只校验冷却、已打开 feed、feed 匹配、30 分钟有效期。
   - 私有 `validateInteractionState`供 Target 和完整 Validate 共用，避免重复规则。
   - 完整 `ValidateInteraction`继续保留现有阅读/滚动阈值，不降低 reply 60 秒门槛。

2. `RecordCommentDwell`：

   - 累计真实 duration。
   - scrolled 时累计 CommentScrollCount。
   - 有有效停留或滚动时同步更新 `LastReadAt`，使评论区阅读可解除“连续互动前需重新阅读”。

3. `BrowseSession.detailCommentsBatchLifecycle`：

   - 在 loader 前记录开始时间。
   - loader 成功后记录本次真实停留时间。
   - 通过 `nextCursor.Round > inputCursor.Round`判断是否发生确认过的物理滚动。
   - 调用一次 `RecordCommentDwell`。
   - error 路径不记录未完成停留。

4. `BrowseSession.detail`的 `loadComments=true`路径同样记录实际加载时长和是否发生滚动；不改变默认 `get_note_detail`无分页路径。

5. `ReplyToComment` state 分支：

   - 删除固定 `ReadMin(45s)`。
   - 删除固定 `DwellInComments(60s)`。
   - 开始时调用 `ValidateInteractionTarget`，确认页面和基本风控状态。
   - 查找评论前记录 `searchStart`。
   - 目标查找和最终 `ScrollIntoView`完成后，将实际 elapsed/scrolled 记录到 ActionState。
   - 在获取/点击回复按钮前再次调用完整 `ValidateInteraction(feedID,"reply")`。
   - 未累计满 60 秒时快速失败，且不得点击回复按钮。
   - 错误文案明确要求先通过 `get_note_detail(max_items/cursor)`阅读并滚动评论区。

6. `findCommentElement`签名改为：

   - 接收 ctx。
   - 返回 `(*hrod.Element, scrolled bool, error)`。

7. 每轮使用一次 Eval 返回：

   - 当前 `.comment-item`数量
   - atEnd
   - commentID 匹配索引
   - userID 匹配索引数组

8. 匹配规则：

   - commentID 优先。
   - userID 只能唯一匹配。
   - userID 多匹配直接报歧义，禁止选择第一条。
   - 找到唯一 index 后只调用一次 `page.Elements(".comment-item")`。
   - 未找到时才执行物理滚动。
   - ctx 取消、到底或连续停滞停止。

验收：

- 回复调用中无固定 45s+60s等待。
- 累计 59 秒仍失败；累计达到 60 秒且至少一次确认滚动才允许点击。
- 快速找到目标时，本次真实查找时间也计入累计值。
- 同一 user 多条评论 fail-closed。
- commentID 唯一定位优先于 userID。
- 完整校验发生在回复按钮 Click 之前。

## 3. P1：热路径与存储优化

### 3.1 ready 按 kind 缩小探测范围

`xiaohongshu/ready.go:62-238`  
调用点：

- `browse_session.go:369-396`
- `browse_session.go:1098-1130`

精确改动：

1. `probeXHSReady`签名增加 `kind XHSReadyKind`。
2. scoped probe只计算：

   - 公共：URL、title、readyState、scrollY、app、risk
   - home：home feeds、feed cards、detail absence
   - home_search：home信号及可交互搜索框
   - search：search feeds/results
   - detail：详情容器、feed匹配、like按钮
   - comment_box：detail + comment box
   - profile：profile state
   - publish：publish signal
   - notification：notification page + 3 tabs

3. `WaitForXHSReady`轮询传 `opts.Kind`。
4. 到达 timeout 后额外执行一次 full probe：

   - 用于 URL fallback。
   - 用于最终诊断。
   - 不在每轮执行 full probe。

5. `PageState`和`waitForHistoryTargetReady`继续使用 full probe，因为需要推断页面种类。

验收：

- 所有 kind 的 ready 逻辑与改前一致。
- notification 仍要求 page + 至少 3 tabs。
- timeout 最终错误仍包含完整 probe。
- scoped probe 不查询无关页面选择器。
- 所有旧两参数 `probeXHSReady`调用零残留。

### 3.2 卡片批量定位

`xiaohongshu/note_open.go:27-87`

精确改动：

- `findFeedCardAnchor`由逐 anchor Eval 改为：

  1. 一次 page Eval扫描 `section.note-item a.cover.mask.ld`。
  2. 按 href、data-feed-id、outerHTML 匹配 feedID。
  3. 只接受有尺寸、已连接的候选。
  4. 返回唯一 index及匹配数。
  5. 零匹配报未找到；多匹配报歧义。
  6. Go 侧只调用一次 `Elements`并按 index取 anchor。

- 保留现有 ScrollIntoView、拟人停顿、点击点验证和 `waitFeedDetailVisible`，本批不重写已验证点击路径。

验收：

- 无逐 anchor Eval 循环。
- 35～50 张卡片仍只执行一次匹配 Eval。
- 多重匹配 fail-closed。
- 实测仍从列表卡片真实点击进入详情，不直接导航。

### 3.3 read stage 简化及单次落盘

`xiaohongshu/read_stage.go:13-252`  
`xiaohongshu/action_state.go:111-131`

精确改动：

1. `contentMetrics`只保留实际参与算法的 Images。
2. 初始轮播 probe同时返回 Images、ActiveIndex。
3. 删除标题、正文、评论数、video等未使用 DOM 查询。
4. `advanceCarouselRight`：

   - 点击后用一次可等待 Promise/MutationObserver Eval等待 active index 变化。
   - 最长 2 秒。
   - 不再每 100～150ms重复完整 carousel probe。

5. Read结束剩余停留改为一次可取消 `page.Sleep(remaining)`，删除 500ms循环。

6. `ActionStateStore`新增：

   - `RecordReadStage(feedID, duration, scrollCount)`

   单次 update同时更新 ReadDuration、FeedScrollCount、LastReadAt。

7. `Read`：

   - 删除中途 `RecordFeedScroll`。
   - 成功完成后仅调用一次 `RecordReadStage`。
   - 失败/取消不记录完整阅读成功。

8. `DwellInComments`删除内部调用；若仓库内确认零引用，可作为本批可选死代码删除。保留`RecordCommentDwell`薄 wrapper供 FeedDetail 使用。

验收：

- 每次 Read 只发生一次 ActionState 文件读写。
- 20 秒最低阅读语义不变。
- 最多浏览两张后续图片不变。
- 每次轮播切换最多一次等待 Eval。
- 取消 ctx 可立即中断剩余停留。

### 3.4 session 状态刷新合并 Eval

`xiaohongshu/browse_session.go:1979-2043`

精确改动：

- `refreshPageState`使用一次 Eval返回 JSON `{url, scrollY}`。
- 单次解析后持锁提交；任何字段缺失只跳过该字段。
- `currentPageURL`先在锁内读取非空 `currentURL`，仅缓存为空时执行现场 Eval。
- 不改变 `finishOperation`、TTL、operation token、active cancellation。

验收：

- 每次成功 operation 的 refresh Eval 从 2 次降为 1 次。
- 页面缓存为空仍能读取真实 URL。
- OpenNote/Back 的 sourceURL 语义不变。
- close 与 operation 竞争测试不回归。

## 4. P2：机械去重与资源限制

### 4.1 评论分页重复分支

`xiaohongshu/feed_detail.go:372-660`

精确改动：

- 在 `LoadCommentsBatch`内部增加唯一局部 helper `partialOrError(err)`：

  - 本次 cursor 确有增长且 ctx 正常：返回当前 batch、cursor、`hasMore=true`、nil。
  - 否则返回原错误。
  - ctx canceled/deadline exceeded不得转换为成功。

- 替换重复的 cursor 增长判断分支。
- 不改变 ReturnedIDs、Round、ExpandRound、partial success和事务边界。

验收：

- 所有错误类型的改前/改后返回语义一致。
- error 路径不会消费调用方保存的 cursor。
- partial success只包含实际返回条目。
- cursor IDs无重复。

### 4.2 展开回复 helper 合并并使用 hrod 点击

`xiaohongshu/feed_detail.go:797-955`

精确改动：

- 合并 `nextShowMoreButton`和`nextVisibleShowMoreButton`。
- 单次 Eval返回候选 button index、parent index、文本、数量。
- Go 侧用一次 `Elements`取得按钮。
- 需要移入视口时调用 hrod `ScrollIntoView`。
- 点击统一调用 element `Click`，不再通过坐标辅助函数。
- 无其他引用后删除 `dispatchMouseClick`。

验收：

- 展开按钮只在允许阈值内点击。
- “收起”按钮不得匹配。
- 可见模式不主动滚动。
- 普通模式使用 hrod 真实滚动和点击。
- 展开后仍按目标 parent 校验子评论增长。

### 4.3 点赞/收藏公共流程

`xiaohongshu/like_favorite.go:104-283`

精确改动：

- 新增内部 `stateOf`和统一 `performInteraction/toggleInteractionOnce`。
- Like/Favorite公共方法只负责提供：

  - action type
  - selector
  - target bool
  - 状态字段选择函数

- 必须保持：

  - wrapper选择器不变
  - 阅读门槛不变
  - SVG href状态不变
  - 幂等 skip
  - 最多一次点击
  - 点击后 2～5 秒停留
  - unknown 不二次点击
  - RecordInteraction行为不变

验收：

- like/unlike/favorite/unfavorite四条路径一致。
- 全仓无自动二次点击循环。
- href unknown继续返回 `state_unknown`。

### 4.4 watchdog 去掉重复字段逻辑

`xiaohongshu/ui_selectors.go:38-116`  
`xiaohongshu/selector_watchdog.go:28-229`

精确改动：

- `SelectorHealthEntry`保存 `VisibleOnly`。
- Register直接从 SelectorSpec复制。
- 删除 `selectorUsesVisibleCount`名称 switch。
- 删除未被使用且不执行约束的 `MaxMatches`字段及所有初始化。
- 定义 24 小时 probe TTL：

  - Unknown立即探测。
  - Degraded required允许下次导航重探。
  - Healthy按 TTL。
  - Suspicious optional同样按 TTL，不得每次操作重探。

验收：

- notification列表为空时 notification_item只标记 suspicious，不在每个操作重复 Eval。
- required选择器退化仍持续可检测。
- health API字段除删除无效 MaxMatches外语义不变。

### 4.5 notification like 匹配去重

`xiaohongshu/notification_like.go:76-130`  
`xiaohongshu/notification_test.go`

精确改动：

- 抽取唯一 `matchNotificationDOMItem(snapshot,target)`。
- `readNotificationLikeState`和`findNotificationRowElement`共用。
- 保留指纹唯一性、歧义拒绝、完整 DOM snapshot和 href解析。
- 不修改 notification ref、generation、cursor、returnedIDs。

验收：

- 未找到、唯一匹配、双重匹配测试全部覆盖。
- 点赞后重新渲染仍能重新定位目标行。
- notification cursor/ref测试不变。

### 4.6 网络捕获限并发

`xiaohongshu/network_capture.go:14-162`

精确改动：

- 增加 body fetch并发上限 3。
- `NetworkCapture`持有 buffered semaphore。
- `onLoadingFinished`：

  - 获取槽位成功才启动 goroutine。
  - 槽位已满则跳过 body summary，只保留 response metadata。
  - goroutine退出时释放槽位并完成 WaitGroup。

- 保持 20 条、128 KiB、1.5 秒 body timeout及脱敏逻辑。

验收：

- 并发 body fetch不超过 3。
- 满载时不阻塞 CDP事件回调。
- Stop不死锁，Snapshot仍包含基础响应信息。

### 4.7 可选死代码清理

仅在实施时再次 `rg`确认零引用后允许删除：

- `browse_session.go:937-945 shouldStopSessionCommentPaging`
- `feed_detail.go:1030-1063 getScrollTop`
- `feed_detail.go:1095-1149 getTotalCommentCount`
- `feed_detail.go:1151-1168 checkNoCommentsArea`
- 相应无用 import

禁止删除 exported constructor、ActionState公开方法和公开响应字段。

## 5. 实施顺序

1. P0.1：hrod context隔离及测试。
2. P0.2：checkPageAccessible单 Eval。
3. P0.3：物理评论区滚轮。
4. P0.4：统一互动 href + OpenNote单快照。
5. P0.5：ActionState累计评论阅读 + 回复目标唯一匹配。
6. P1.1：kind-specific ready。
7. P1.2：卡片批量定位。
8. P1.3：read stage + ActionState单次落盘。
9. P1.4：session refresh单 Eval。
10. P2.1～P2.7：纯去重、watchdog和资源限制。
11. 统一 gofmt、静态扫描、测试和真实浏览器验收。

不得把多个 P0 项压成一个不可独立回滚的提交。

## 6. 最终验收

静态：

- MCP正式工具注册仍恰好 22 个。
- 工具名、参数、confirm token零变化。
- `mcp_server.go/mcp_handlers.go/service.go`无 diff。
- notification cursor/ref/generation相关代码无语义 diff。
- `pkg/humanize`核心无 diff。
- `checkPageAccessible`无 Sleep/Timeout/Element。
- 评论滚动 helper无 `scrollBy/scrollTo` fallback。
- 互动状态无 class/like-active依赖。
- 无自动二次点击。
- 无新增 TODO、兼容 alias、页面级 selector fallback。
- `git diff --check`通过。
- `gofmt`通过。
- `go test ./...`通过。
- `go test -race ./pkg/humanize/rod/... ./xiaohongshu/...`通过。
- CI build通过。

真实浏览器：

- 短 timeout探测后，评论/点赞/收藏 Click仍使用外层调用 ctx。
- 首页/搜索→卡片真实点击→详情成功。
- OpenNote只做一次内容快照，正文、互动、图片、首屏评论完整。
- like `#like↔#liked`、collect `#collect↔#collected`幂等正确。
- 评论区滚动由真实 wheel推动，滚动位置可确认。
- `get_note_detail`多轮累计 CommentDwellTime/CommentScrollCount。
- reply不足 60 秒不点击；达标后唯一目标回复成功；userID歧义拒绝。
- 评论分页 cursor连续、无重复、异常不吞条目。
- 通知3 tab、点赞、回复、cursor/ref生命周期全部回归通过。
- RPi环境下无 body capture goroutine突增，ready轮询和打开卡片的 CDP往返明显下降。


tokens used
2,112,634
