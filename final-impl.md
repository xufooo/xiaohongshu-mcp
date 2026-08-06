# 最终优化唯一实施单

基线：`fixup-test @ a45d0a4`。以下行号均以该基线为准。四阶段各自形成独立提交，必须在本阶段验收通过后再进入下一阶段。

## 1. 修改白名单

P0：

- `.github/workflows/build.yml`
- `main.go`
- `browser/browser.go`
- `browser/browser_test.go`
- `cookies/cookies.go`
- `cookies/cookies_test.go`（新增）
- `pkg/downloader/images.go`
- `pkg/downloader/images_test.go`（新增）
- `pkg/downloader/processor.go`
- `third_party/headless_browser/headless_browser.go`
- `third_party/headless_browser/headless_browser_test.go`
- `xiaohongshu/ready.go`
- `xiaohongshu/ready_test.go`（新增）
- `xiaohongshu/browse_session.go`
- `xiaohongshu/session_optimization_test.go`
- `xiaohongshu/user_profile.go`
- `xiaohongshu/publish.go`
- `xiaohongshu/publish_video.go`
- `xiaohongshu/like_favorite.go`
- `xiaohongshu/comment_feed.go`
- `xiaohongshu/redact_test.go`（新增）

P1：

- `mcp_server.go`
- `mcp_handlers.go`
- `service.go`
- `xiaohongshu/feed_detail.go`
- `xiaohongshu/search.go`
- `xiaohongshu/browse_session.go`
- `xiaohongshu/comment_feed.go`

P2：

- `xiaohongshu/user_profile.go`
- `xiaohongshu/user_profile_test.go`（新增）
- `xiaohongshu/testdata/user_profile_state_note.json`（新增、真实状态脱敏 fixture）

P3：

- `xiaohongshu/dom_extract.go`
- `xiaohongshu/ready.go`
- `browser/browser_manager.go`
- `third_party/headless_browser/headless_browser.go`

禁止修改：

- `humanize/**`
- `go.mod`、`go.sum`
- MCP 工具名、参数结构、注册数量
- notification cursor/ref/generation 语义
- `action_state.go`、`risk.go`
- docs/README
- 点赞、收藏、评论、通知选择器

---

## 2. P0：correctness/security + verification

提交建议：`fix: close final correctness and security gaps`

### `xiaohongshu/ready.go`

`probeXHSReady`，基线 134–241 行：

- 第 229 行传给 `searchInputInFeedsSelector` 的实参由 `SelectorSearchInput` 改为 `SelectorSearchInputInFeeds`。
- 其他九个实参及顺序不变。

`probeXHSReadyFull`，基线 245–356 行：

- 第 343 行同样将第八个选择器实参改为 `SelectorSearchInputInFeeds`。
- 不调整 scoped/full probe 判定条件和 fallback。

### `xiaohongshu/ready_test.go`（新增）

增加：

- `TestHomeSearchReadyRequiresDedicatedInputSignal`：`SearchInputInFeedsReady=false` 时，哪怕存在首页 feed 也不得判定 `XHSReadyHomeSearch` ready。
- `TestHomeSearchSelectorIsDedicated`：确认 `SelectorSearchInputInFeeds` 与宽泛 `SelectorSearchInput` 不相等。
- 静态验收额外确认 `ready.go` 两个 Eval 调用不存在 `SelectorLikeButton, SelectorSearchInput, SelectorNotificationPage` 旧参数序列。

### `xiaohongshu/browse_session.go`

`GetInitialCommentIDs`，基线 354–358 行：

```go
return append([]string(nil), s.initialCommentIDs...)
```

锁范围保持不变。

### `xiaohongshu/session_optimization_test.go`

新增 `TestGetInitialCommentIDsDefensiveCopy`：

- 初始化两个 comment ID。
- 获取返回切片后修改元素并 append。
- 再次读取，内部 ID 和长度必须保持原值。

### Deadline 修复

`xiaohongshu/user_profile.go`：

- `UserProfile` 第 24 行改为：

```go
page := u.page.Context(ctx).Timeout(60 * time.Second)
```

- `GetMyProfileViaSidebar` 第 129 行同样恢复 60 秒上限。
- 构造器暂不调整，P2 再整理提取逻辑。

`xiaohongshu/publish.go`：

- `Publish` 第 77 行改为：

