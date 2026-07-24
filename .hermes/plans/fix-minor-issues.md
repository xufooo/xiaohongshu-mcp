修 Codex 发现的 4 个 ISSUE：

## ISSUE 1 (P1): [service.go:927] 二次 CheckReusable 无效

二次检查后仍然释放 opToken，不能消除竞态。而且统一 retry 破坏了三分支语义。
**修复：直接删掉二次 CheckReusable 的整个代码块**（约 12 行）。第一次检查足够——如果 session 在 Renew 前被关了，Renew 自身也会失败。

## ISSUE 2 (P2): [browse_session.go:65] 未使用的结构体字段

`Operation`、`OperationSince`、`ReuseCheck.Ready`、`ReuseCheck.Risk` 没有消费者。
**修复：**
- `BrowseSessionStatusInfo` 中删掉 `Operation`、`OperationSince`
- `ReuseCheck` 中保留字段但不删除（内部使用，不影响外部）
- `tryReuseSession` 中不用传 `Risk` 和 `Ready`

## ISSUE 3 (P2): [browse_session.go:1156] 重复的 URL 空检查

第 1146 行已经检查过 `pageState.URL == ""`，后面的检查不会再触发。
**修复：删掉第 1156 行页的重复检查**，只保留 `readyState` 检查。

不改其他代码，不要扩展 scope。
