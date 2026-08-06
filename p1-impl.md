# P1 唯一实施单

基线：`fixup-test @ 26f810d`。  
本期注册 18 个真实工具；通知 4 工具留到 P2。P0 报告不在 P1 修改范围，P1 也不修改任何选择器。

## 修改白名单

仅允许修改：

- [mcp_server.go](/home/dietpi/work/xiaohongshu-mcp/mcp_server.go:43)
- [mcp_handlers.go](/home/dietpi/work/xiaohongshu-mcp/mcp_handlers.go:15)
- [browse_session.go](/home/dietpi/work/xiaohongshu-mcp/xiaohongshu/browse_session.go:615)
- [docs/API.md](/home/dietpi/work/xiaohongshu-mcp/docs/API.md:3)
- [README.md](/home/dietpi/work/xiaohongshu-mcp/README.md:846)
- [README_EN.md](/home/dietpi/work/xiaohongshu-mcp/README_EN.md:751)

禁止修改 `service.go` 及 `xiaohongshu` 互动实现。现有 `SessionFavorite`、`SessionReply` 已足够暴露。

---

## 1. `mcp_server.go`

### 1.1 参数结构：基线 43–144 行

删除以下旧全局参数结构：

- 43–47：`SearchFeedsArgs`
- 63–69：`PostCommentArgs`
- 81–87：`LikeFeedArgs`

保留以下 session 参数结构作为最终工具 schema，内部 Go 类型名不改：

- 120–126：`SessionSearchArgs` → 给 `search_feeds` 使用。
- 128–132：`SessionOpenNoteArgs` → 给 `open_note` 使用，保留可选 `xsec_token`。
- 134–138：`SessionLikeArgs` → 给 `like_feed` 使用。
- 140–144：`SessionCommentArgs` → 给 `comment_feed` 使用。
- 111–118：`SessionDetailArgs` → 给 `get_note_detail` 使用。

修改 71–79 `ReplyCommentArgs`：

```go
type ReplyCommentArgs struct {
    SessionID   string `json:"session_id" ...`
    CommentID   string `json:"comment_id,omitempty" ...`
    UserID      string `json:"user_id,omitempty" ...`
    Content     string `json:"content" ...`
    ConfirmToken string `json:"confirm_token,omitempty" ...`
}
```

精确要求：

- 删除 `FeedID`。
- 删除 `XsecToken`。
- 新增 `SessionID`。
- `comment_id/user_id` 仍要求至少一个。

修改 89–95 `FavoriteFeedArgs`：

```go
type FavoriteFeedArgs struct {
    SessionID    string `json:"session_id" ...`
    Unfavorite   bool   `json:"unfavorite,omitempty" ...`
    ConfirmToken string `json:"confirm_token,omitempty" ...`
}
```

精确要求：

- 删除 `FeedID`。
- 删除 `XsecToken`。
- 新增 `SessionID`。

更新 97–143 所有 jsonschema：

- “由 `create_browse_session` 返回” → “由 `start_page` 返回”。
- 114 行 cursor 文案：`session_detail` → `get_note_detail`。
- 125 行 cursor 文案：`session_search` → `search_feeds`。
- 131 行保留 `xsec_token` 可选说明。

`UserProfileArgs` 57–61 原样保留，本期不加 `tab`、不改 session 语义。

### 1.2 工具注册：基线 275–592 行

#### `list_feeds`：275–289

名字与 handler 不变，仅确认描述明确要求 `session_id`。

#### `search_feeds`：291–305

保留这个注册位置和正式名字，改为：

- 参数类型：`SessionSearchArgs`
- handler：`appServer.handleSessionSearch`
- 描述：需要 `session_id`，在保留页面中通过真实 UI 搜索。
- 返回的 `result_ref` 可供 `open_note` 使用。

删除 482–496 的整个 `session_search` 注册块。

也就是说：

```text
保留：handleSessionSearch → SessionSearch
删除：handleSearchFeeds → 全局 SearchFeeds
```

#### `user_profile`：307–325

完全保留，不改参数和 handler。

#### 评论：327–347

把原 `post_comment_to_feed` 注册改成：

- Name：`comment_feed`
- 参数：`SessionCommentArgs`
- handler：`appServer.handleSessionComment`
- 描述：评论当前 session 已打开且已阅读的笔记。

删除原来的 `argsMap` 转换。

删除 546–560 的整个 `session_comment` 注册块。

#### 回复：349–378

保留名字 `reply_comment_in_feed`，但改为：