```go
page := p.page.Context(ctx).Timeout(300 * time.Second)
```

`xiaohongshu/publish_video.go`：

- `PublishVideo` 第 52 行同样使用 `Context(ctx).Timeout(300*time.Second)`。

外部 ctx 更早的 deadline 继续优先。

### URL 与启动日志脱敏

`xiaohongshu/like_favorite.go`：

- `interactAction.preparePage` 第 70 行只修改日志参数：

```go
logrus.Infof("Opening feed detail page for %s: %s", actionType, redactSensitiveURL(url))
```

- `page.Navigate(url)` 仍使用原 URL。

`xiaohongshu/comment_feed.go`：

- `preparePage` 第 288 行日志改为 `redactSensitiveURL(url)`。
- 导航参数不改。

`xiaohongshu/redact_test.go`（新增）：

- 覆盖 `xsec_token`、`xsecToken`、`access_token`。
- 断言 path 和非敏感 query 保留。
- 断言原 token 零残留。

`browser/browser.go`：

- `maskProxyCredentials`，基线 98–110 行，改为替换原始 userinfo 的实现，输出可读的 `***`/`***:***`，避免 `%2A`。
- 不改变实际传给 `WithProxy` 的值。

`browser/browser_test.go`：

新增代理测试：

- 无认证代理保持原值。
- 用户名代理不出现原用户名。
- 用户名+密码代理不出现任一原凭据。
- 输出包含 `***:***@`。

`third_party/headless_browser/headless_browser.go`：

- `New` 第 190 行：Info 日志仅打印 fingerprint platform，不打印 seed。
- 第 220–223 行：删除 `"args": l.FormatArgs()`，替换为 `"arg_count": len(l.FormatArgs())`。
- 不改变 launcher 实际 flags。
- 保留 bin 日志。
- 本阶段不动第 406/415 行 Debug stderr，留到 P3。

`main.go`：

- 第 67 行改为不带具体数值的日志，例如：

```go
logrus.Info("CloakBrowser fingerprint seed pinned")
```

- `SetFingerprintSeed(seed)` 保持不变。

`third_party/headless_browser/headless_browser_test.go`：

- 保留全部 v0.4.0/扩展测试。
- 增加纯配置测试，确认 fingerprint seed 仍进入 Config/launcher 配置；测试不依赖日志中的 seed。

### 远程图片内存上限

`pkg/downloader/images.go`：

- 常量区新增私有常量：

```go
const maxRemoteImageBytes int64 = 50 << 20
```

- 新增私有函数：

```go
func readImageBody(r io.Reader, limit int64) ([]byte, error)
func displayImageURL(raw string) string
```

`readImageBody`：

- 使用 `io.LimitReader(r, limit+1)`。
- 读取结果超过 limit 时返回明确的“远程图片超过大小上限”错误。
- 不吞掉底层读取错误。

`DownloadImage`，基线 41–104 行：

- `ContentLength > maxRemoteImageBytes` 时读取前直接拒绝。
- 第 74 行改为调用 `readImageBody`。
- HTTP、格式识别、文件命名、写盘顺序不变。
- 第 65、70 行错误中的 URL 改为 `displayImageURL(imageURL)`；该函数保留 scheme/host/path，移除 query 和 fragment。

`DownloadImages` 第 107–122 行：

- 聚合错误中使用 `displayImageURL`。

`pkg/downloader/processor.go`：

- `ProcessImages` 的下载错误不再拼接原始带 query URL，改用同包 `displayImageURL`。

`pkg/downloader/images_test.go`（新增）：

- 小型合法 PNG 成功。
- `Content-Length` 超限时在读 body 前拒绝。
- chunked/未知长度响应通过小 limit 的 `readImageBody` 测试超限。
- 错误信息不包含 URL query 中的 token。
- 非图片仍拒绝。

### Cookie 权限

`cookies/cookies.go`：

`localCookie.write`，基线 94–116 行：

- `os.WriteFile` mode 从 `0644` 改为 `0600`。
- 写成功后执行 `os.Chmod(c.path, 0600)`。
- chmod 失败必须返回错误。
- v1/v2 解析、seed 保留和目录创建逻辑不变。

`cookies/cookies_test.go`（新增）：

