修 Codex 发现的 2 个 ISSUE：

## ISSUE 1: [xiaohongshu/browse_session.go:10] crypto/rand 和 math/rand 冲突

`crypto/rand` 和 `math/rand` 同时导入，名称冲突且 `math/rand` 未使用。
修复：移除 `math/rand` 导入（只需保留 `crypto/rand`）。

## ISSUE 2: [service.go:980] SessionNotReady 落入 default

`tryReuseSession` 中的 switch 没有 `case SessionNotReady`，`CheckReusable` 返回的取消/超时状态落入 `default` 分支，仍然关闭 session 并要求重建。
修复：在 switch 中加 `case xiaohongshu.SessionNotReady:`，不做任何破坏操作，直接返回 blocked + recommended_action="wait" 或 "retry"。

注意：不要改动其他代码，不要扩展 scope。
