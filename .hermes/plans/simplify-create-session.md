按 Codex 审查意见精简 create_browse_session 改动，把 +356 行压缩到 ~100 行核心逻辑。

## 删除清单

### 1. 删除 tombstone 全套（~60行）
- `tombstoneRetention` 常量
- `SessionTombstone` 结构体
- `lastTombstone` 字段（BrowseSessionManager struct）
- `saveTombstone()`, `TombstoneInfo()`, `ClearTombstone()` 方法
- `remove()` 方法里调用的 `saveTombstone`
- `ClearTombstone()` 的调用

### 2. 删除 isRiskURL（~15行）
- `isRiskURL()` 函数
- `CheckReusable` 中调用的 `isRiskURL`
- `riskPatterns` 数组

### 3. 删除 `status` 持久化字段（~10行）
- `BrowseSession` struct 中的 `status` 字段
- `create()` 中设置 `status: SessionInitializing`
- `close()` 中设置 `s.status = SessionClosed`
- `Status()` 和 `SetStatus()` 方法（返回和设置）
- `service.go` 中 `session.SetStatus(xiaohongshu.SessionReady)`

**保留 `BrowseSessionStatus` 枚举**——作为 `ReuseCheck.Status` 的返回值类型，但要删掉 `SessionInitializing` 和 `SessionClosed`（不再需要持久化）

### 4. 简化 tryReuseSession 的 switch（service.go）

从 7 条分支收敛到 3 条：
1. `SessionReady` → Renew，返回 reused
2. `SessionBusy` → 返回 blocked + wait
3. 其他（包括 unhealthy, not_ready, expired 等）→ 统一返回 blocked + recreate，**不自动 Close()**

## 保留清单
- ✅ `CreateBrowseSessionResult` 和 `BrowseSessionStatusInfo` 结构体（返回给调用方）
- ✅ `ReuseCheck` 结构体（内部检查结果）
- ✅ `CheckReusable()` 方法（opToken + CDP health + JS eval）
- ✅ `createSessionMu` 互斥锁
- ✅ 初始化顺序：Navigate+Wait 完成后再 `session.Create`
- ✅ `BrowseSessionStatus` 枚举（只作返回值用）
- ✅ `forceRecreate` 参数
- ✅ 限流在 handler 层

## 不改的范围
- 不改其他文件/工具
- 不要跑/安装Go
- 不要提交/推送
- 不要重启服务
