补完 create_browse_session 剩余的改动：

## 1. MCP handler 增加 force_recreate 参数

在 mcp_server.go:446 附近，把 handler 改成：

```go
type CreateBrowseSessionArgs struct {
    ForceRecreate bool `json:"force_recreate,omitempty"`
}

// handler 注册处改成：
func(ctx context.Context, req *mcp.CallToolRequest, args CreateBrowseSessionArgs) (...) {
    result := appServer.handleCreateBrowseSession(ctx, args)
    return convertToMCPResult(result), nil, nil
}
```

在 mcp_handlers.go:956，把 handler 改成：

```go
func (s *AppServer) handleCreateBrowseSession(ctx context.Context, args CreateBrowseSessionArgs) *MCPToolResult {
```

然后调用 service 时传 args.ForceRecreate。

同时 mcp_handlers.go 中 handleCreateBrowseSession 里的 rateLimitMCP 调用要移除——限流逻辑已移到 service.CreateBrowseSession 内部。

## 2. 格式化 Go 文件

```
gofmt -w service.go xiaohongshu/browse_session.go mcp_handlers.go mcp_server.go
```

## 不改的范围
- 不改其他工具
- 不要跑/安装 Go
- 不要提交/推送
- 不要重启服务
