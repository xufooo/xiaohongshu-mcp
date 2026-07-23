修 Codex 发现的 ISSUE：

## ISSUE: [mcp_handlers.go:958] force_recreate 完全跳过限流

当前写法：`if !args.ForceRecreate` 才 rateLimitMCP，导致 force_recreate 无限重建。

修复：去掉 `!args.ForceRecreate` 条件，所有创建请求都走限流（包括 force_recreate）。复用路径不受影响——复用在 service 内部 tryReuseSession 处理。

改法：在 mcp_handlers.go 的 handleCreateBrowseSession 中：

```go
// 旧
if !args.ForceRecreate {
    if blocked := s.rateLimitMCP(ctx, "创建浏览会话", ratelimit.ActionBrowse); blocked != nil {
        return blocked
    }
}

// 新
if blocked := s.rateLimitMCP(ctx, "创建浏览会话", ratelimit.ActionBrowse); blocked != nil {
    return blocked
}
```

不要改动其他代码，不要扩展 scope。
