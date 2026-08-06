# user_profile / open_note 实施单

## 目标

本次只处理两个行为问题：

1. `user_profile` 不再调用浏览器级 `Navigate` 直达用户主页，改为在当前页面里动态创建 `<a>`，设置 `target="_self"` 后触发真实 `click()`，让页面按站内点击语义在当前标签页打开用户主页。
2. `open_note` 从 session 中已经保存的搜索结果 `Feed.NoteCard.User` 补齐详情 DOM 快照中缺失的作者 `userId` 和作者主页 `xsecToken`；只补空值，不覆盖详情页已有值，也绝不把笔记访问 token 当作作者 token。

不改变 MCP/API 参数，不改变返回结构，不新增依赖。

## 一、user_profile.go：点击式当前页打开

### 改动文件与函数

- 文件：`xiaohongshu/user_profile.go`
- 函数：`(*UserProfileAction).UserProfile`
- 保持不变：`extractUserProfileData`、`parseUserProfileState`、`makeUserProfileURL`

### 具体改动

保留现有的：

- `page := u.page.Context(ctx).Timeout(60 * time.Second)`；
- `searchURL := makeUserProfileURL(userID, xsecToken)`；
- 导航后的 `page.Wait(rod.Eval(...))`，继续等待 `document.readyState !== "loading"` 且 `user.userPageData` 解包后就绪；
- `extractUserProfileData(page)` 内两次小 `Eval`：第一次只提取 `userPageData`，第二次只提取 `notes + activeTab`，随后在 Go 侧拼 envelope 并调用 `parseUserProfileState`。

只把当前这段：

```go
if err := page.Navigate(searchURL); err != nil {
	return nil, fmt.Errorf("navigate to user profile failed: %w", err)
}
```

替换为当前页点击式打开：

```go
if _, err := page.Eval(`(url) => {
	const a = document.createElement("a");
	a.href = url;
	a.target = "_self";
	a.style.display = "none";
	(document.body || document.documentElement).appendChild(a);
	a.click();
	return true;
}`, searchURL); err != nil {
	return nil, fmt.Errorf("click user profile link failed: %w", err)
}
```

这里必须满足：

- URL 仍由 `makeUserProfileURL(userID, xsecToken)` 生成，继续携带用户主页 token 和 `xsec_source=pc_note`；
- 必须是动态 `<a>` 的 `click()`，不是 `location.href = ...`，也不是重新调用 `Navigate`；
- `target` 必须是 `_self`，避免新开标签页后 action 仍绑定旧页面；
- 点击后的原有 `Wait` 不删除、不合并进点击 `Eval`；它负责等待新页面的 profile state 就绪；
- `extractUserProfileData` 的两次小 `Eval` 保留，不退回一次性提取整个 `__INITIAL_STATE__`。

建议同步更新 `UserProfile` 上方注释，把“导航后”等字样改成“点击打开后”，避免实现和注释不一致。

### 错误语义

- 创建并点击链接失败：`click user profile link failed: ...`；
- 点击成功但主页状态未在超时内出现：继续使用现有 `wait for profile state failed: ...`；
- `userPageData` 或 `notes` 缺失：继续由现有两次提取及解析逻辑分别报错。

## 二、open_note：补齐作者 userId / xsecToken

### 改动文件与函数

- 文件：`xiaohongshu/browse_session.go`
- 函数：`(*BrowseSession).OpenNote`
- 可选新增同文件私有 helper：`mergeOpenedNoteUserFromSearchResult`
- 数据来源/既有类型：`xiaohongshu/types.go` 中的 `Feed.NoteCard.User`、`OpenedNoteContent.User`、`User`
- 无需修改：`SessionOpenNoteResponse`、`OpenedNoteContent`、`User` 的结构定义；字段已经存在。

### 改动位置

在 `OpenNote` 完成以下步骤之后：

```go
snapshot, err := ExtractOpenedNoteSnapshotFromDOM(s.page.Context(opCtx), feed.ID)
if err != nil {
	return nil, err
}
content := snapshot.Note
```

并在构造 `SessionOpenNoteResponse`、写入/返回 `content` 之前，立即把 session 搜索结果中的 `feed.NoteCard.User` 合并到 `content.User`。

推荐抽成纯函数，便于精确单测：

```go
func mergeOpenedNoteUserFromSearchResult(content *OpenedNoteContent, searchUser User) {
	if content == nil {
		return
	}
	if content.User.UserID == "" {
		content.User.UserID = searchUser.UserID
	}
	if content.User.XsecToken == "" {
		content.User.XsecToken = searchUser.XsecToken
	}
}
```