- 参数仍为修改后的 `ReplyCommentArgs`。
- 直接调用 `appServer.handleReplyComment(ctx, args)`。
- 删除 `feed_id/xsec_token` argsMap。
- 注册层可保留 `comment_id/user_id` 至少一个的快速校验，也可统一放 handler；不得两处产生不一致文案。
- 描述改为操作当前 session 笔记中的目标评论。

#### `like_feed`：406–426

保留正式名字，改为：

- 参数类型：`SessionLikeArgs`
- handler：`appServer.handleSessionLike`
- 删除 `feed_id/xsec_token` argsMap。
- 描述改为当前 session 笔记。

删除 530–544 的整个 `session_like` 注册块。

#### `favorite_feed`：428–448

保留名字，改为：

- 使用修改后的 `FavoriteFeedArgs`。
- 直接调用 `appServer.handleFavoriteFeed(ctx, args)`。
- 删除 `feed_id/xsec_token` argsMap。
- 描述改为当前 session 笔记。

#### 页面生命周期和读取：450–592

只改公开注册名、Title 和描述，handler 函数名不改：

| 基线行 | 新注册名 | 保留的内部 handler |
|---|---|---|
| 450–464 | `start_page` | `handleCreateBrowseSession` |
| 466–480 | `get_page_state` | `handleSessionState` |
| 498–512 | `open_note` | `handleSessionOpenNote` |
| 514–528 | `get_note_detail` | `handleSessionDetail` |
| 562–576 | `go_back` | `handleSessionBack` |
| 578–592 | `close_page` | `handleCloseBrowseSession` |

同步 `withPanicRecovery()` 的字符串为正式工具名。

描述中的旧工具引用同步：

- `session_open_note` → `open_note`
- `session_detail` → `get_note_detail`
- “session 工具”改为“页面工具”或“需要 `session_id`”。

### 1.3 注册数量：594 行

```go
logrus.Infof("Registered %d MCP tools", 18)
```

本期不注册 notification 工具，不保留旧名 alias。

---

## 2. `mcp_handlers.go`

## 2.1 `available_tools`：基线 15–27 行

内部变量名可保留，不做机械重命名；只改内容：

```go
var sessionBaseTools = []string{"close_page"}
var sessionCreateTools = []string{"start_page", "check_login_status"}

var (
    afterCreateTools = append([]string{
        "search_feeds", "list_feeds",
    }, sessionBaseTools...)

    afterFeedsTools = append([]string{
        "open_note", "search_feeds", "list_feeds",
    }, sessionBaseTools...)

    afterSearchTools = append([]string{
        "open_note", "search_feeds", "list_feeds",
    }, sessionBaseTools...)

    afterOpenTools = append([]string{
        "like_feed",
        "favorite_feed",
        "comment_feed",
        "reply_comment_in_feed",
        "get_note_detail",
        "go_back",
    }, sessionBaseTools...)

    afterBackTools = append([]string{
        "search_feeds", "open_note", "list_feeds",
    }, sessionBaseTools...)

    afterCloseTools = sessionCreateTools
)
```

关键点：

- 关闭页面后不推荐 `list_feeds/search_feeds`，因为没有有效 `session_id`。
- `user_profile` 本期仍是全局路径，活跃 page 时会被 browser busy 拦截，因此不放入 `afterOpenTools`。

## 2.2 busy 文案：基线 49–85 行

修改 57–58：

```text
Use get_page_state or close_page first.
```

删除旧文案：

```text
Use session_* tools
close_browse_session
```

完成 handler 合并后，68–86 的 `requireBrowserForMCPWithFeed` 应无调用。确认无引用后删除整个函数及其“P2 旧工具委托”注释。

保留 `requireBrowserAvailableForMCP`，因为登录、发布和 `user_profile` 仍使用全局浏览器路径。

## 2.3 next-step：基线 175–220 行

内部函数名不改，只改返回值：

- `sessionNextStepCreateSession`
  - Tool：`start_page`
  - Hint：先调用 `start_page` 获取 `session_id`。
- `sessionNextStepState`
  - Tool：`get_page_state`
- `sessionNextStepSearch`
  - Tool：`search_feeds`
- `sessionNextStepSearchInput`
  - Tool：`search_feeds`
- `sessionNextStepOpenNote`
  - Tool：`open_note`
  - Hint 中 `session_state.results` → `get_page_state.results`
- `sessionNextStepCommentInput`
  - Tool：`comment_feed`

不得重命名这些 Go 函数。

## 2.4 搜索合并

删除基线 535–592 的旧全局 `handleSearchFeeds` 整个函数。

保留 934–956 的 `handleSessionSearch`，只改用户文案：

- `session搜索失败` → `搜索失败`
- 保持调用：
  ```go
  s.xiaohongshuService.SessionSearch(...)
  ```