- `SaveSeed` 新建文件后权限为 `0600`。
- 预先建立 `0644` 文件，再 `SaveCookies` 后权限收紧为 `0600`。
- seed 与 cookies 相互保存逻辑不变。
- v1 裸 cookie 数组仍可读取。

### CI

`.github/workflows/build.yml`：

- push branches 临时加入 `fixup-test`。
- 新增 `verify` job，Linux amd64、Go 1.24：

```bash
go test ./...
go build ./...
(
  cd third_party/headless_browser
  go test ./...
)
```

- 原 `build` matrix 增加 `needs: verify`。
- 原交叉编译与 artifact 名称不变。

### P0 验收

- 根 module `go test ./...`。
- headless 子 module `go test ./...`。
- `go build ./...`。
- `git diff --check`。
- `rg 'SelectorLikeButton, SelectorSearchInput, SelectorNotificationPage' xiaohongshu/ready.go` 零结果。
- 日志源码中无 `xsec_token` 原值、代理密码、完整 launcher args、具体 fingerprint seed 输出。
- 22 个 MCP 注册完全未动。

---

## 3. P1：mechanical cleanup

提交建议：`refactor: remove final mechanical duplication`

### `mcp_server.go`

`registerTools`：

- `publish_content` 第 259–273 行：删除 `argsMap`，直接调用：

```go
appServer.handlePublishContent(ctx, args)
```

- `user_profile` 第 319–325 行：直接调用 `handleUserProfile(ctx, args)`。
- `publish_with_video` 第 371–383 行：直接调用 `handlePublishVideo(ctx, args)`。

文件末尾第 612–619 行：

- 删除 `convertStringsToInterfaces`。

不得改三个 Args 结构、JSON tag、schema 文案或 tool registration。

### `mcp_handlers.go`

删除第 72–82 行 `parseVisibility`。

`handlePublishContent`，基线 339–419 行：

- 签名改为接收 `PublishContentArgs`。
- 直接使用 `args.Title/Content/Images/Tags/ScheduleAt/IsOriginal/Visibility/Products/ConfirmToken`。
- 删除 map type assertion 和 slice 转换循环。
- `PublishRequest`、confirmation key 参数顺序、summary、错误文案不变。

`handlePublishVideo`，基线 422–500 行：

- 签名改为接收 `PublishVideoArgs`。
- 直接使用 typed fields。
- 保留 video 空值校验、confirmation key 和错误文案。

`handleListFeeds`，基线 503–546 行：

- 删除第 523–545 行 marshal/unmarshal 往返。
- 成功后直接：

```go
return jsonMCPResultWithTools(result, afterFeedsTools)
```

`handleUserProfile`，基线 549–609 行：

- 签名改为 `UserProfileArgs`。
- 继续分别校验 `UserID` 和 `XsecToken`，原错误文案不变。
- 调用链仍为 `service.UserProfile(ctx, args.UserID, args.XsecToken)`。

### `service.go`

删除死代码：

- `XiaohongshuService.rateLimitFunc`，第 32 行。
- `SetRateLimiter` 第 68–78 行闭包；函数只保留 `s.rateLimiter = limiter`。
- `acquirePage`，第 1762–1764 行。
- 保留 `rateLimiter`、`acquirePageFor` 和所有 HTTP 全局路径。

新增两个私有 helper，放在现有 `setFeedCursor/getFeedCursor/delFeedCursor` 附近：

```go
func (s *XiaohongshuService) loadFeedCursor(
    cursorID, sessionID, queryKey string,
) (*xiaohongshu.FeedCursor, error)

func (s *XiaohongshuService) commitFeedCursor(
    oldCursorID, sessionID, queryKey string,
    next *xiaohongshu.FeedCursor,
    hasMore bool,
) (nextCursorID string, seenCount int)
```

语义：

- 空 cursor 返回 nil。
- 校验错误文案保持“feed cursor 不存在或已过期”及现有 `Validate` 文案。
- 返回 clone，不返回存储对象。
- 新 cursor ID 格式仍为 `fc_<session>_<UnixNano>`。
- 仅成功完成页面批次后消费旧 cursor。
- `seenCount` 仍取 `next.ReturnedIDs`，即使 `hasMore=false` 也保持现状。

`SessionListFeeds` 第 1310–1354 行和 `SessionSearch` 第 1356–1406 行：

