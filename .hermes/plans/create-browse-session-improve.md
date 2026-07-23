# 改进 create_browse_session：复用检测 + 状态报告

## 目标

让 `create_browse_session` 在调用时：
1. 先检查当前是否有可用 session
2. 有则复用（需先做健康检查）
3. session 忙碌/初始化中 → 返回 busy/initializing 状态，建议 wait
4. session 异常（崩了/CDP断开/风险）→ 返回 unhealthy 状态，建议 recreate
5. 正常健康 → 续期复用，返回 reused
6. 无 session → 新建

## 具体改动

### 1. 引入 Session 状态枚举

在 `xiaohongshu/browse_session.go` 中增加：

```go
type BrowseSessionStatus string

const (
    SessionInitializing BrowseSessionStatus = "initializing"
    SessionReady        BrowseSessionStatus = "ready"
    SessionBusy         BrowseSessionStatus = "busy"
    SessionNotReady     BrowseSessionStatus = "not_ready"
    SessionExpired      BrowseSessionStatus = "expired"
    SessionClosed       BrowseSessionStatus = "closed"
    SessionUnhealthy    BrowseSessionStatus = "unhealthy"
)
```

在 `BrowseSession` struct 中增加 `status` 字段：
```go
status  BrowseSessionStatus
```

### 2. Create 返回结构化结果

在 `xiaohongshu/browse_session.go` 中增加：

```go
type CreateBrowseSessionResult struct {
    Outcome           string                  `json:"outcome"`           // "created" / "reused" / "blocked"
    Session           *BrowseSessionInfo      `json:"session,omitempty"`
    Status            BrowseSessionStatusInfo `json:"status"`
    RecommendedAction string                  `json:"recommended_action"` // "continue" / "wait" / "recreate" / "login"
}

type BrowseSessionStatusInfo struct {
    Status          BrowseSessionStatus `json:"status"`
    Session         BrowseSessionInfo   `json:"session,omitempty"`
    Operation       string              `json:"operation,omitempty"`
    OperationSince  time.Time           `json:"operation_since,omitempty"`
    LastError       string              `json:"last_error,omitempty"`
    HealthCheckedAt time.Time           `json:"health_checked_at,omitempty"`
    Ready           bool                `json:"ready"`
    Risk            string              `json:"risk,omitempty"`
}
```

### 3. 在 service.go 中实现复用+健康检查逻辑

修改 `CreateBrowseSession` 方法（`service.go:865`）：

1. 先检查 active session
2. 如果有：
   a. 尝试非阻塞获取 opToken → 失败返回 busy
   b. 检查 page 健康（轻量 Eval + CDP Health）
   c. 健康则 Renew() 返回 reused
   d. 不健康则标记 unhealthy，返回 blocked
3. 如果无 active session 或有 tombstone → 返回状态建议
4. 确认无异常后新建

### 4. 增加不触 TTL 的健康检查方法

在 `BrowseSession` 上增加：

```go
func (s *BrowseSession) CheckReusable(ctx context.Context) ReuseCheck
```

检查顺序：
1. 非阻塞获取 opToken → 失败返回 busy
2. closed → 返回 closed
3. TTL 检查（不续期）
4. page != nil
5. 短超时 browser CDP health (Browser.getVersion)
6. 轻量 page Eval (location.href + document.readyState)
7. 分类返回

### 5. 修正初始化期暴露问题

`createBrowseSession` 的流程改成：
```
CreateBrowseSession()
  ├─ 加 create mutex（防止并发创建）
  ├─ 检查/复用（上面第3步）
  ├─ 新流程：先 Navigate + WaitForXHSReady → 完成后再注册进 manager
  ├─ 设置 status = ready
  └─ 释放 mutex
```

### 6. 增加 tombstone 机制

在 `BrowseSessionManager` 中增加：
```go
type SessionTombstone struct {
    Info      BrowseSessionInfo
    Status    BrowseSessionStatus
    ClosedAt  time.Time
    Reason    string
}
var lastTombstone *SessionTombstone
```

TTL 超时/关闭时写入 tombstone，保留 1-5 分钟。create 时先查 tombstone。

### 7. MCP handler 增加 force_recreate 参数

`mcp_server.go:446` 的 handler 改成接收参数：

```go
type CreateBrowseSessionArgs struct {
    ForceRecreate bool `json:"force_recreate,omitempty"`
}
```

`force_recreate=true` → 关闭旧 session 后重建。

### 8. 调整限流位置

只在真正需要新建时才执行 `ActionBrowse` 限流。复用路径（健康检查 + 续期）不走限流。

## 不改的范围

- 不改其他 session 操作工具（session_search, session_detail 等）
- 不改 browser/browser_manager.go
- 不改 third_party/headless_browser
- 不添加新的 MCP 工具
- 不添加测试文件（本机无 Go 工具链）

## 注意事项

- 不要本机安装/运行 Go
- 不要提交/推送代码
- 不要重启服务