- 保持 cursor、max_items、filters 和 `afterSearchTools` 行为。

最终唯一调用链：

```text
search_feeds
→ handleSessionSearch
→ XiaohongshuService.SessionSearch
→ BrowseSession.SearchBatchWithAI
```

禁止调用 `XiaohongshuService.SearchFeeds`。

## 2.5 点赞合并

删除基线 657–698 的旧全局 `handleLikeFeed`。

保留 1020–1038 的 `handleSessionLike`，修改：

- `session点赞` → `点赞`
- `session取消点赞` → `取消点赞`
- confirmation action/key：
  ```go
  writeConfirmationKey("like_feed", args.SessionID, args.Unlike)
  requireWriteConfirmation("like_feed", ...)
  ```
- 保持调用：
  ```go
  s.xiaohongshuService.SessionLike(...)
  ```
- 错误前缀改为 `点赞失败`。

最终不再读取 `feed_id/xsec_token`。

## 2.6 评论合并

删除基线 743–814 的旧全局 `handlePostComment`。

保留 1040–1057 的 `handleSessionComment`，修改：

- confirmation action/key：
  ```go
  writeConfirmationKey("comment_feed", args.SessionID, args.Content)
  requireWriteConfirmation("comment_feed", ...)
  ```
- summary 改成：
  ```text
  评论当前笔记: session_id=... content=...
  ```
- 保持调用：
  ```go
  s.xiaohongshuService.SessionComment(...)
  ```
- 错误前缀改为 `评论失败`。

## 2.7 收藏改为 session 语义

修改基线 700–741 的 `handleFavoriteFeed`，函数名不改。

函数签名改为：

```go
func (s *AppServer) handleFavoriteFeed(
    ctx context.Context,
    args FavoriteFeedArgs,
) *MCPToolResult
```

函数体替换为：

1. 校验并 trim `args.SessionID`。
2. 缺失时返回 `sessionNextStepCreateSession()`。
3. 根据 `args.Unfavorite` 生成“收藏/取消收藏”文案。
4. confirmation：
   ```go
   key := writeConfirmationKey(
       "favorite_feed",
       args.SessionID,
       args.Unfavorite,
   )
   ```
5. 调用现有：
   ```go
   s.xiaohongshuService.SessionFavorite(
       ctx,
       args.SessionID,
       args.Unfavorite,
   )
   ```
6. 错误通过 `sessionMCPErrorFromErr(..., sessionNextStepState())`。
7. 成功通过 `jsonMCPResultWithTools(result, afterOpenTools)`。

现有能力位置，仅作调用依据，不修改：

- `service.go:1531–1533`：`SessionFavorite`
- `service.go:1539–1553`：内部 `sessionFavorite`
- `browse_session.go:949–988`：session 页面内收藏执行链

删除：

- `feed_id/xsec_token` 校验。
- `requireBrowserForMCPWithFeed`。
- `FavoriteFeed/UnfavoriteFeed` 全局调用。

## 2.8 回复改为 session 语义

修改基线 816–899 的 `handleReplyComment`，函数名不改。

函数签名改为：

```go
func (s *AppServer) handleReplyComment(
    ctx context.Context,
    args ReplyCommentArgs,
) *MCPToolResult
```

处理顺序：

1. trim/校验 `session_id`。
2. trim `comment_id/user_id/content`。
3. `comment_id` 和 `user_id` 至少一个。
4. `content` 不能为空。
5. confirmation key：
   ```go
   writeConfirmationKey(
       "reply_comment_in_feed",
       args.SessionID,
       args.CommentID,
       args.UserID,
       args.Content,
   )
   ```
6. 调用：
   ```go
   s.xiaohongshuService.SessionReply(
       ctx,
       args.SessionID,
       args.CommentID,
       args.UserID,
       args.Content,
   )
   ```
7. 错误使用 `sessionMCPErrorFromErr`。
8. 成功使用 `jsonMCPResultWithTools(result, afterOpenTools)`。

现有能力位置，仅作调用依据，不修改：

- `service.go:1577–1579`：`SessionReply`
- `service.go:1585–1602`：内部 `sessionReply`
- `browse_session.go:1024–1055`：session 页面内回复执行链

删除：

- `feed_id/xsec_token` 校验。
- `requireBrowserForMCPWithFeed`。
- `ReplyCommentToFeed` 全局调用。

## 2.9 页面 handler 文案：基线 901–1068

函数名全部保留，仅改用户文案：