- 只用上述 helper 替换重复 cursor 段。
- 首页 query key 仍为 `home::`。
- 搜索 query key、AIChat、风控记录不变。

### 死代码

`xiaohongshu/feed_detail.go`：

- 删除 `getCommentCount` 第 1037–1065 行。
- 删除 `checkEndContainer` 第 1067–1107 行。
- 仅按实际引用清理 import；仍被其他函数使用的 `retry`、`strings`、`time` 不删。

`xiaohongshu/search.go`：

- 删除 `waitForFilterRefresh` 第 631–659 行。
- 保留 `waitFeedsChanged` 和当前搜索刷新链。

`xiaohongshu/browse_session.go`：

- 删除 `currentFeed` 第 1937–1940 行。
- 删除 `ensureReadableInteraction` 第 1954–1964 行。
- 保留 `currentFeedFor` 和实际阅读校验链。

`xiaohongshu/comment_feed.go`：

- `PostComment` 删除第 74 行重复：

```go
page = f.page.Context(ctx).Timeout(120 * time.Second)
```

- 后续继续使用 `preparePage` 返回的 page。
- 两次 `checkPageAccessible` 均保留。

### P1 验收

- `rg` 确认已删符号零引用。
- typed handler 三条调用链编译通过。
- 22 个工具名和参数 schema 与 P0 完全一致。
- confirmation key 单测/静态参数顺序不变。
- feed cursor 首批、续页、scope mismatch、旧 cursor 消费行为不变。
- 根/子 module 测试、build、diff-check 全通过。

---

## 4. P2：借鉴 upstream 的主页 active tab 解析

提交建议：`fix: return only active profile note tab`

### 前置取证

现有 `p0-evidence.md` 只有主页 DOM tab，不包含真实 `__INITIAL_STATE__`。实施 P2 前必须用已登录真实主页读取：

- `user.userPageData.value/_value`
- `user.notes.value/_value`
- `user.activeTab.value/_value`
- `activeTab.index`
- `activeTab.query`

落盘前删除昵称、用户 ID、xsec token、图片 URL 和真实内容，只保留字段层级、三组不同占位 feed ID、active index/query。

### `xiaohongshu/testdata/user_profile_state_note.json`

新增真实结构脱敏 fixture：

- 三个 notes 分区分别放不同 ID。
- active index 指向笔记分区。
- query 为真实页面取得的 note 值。
- 同时保留真实页面采用的 value 或 `_value` 包装形式证据。

### `xiaohongshu/user_profile.go`

`UserProfile` 和 `GetMyProfileViaSidebar`：

- 保留 P0 的 `Context(ctx).Timeout(60*time.Second)`。

`extractUserProfileData`，基线 38–122 行：

- `window.__INITIAL_STATE__` wait 保留。
- 两次提取 Eval 合并成一次 Eval。
- JS 内统一 `unwrap(value/_value)`。
- 返回一个 JSON envelope，包含：
  - `userPageData`
  - `notes`
  - `index`
  - `query`
- userPageData 或 notes 缺失时保留现有错误语义。

新增私有 Go 类型及函数：

```go
type userProfileStateSnapshot struct { ... }

func parseUserProfileState(raw string) (*UserProfileResponse, error)
```

解析规则：

- query 非空且不是默认 note 时 fail-closed。
- index 必须位于 notes 范围内；越界返回明确错误，不展平 fallback。
- `Feeds` 仅复制 `Notes[Index]`。
- 不添加 `ProfileTab` exported 类型。
- 不修改 `UserProfileArgs`，不新增 tab 参数。
- `makeUserProfileURL` 不变。

### `xiaohongshu/user_profile_test.go`

覆盖：

- 真实 fixture 只返回 active note 分区。
- fav/liked 占位 ID 不得混入。
- query 与默认 note 不符时失败。
- index 越界失败。
- userPageData 缺失失败。
- notes 缺失失败。
- `value` 与 `_value` 两种规范化结果均能解析。

### P2 验收

- `user_profile` 工具参数仍只有 `user_id/xsec_token`。
- MCP 工具数仍为 22。
- 默认主页返回内容不混入收藏/点赞分区。
- 单次 extraction Eval；Wait 不计入 extraction Eval。
- 真实主页冒烟结果与 active“笔记”tab 卡片 ID 对齐。
- P0/P1 全部测试无回归。