调用位置：

```go
content := snapshot.Note
mergeOpenedNoteUserFromSearchResult(&content, feed.NoteCard.User)
comments := snapshot.Comments
```

### 合并规则

- `userId` 的唯一补充来源是当前 `result_ref` 解析出的 session 搜索结果 `feed.NoteCard.User.UserID`。
- 作者 `xsecToken` 的唯一补充来源是同一个搜索结果的 `feed.NoteCard.User.XsecToken`。
- 仅当 `content.User.UserID == ""` 时补 `UserID`。
- 仅当 `content.User.XsecToken == ""` 时补 `XsecToken`。
- DOM/详情快照已有非空值时必须保留，搜索结果不得覆盖。
- 两个字段分别判断；例如详情已有 `userId` 但缺 `xsecToken`，只补 token。
- 搜索结果对应字段也为空时保持为空，不制造数据。
- 不改动昵称、`nickName`、头像等字段；这些继续以详情 DOM 快照为准。

### token 边界（必须遵守）

`feed.XsecToken` 是笔记访问 token，用于：

- `validateFeedAccessArgs(feed.ID, feed.XsecToken)`；
- `OpenFromCards(..., feed.ID, feed.XsecToken, ...)`；
- session 的 `currentXsecToken`，供当前笔记的点赞、收藏、评论等操作使用。

它不是作者主页 token，因此禁止出现下面这种回退：

```go
content.User.XsecToken = feed.XsecToken // 禁止：拿笔记 token 冒充作者 token
```

即使调用方通过 `open_note.xsec_token` 覆盖了 `feed.XsecToken`，这个覆盖也只影响笔记打开流程，不能进入 `content.User.XsecToken`。作者 token 只能来自 `feed.NoteCard.User.XsecToken`（或详情快照本身已有的作者 token）。

## 三、测试与验收

### 涉及测试文件/函数

- `xiaohongshu/user_profile_test.go`
  - 保留现有 `parseUserProfileState` 用例，确认两次小 Eval 后拼出的 envelope 解析语义未变。
  - 若现有浏览器测试设施支持拦截页面行为，增加 `UserProfile` 点击导航用例：断言创建链接、`target == "_self"`、触发 click，并能通过后续 Wait 和两次提取完成返回。
- 建议在 `xiaohongshu/browse_session_test.go`（若该文件尚不存在则新建）对纯合并 helper 增加表驱动测试：
  - 详情两个字段都为空：从 `noteCard.user` 补齐二者；
  - 详情两个字段都非空：均不覆盖；
  - 仅一个字段为空：逐字段补齐；
  - 搜索结果字段为空：详情保持原值/空值；
  - `feed.XsecToken` 与作者 token 不同：结果只能得到 `noteCard.user.xsecToken`，绝不能得到笔记 token。

### 验收标准

1. `xiaohongshu/user_profile.go` 的 `UserProfile` 中不再用 `page.Navigate(searchURL)`，而是动态 `<a target="_self">` 加 `click()`。
2. 点击后原有 profile-state `Wait` 仍存在。
3. `extractUserProfileData` 仍以两次小 `Eval` 分别提取 `userPageData` 和 `notes + activeTab`。
4. `open_note` 返回的 `note.user.userId` / `note.user.xsecToken` 在详情缺失且搜索结果具备时得到补齐。
5. 详情已有作者字段不被搜索结果覆盖。
6. 笔记 `feed.XsecToken` 永远不会被写入作者 `content.User.XsecToken`。
7. 现有 user profile 解析测试与 browse session 相关测试全部通过。

## 四、最终涉及文件清单

必须修改：

- `xiaohongshu/user_profile.go`
  - `(*UserProfileAction).UserProfile`
- `xiaohongshu/browse_session.go`
  - `(*BrowseSession).OpenNote`
  - 可新增 `mergeOpenedNoteUserFromSearchResult`

测试修改/新增：

- `xiaohongshu/user_profile_test.go`（在具备页面行为测试条件时补点击式打开覆盖）
- `xiaohongshu/browse_session_test.go`（合并规则的纯单元测试）

无需修改：

- `xiaohongshu/types.go`（所需字段已经存在）
- `service.go`
- `mcp_handlers.go`
- `mcp_server.go`
- API/MCP schema 与其他业务代码
