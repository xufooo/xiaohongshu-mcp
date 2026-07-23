修基于测试报告发现的 3 个 P1 ISSUE：

## ISSUE 1 (P1): [browse_session.go:25] healthCheckTimeout=5s 太短

RPi 上单次 CDP Eval 能到 30s，5s 会把健康但慢的 session 误判不可用。
修复：`healthCheckTimeout` 改成 30s。

## ISSUE 2 (P1): [service.go:948] CDP 超时落入 default→recreate

CheckReusable 因超时返回的 SessionNotReady/Expired 落入 default 分支返回 recreate。
修复：在 default 分支中，如果是超时原因（LastError 含"超时"或"CDP"），改为返回 blocked + recommended_action="retry"，不要自动 recreate。
或者在 tryReuseSession 的 switch 中加 `case SessionExpired:` 和 `case SessionNotReady:` 分别处理。

简单方式：在 switch 中加：
```go
case xiaohongshu.SessionExpired, xiaohongshu.SessionNotReady:
    return &xiaohongshu.CreateBrowseSessionResult{
        Outcome:           "blocked",
        RecommendedAction: "retry",
        Status: xiaohongshu.BrowseSessionStatusInfo{
            Status:    check.Status,
            LastError: check.LastError,
        },
    }
```

## ISSUE 3 (P1): [mcp_handlers.go:958] 限流在复用检查前

复用路径不应该占 ActionBrowse 限流额度。
修复：handler 层不再对所有调用做限流，在 service.CreateBrowseSession 内部"需要新建"时再限流。
给 XiaohongshuService 加一个限流回调字段，service 初始化时从 AppServer 传入。

具体改法：
1. 在 service.go 的 XiaohongshuService struct 加字段：
```go
rateLimitFunc func(ctx context.Context, name string, action ratelimit.Action) bool
```

2. 在 NewXiaohongshuService 加参数或 SetRateLimiter 方法，调用方（main.go）传入。

3. 在 service.CreateBrowseSession 中，tryReuseSession 返回 nil（需要新建）后、CloseAll 前调 rateLimitFunc。

4. handler 中移除 rateLimitMCP 调用。

注意：
- 不要跑/安装Go
- 不要提交/推送
- 不要重启服务
- 保持简洁
