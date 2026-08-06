# user_profile 点击式打开 + open_note 返回作者链接 — 实施单

基线：fixup-test @ acdb72c。只改代码，禁止安装/下载 Go 工具链，禁止跑 gofmt/test/build（CI 验证）。

## 背景（实验结论）
- RPi 上 user_profile 直接 Navigate 用户主页慢：Page.navigate 90s 超时（当前标签完整导航），MCP 60s Wait 不够 → 反复 `wait for profile state failed`。
- 实测：当前页（XHS 已加载）用动态 a 标签（href=profile URL, target=_self）+ click()，SPA 路由当前页打开，数秒 complete 且 userPageData 就绪；back 可正常返回；不新建标签。
- 新标签打开也快但 MCP 无法可靠关闭（go-rod Close 卡死历史问题）。
- 老大确认方向：user_profile = 拿 user_id+xsec_token（schema 不变）构造 URL，用点击式当前页打开替代 Navigate。

## 改动 1：xiaohongshu/user_profile.go — UserProfile() 点击式打开
现状（L29-32）：`page.Navigate(searchURL)`（60s 超时风险）。
改为：用 Eval 动态创建 a 标签点击（当前页 SPA 路由打开），Eval 立即返回不等待 load：
```go
searchURL := makeUserProfileURL(userID, xsecToken)
// 点击式当前页打开：动态 a 标签 target=_self + click()，触发 SPA 路由，
// 避免直接 Navigate 在当前标签上完整导航（RPi 上 90s 超时）。
// Eval 立即返回，随后由下方 Wait 等待目标数据就绪。
if _, err := page.Eval(`(url) => {
    const a = document.createElement('a');
    a.href = url;
    a.target = '_self';
    a.style.display = 'none';
    document.body.appendChild(a);
    a.click();
    a.remove();
}`, searchURL); err != nil {
    return nil, fmt.Errorf("open user profile by click failed: %w", err)
}
```
- 保留后续 Wait（`readyState !== "loading" && userPageData 就绪`）与两次小 Eval 提取逻辑（acdb72c 现状，不动）。
- makeUserProfileURL 不动（保持基线行为，不扩大改动）。
- 注意：Eval 参数传入 url（用 rod 的参数形式），避免字符串拼接注入问题。

## 改动 2：open_note 返回笔记作者 profile 链接
现状：open_note 返回 note.user.userId=''、user.xsecToken=''（详情页数据无此字段）。
改为：组装 open_note 返回时，从该 session 搜索结果 noteCard.user 合并补全：
- 若 note.user.userId 为空 && 搜索结果中该 note_id 对应的 noteCard.user.userId 非空 → 补 userId。
- 若 note.user.xsecToken 为空 && noteCard.user.xsecToken 非空 → 补 xsecToken。
- 不覆盖已有值（DOM 已有 nickname/avatar 等保持）。
- 不拿笔记级 feed.XsecToken（笔记 token）冒充用户主页 token。
- 参考 search.go fillMissingFeedFields 的合并模式；改动位置在 open_note 返回结构组装处（browse_session.go / action_state.go / note_open.go 中实际组装 NoteOpenResult 的函数）。

## 单测（新增/修改，与改动对应）
- 合并补全：目标为空时补齐、目标非空时不覆盖、搜索结果缺字段保持空、不把笔记 token 当用户 token。
- user_profile 点击式打开的函数可单测 URL 构造与 Eval 调用参数（如已有测试框架支持）。

## 验收项（真实 session 验收）
1. search_feeds → open_note：note.user.userId 非空、xsecToken 非空。
2. 用 open_note 返回的 userId+xsecToken 调 user_profile：不再 60s 超时，返回用户基本信息（nickname）+ 笔记列表，确属目标 user_id。
3. open_note → go_back 仍回原搜索结果页；不产生额外标签。

## 风险点（承 Codex 分析）
- 若初始页面是空白页（非 XHS 已加载页），a.click() 收益可能主要来自"Eval 发起同标签导航立即返回"——需真实服务调用路径验证（验收项 2 覆盖）。
- 若 XHS 将路由限制为可信鼠标事件，a.click() 可能失效——届时再考虑"JS 创建可见 anchor + go-rod 原生点击"较重方案。
- notes 可能晚于 userPageData hydrate——acdb72c 既定取舍（extract 快速报错），本次不改。