- `handleCreateBrowseSession`：工具提示引用 `start_page`。
- `handleCloseBrowseSession`：返回字段 `closed_session_id` 可保留，参数仍叫 `session_id`。
- `handleSessionState`：
  - `session状态获取失败` → `页面状态获取失败`
  - fallback → `页面状态获取成功`
- `handleSessionOpenNote`：
  - `session打开笔记失败` → `打开笔记失败`
- `handleSessionDetail`：
  - `session详情获取失败` → `笔记详情获取失败`
  - `session分批加载评论失败` → `分批加载评论失败`
- `handleSessionBack`：
  - `session返回失败` → `返回上一页失败`

“session_id”本身不替换。

---

## 3. `xiaohongshu/browse_session.go`

## 3.1 用户提示：基线 615、635、1559–1569 行

- 615 注释：`session_detail` → `get_note_detail`
- 635 返回文案：`session_detail` → `get_note_detail`
- 1562：
  - `session_open_note` → `open_note`
  - `session_detail` → `get_note_detail`
  - `session_like` → `like_feed`
  - `session_comment` → `comment_feed`
  - 增加 `favorite_feed`
  - 增加 `reply_comment_in_feed`
  - 删除当前不可在活跃 session 中使用的 `user_profile` 推荐
- 1564：
  - `session_search` → `search_feeds`
  - `session_open_note` → `open_note`
- 1566、1568：
  - `session_search` → `search_feeds`

## 3.2 `availableActionsLocked`：1608–1617

改为：

```go
actions := []string{
    "get_page_state",
    "search_feeds",
    "close_page",
}
```

有结果未打开：

```go
"open_note"
```

打开笔记后：

```go
"get_note_detail"
"like_feed"
"favorite_feed"
"comment_feed"
"reply_comment_in_feed"
"go_back"
```

## 3.3 `semanticActionsLocked`：1619–1676

替换现有 Tool 和公开 Ref：

- state → `get_page_state`
- search → `search_feeds`
- open → `open_note:<result_ref>` / Tool `open_note`
- detail → `get_note_detail`
- like → `like_feed`
- comment → `comment_feed`
- back → `go_back`
- close → `close_page`

新增收藏 action：

```go
{
    Ref:      "favorite_feed",
    Tool:     "favorite_feed",
    Label:    "收藏当前笔记",
    FeedID:   s.currentFeedID,
    Requires: "opened",
    Confirm:  true,
}
```

新增回复 action：

```go
{
    Ref:      "reply_comment_in_feed",
    Tool:     "reply_comment_in_feed",
    Label:    "回复当前笔记中的评论",
    FeedID:   s.currentFeedID,
    Requires: "opened + comment_id/user_id",
    Confirm:  true,
}
```

不要填造具体 comment ID。

## 3.4 `recommendedActionLocked`：1679–1723

- not ready：
  - Ref/Tool → `get_page_state`
- 已打开：
  - Ref/Tool → `go_back`
- 未读结果：
  - Tool → `open_note`
- 无结果：
  - Ref/Tool → `search_feeds`

## 3.5 timeline

不修改业务事件名：

```text
list_feeds
search
open_note
detail
like
favorite
comment
reply
back
```

它们是 timeline 事件，不是 MCP 注册名。仅清除 timeline note、summary、hint 中出现的旧工具名；基线现有 timeline note 无需改。

---

## 4. `docs/API.md`

基线 1–11 行之间，在概述后、通用响应格式前新增“MCP 工具目录”。

列出本期实际 18 个：

```text
check_login_status
get_login_qrcode
delete_cookies
publish_content
publish_with_video
start_page
get_page_state
close_page
list_feeds
search_feeds
open_note
get_note_detail
go_back
like_feed
favorite_feed
comment_feed
reply_comment_in_feed
user_profile
```

文档明确：

- `start_page` 返回 `session_id`。
- 页面操作都继续使用 `session_id`。
- 写操作使用 `confirm_token`。
- `open_note` 保留可选 `xsec_token`。
- 通知 4 工具在 P2 注册，本期不列为可用。
- 不修改 33 行后的 HTTP endpoint 名称和 REST 请求结构。

增加最小调用链：

```text
start_page
→ list_feeds / search_feeds
→ open_note
→ get_note_detail / like_feed / favorite_feed / comment_feed
→ go_back
→ close_page
```

---

## 5. `README.md`

替换基线 846–880 的 MCP 工具清单，列出同样的 18 个工具。

参数同步：