---

## 5. P3：重复 JS 与 Debug 输出精简

提交建议：`refactor: deduplicate stable DOM probe helpers`

### `xiaohongshu/dom_extract.go`

在 `interactStateJS` 附近新增私有 JS 常量：

```go
const domCleanJS = `...`
const domNoteHelpersJS = `...`
const domCommentExtractorJS = `...`
```

要求：

- `domCleanJS` 只定义现有 `clean`。
- `domNoteHelpersJS` 只包含完全相同的 `pickText/pickAttr/countNear`。
- `domCommentExtractorJS` 定义 `extractComments(feedID)`，内容逐字符源自当前三处相同评论遍历逻辑。

替换：

- `ExtractOpenedNoteSnapshotFromDOM` 第 44–146 行中的重复 helper/comment 段。
- `ExtractFeedDetailFromDOM` 第 275–379 行中的重复段。
- `ExtractCommentsFromDOM` 第 391–438 行中的重复段。

禁止：

- 改选择器。
- 改字段名、过滤规则或 ID fallback 顺序。
- 把 `ExtractCommentsFromDOM` 改为完整详情快照。
- 增加 Eval 次数。
- 改 `interactStateJS` href 判定。

### `xiaohongshu/ready.go`

在现有 `xhsProbeVisibleJS/xhsProbeFeedMatchJS` 附近新增：

- `xhsProbeCollectionJS`：`count/visibleCount/unwrap/sizeOf`。
- `xhsProbeRiskJS`：当前相同 risk keyword 与 riskText 生成。
- `xhsSearchInputReadyJS`：定义按需调用的 `searchInputReady(selector)`，仅函数定义，不在 common 段主动查询 DOM。

`probeXHSReady`：

- 拼接公共常量。
- 仅 `home_search` 分支调用 `searchInputReady(searchInputInFeedsSelector)`。

`probeXHSReadyFull`：

- 复用同一函数并主动计算首页搜索输入状态。
- 保留 full probe 全量诊断字段。

新增私有 `decodeXHSReadyProbe`，统一 nil 检查和 JSON unmarshal。

必须保留 P0 的 `SelectorSearchInputInFeeds` 实参。

### Debug 输出

`browser/browser_manager.go`：

- `newPage` 第 341–349 行把 `fmt.Fprintf(os.Stderr, ...)` 改为 `logrus.WithError(err).Debug(...)`。
- 删除不再使用的 `os` import，新增 logrus import。
- panic 转 error 行为不变。

`third_party/headless_browser/headless_browser.go`：

- `Browser.Page` 第 402–423 行两处 stderr 改为 logrus Debug。
- panic/error 返回值不变。
- 删除不再使用的 `os` import。
- 不触碰 New、CloseContext、Health、UA override。

### P3 验收

- 三个 DOM extractor 的 JSON fixture/真实页面输出字段一致。
- 点赞收藏仍只读 `svg use href`。
- scoped ready 仍只计算当前 kind 必需信号。
- full probe 仍只在 PageState/超时诊断执行。
- `rg 'fmt\\.Fprintf\\(os\\.Stderr' browser third_party/headless_browser` 零结果。
- Eval 数量不增加。
- 根/子 module 测试全部通过。

---

## 6. 最终验收

必须同时满足：

- 22 个正式 MCP 工具，名称、参数、annotations、confirm_token 全部不变。
- notification cursor/ref/generation 与事务式提交无 diff。
- `humanize/**`、`go.mod`、`go.sum` 无 diff。
- `go test ./...` 通过。
- `cd third_party/headless_browser && go test ./...` 通过。
- `go build ./...` 通过。
- 现有跨平台 build matrix 通过。
- `git diff --check` 通过。
- 无新增 TODO/FIXME。
- 无旧死代码或已删 helper 悬空引用。
- 日志不输出原始 xsec token、代理认证、完整 launcher args、fingerprint seed。
- 远程图片超限 fail-closed，正常图片发布不变。
- Cookie 文件保存后权限为 `0600`。
- 真实浏览器回归：首页搜索、打开笔记、详情、点赞、收藏、评论、回复、通知三 tab、通知点赞/回复、主页默认笔记。
- 不增加机械页面导航、JS scroll fallback、短 `.Timeout()` 污染或额外固定停留。


tokens used
3,399,850
