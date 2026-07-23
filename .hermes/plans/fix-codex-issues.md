修 Codex 审查发现的 6 个 ISSUE，都在当前工作区。

所有改动保持简洁，不要扩展 scope，不要加测试。

## ISSUE 1: [service.go:877] 强制重建先关 session 再查限流

force_recreate 路径目前顺序：先 CloseAll() → 再限流 → 再创建。
如果限流被拒，旧 session 已销毁，新 session 没创建成。
修复：先把限流额度预占了，再 CloseAll()。

## ISSUE 2: [service.go:868] sync.Mutex 不响应 ctx 取消

`createSessionMu` 是普通 Mutex，`Lock()` 不响应 ctx。
已取消的请求拿到锁后，会用已取消的 ctx 做健康检查，把正常 session 误判为 unhealthy。
修复：获取锁后立即检查 `ctx.Err()` 或使用 `sync/semaphore` 的 `Weighted` 替代。

## ISSUE 3: [xiaohongshu/browse_session.go:1160] CheckReusable 拿到 opToken 就归还

CheckReusable 中 select 拿到 opToken → 立即归还 → 后续健康检查期间其他操作可能进来。
修复：将 opToken 持有到检查完成再归还。同时将 3 次 JS eval 合并为一次：
```js
(() => ({url: location.href, readyState: document.readyState}))()
```

## ISSUE 4: [xiaohongshu/browse_session.go:256] tombstone 读 session 字段没拿锁

tombstone 的 saveTombstone 中读 session.status 和 infoLocked() 时没有持 session.mu。
修复：先在 session 锁下生成完整快照，再写入 manager。

## ISSUE 5: [service.go:924] 正常关闭后 tombstone 挡着不让重建

正常关闭后 3 分钟内 tombstone 存在，非强制 create 一直返回 "recreate"。
但再次调用（不带 force_recreate）还是拿同一个 tombstone，重建不了。
修复方案（二选一，选简单的）：
- 方案 A：tombstone 仅报告不阻挡——看到 tombstone 且有 active session 的痕迹时直接返回 nil（让 service 走新建流程）
- 方案 B：强制重建路径直接从 handler 层传 force_recreate=true

选方案 A——更简单，改动更小。

## ISSUE 6: [xiaohongshu/browse_session.go:68] omitempty 对值类型无效

`BrowseSessionStatusInfo` 中的 `Session BrowseSessionInfo` 和 `OperationSince time.Time` 是值类型，`omitempty` 不会省略零值。
修复：`Session` 改为 `*BrowseSessionInfo`（指针），`OperationSince` 用 `*time.Time` 或直接省略 omitempty 标签（time.Time 零值也不算太大）。

## 不改的范围
- 不改其他工具/文件
- 不要跑/安装 Go
- 不要提交/推送
- 不要重启服务