- `start_page(force_recreate?)`
- `get_page_state(session_id)`
- `close_page(session_id)`
- `list_feeds(session_id, cursor?, max_items?)`
- `search_feeds(session_id, keyword, filters?, cursor?, max_items?)`
- `open_note(session_id, result_ref, xsec_token?)`
- `get_note_detail(session_id, ...)`
- `go_back(session_id)`
- `like_feed(session_id, unlike?, confirm_token?)`
- `favorite_feed(session_id, unfavorite?, confirm_token?)`
- `comment_feed(session_id, content, confirm_token?)`
- `reply_comment_in_feed(session_id, content, comment_id?/user_id?, confirm_token?)`
- `user_profile(user_id, xsec_token)`

删除：

- `post_comment_to_feed`
- 所有 `like/favorite/reply` 的必需 `feed_id/xsec_token` 说明
- `list_feeds`“无参数”
- `search_feeds` 仅需 keyword 的旧说明

---

## 6. `README_EN.md`

替换基线 751–781 的英文 MCP 工具清单，与中文严格对齐：

- 同样 18 个工具。
- 相同必需/可选参数。
- 删除 `post_comment_to_feed`。
- `like/favorite/comment/reply` 改为 `session_id` 语义。
- 不提前列出 notification 工具。

---

# 实施顺序

1. 修改 `mcp_server.go:43–144` 参数结构。
2. 修改 `mcp_handlers.go`：
   - busy/next-step/available tools；
   - 删除旧全局 search/like/comment handler；
   - favorite/reply 改 session；
   -调整保留的 session handler confirmation key。
3. 回到 `mcp_server.go:275–594` 修改注册指向、删除重复注册、更新数量。
4. 修改 `browse_session.go:615–1723` 的公开语义输出。
5. 修改 `docs/API.md`。
6. 修改 `README.md`、`README_EN.md`。
7. `gofmt`、静态核验、Go 环境测试。
8. 作为一个原子提交交付，不拆分发布。

---

# 分步验收

## 参数与注册验收

- `search_feeds` 参数类型包含 `session_id/cursor/max_items`。
- `like_feed/comment_feed/favorite_feed/reply_comment_in_feed` 均包含 `session_id`。
- `open_note` 保留可选 `xsec_token`。
- 仅注册 18 个工具。
- 没有 notification 注册。
- 没有旧名 alias。

## 调用链验收

必须静态确认：

```text
search_feeds → handleSessionSearch → SessionSearch
like_feed → handleSessionLike → SessionLike
comment_feed → handleSessionComment → SessionComment
favorite_feed → handleFavoriteFeed → SessionFavorite
reply_comment_in_feed → handleReplyComment → SessionReply
```

禁止残留：

```text
handleSearchFeeds → SearchFeeds
handleLikeFeed → LikeFeed/UnlikeFeed
handlePostComment → PostCommentToFeed
handleFavoriteFeed → FavoriteFeed/UnfavoriteFeed
handleReplyComment → ReplyCommentToFeed
```

## 语义输出验收

`get_page_state` 返回的以下字段只能出现正式工具名：

- `available_tools`
- `actions[].tool`
- `actions[].ref` 中作为工具引用的值
- `recommended_action.tool`
- `current.next_hint`
- `summary`

timeline 事件名保持原样。

---

# 最终验收命令

```bash
git diff --check
git diff --name-only
```

`git diff --name-only` 只能出现白名单 6 文件。

检查旧公开名：

```bash
rg -n \
'"(create_browse_session|session_state|session_search|session_open_note|session_detail|session_like|session_comment|session_back|close_browse_session|post_comment_to_feed)"' \
mcp_server.go \
mcp_handlers.go \
xiaohongshu/browse_session.go \
docs/API.md \
README.md \
README_EN.md
```

必须无命中。

检查注册目录：

```bash
rg 'Name:\s+"[^"]+"' mcp_server.go
```

排除 server implementation 的 `xiaohongshu-mcp` 后：

- 恰好 18 个工具。
- 无重复。
- 与本实施单 18 名完全相等。

检查全局路径已移除：

```bash
rg -n 'xiaohongshuService\.(SearchFeeds|LikeFeed|UnlikeFeed|FavoriteFeed|UnfavoriteFeed|PostCommentToFeed|ReplyCommentToFeed)' mcp_handlers.go
```

必须无命中。

Go 环境执行：

```bash
gofmt -w mcp_server.go mcp_handlers.go xiaohongshu/browse_session.go
go test ./...
```

本地无 Go 工具链时交 CI/具备 Go 环境的 runner；仅 build 成功不能替代 `go test ./...`。

最终判定：

- 工具数 18。
- 旧名 0 注册、0 alias。
- search/like/comment 合并完成。
- favorite/reply 使用现有 session 能力。
- 文档与返回的推荐动作一致。
- 无白名单外改动。
- `diff --check` 和 `go test ./...` 通过。


tokens used
1,005,016
